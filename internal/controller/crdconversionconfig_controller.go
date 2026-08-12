/*
Copyright 2026 The declarative-conversion-operator Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	applyextv1 "k8s.io/apiextensions-apiserver/pkg/client/applyconfiguration/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/assign"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/crdadapter"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// TargetCRDNameIndex is CRDConversionConfig's counterpart to
// TargetXRDNameIndex — see there for why this exists.
const TargetCRDNameIndex = "spec.targetCRD.name"

// CRDConversionConfigReconciler reconciles a CRDConversionConfig. This is
// XRDConversionConfigReconciler's sibling for plain native
// CustomResourceDefinitions: identical validate -> resolve -> health-gate
// -> patch ordering and identical safe-delete semantics, differing only in
// which kind of target resource it reads from and patches (a typed
// apiextensions.k8s.io/v1 CustomResourceDefinition here, versus an
// unstructured Crossplane CompositeResourceDefinition there).
type CRDConversionConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// DefaultServerNamespace is used for instances that don't set
	// spec.namespace — normally the operator's own install namespace.
	DefaultServerNamespace string
}

// +kubebuilder:rbac:groups=terasky.com,resources=crdconversionconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=terasky.com,resources=crdconversionconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=terasky.com,resources=crdconversionconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=terasky.com,resources=conversionwebhookservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *CRDConversionConfigReconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var cfg teraskyv1alpha1.CRDConversionConfig
	if err := r.Get(ctx, req.NamespacedName, &cfg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !cfg.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &cfg)
	}

	if !controllerutil.ContainsFinalizer(&cfg, teraskyv1alpha1.CRDConversionConfigFinalizer) {
		controllerutil.AddFinalizer(&cfg, teraskyv1alpha1.CRDConversionConfigFinalizer)
		if err := r.Update(ctx, &cfg); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	result, err := r.reconcileNormal(ctx, &cfg)
	if err != nil {
		logger.Error(err, "reconcile failed")
	}
	return result, err
}

func (r *CRDConversionConfigReconciler) reconcileNormal(ctx context.Context, cfg *teraskyv1alpha1.CRDConversionConfig) (ctrl.Result, error) {
	// PhaseStale means a prior apply still stands (KeepServingStale /
	// health-gate hold). Treat it like Applied so a later reconcile does
	// not demote persistent drift to Invalid or drop ConditionStale.
	wasApplied := cfg.Status.Phase == teraskyv1alpha1.PhaseApplied ||
		cfg.Status.Phase == teraskyv1alpha1.PhaseStale
	orig := cfg.DeepCopy()

	// Step 1: fetch the target CRD.
	var crd extv1.CustomResourceDefinition
	if err := r.Get(ctx, types.NamespacedName{Name: cfg.Spec.TargetCRD.Name}, &crd); err != nil {
		if apierrors.IsNotFound(err) {
			r.setInvalid(cfg, wasApplied, fmt.Sprintf("target CustomResourceDefinition %q not found", cfg.Spec.TargetCRD.Name))
			return ctrl.Result{}, r.patchStatus(ctx, orig, cfg)
		}
		return ctrl.Result{}, fmt.Errorf("getting target CRD: %w", err)
	}
	cfg.Status.ObservedCRDGeneration = crd.Generation

	// Step 2+3: analyze against the live schema.
	source := crdadapter.New(&crd)
	ruleSets, err := cfg.ToRuleSets()
	if err != nil {
		r.setInvalid(cfg, wasApplied, fmt.Sprintf("invalid rule configuration: %v", err))
		return ctrl.Result{}, r.patchStatus(ctx, orig, cfg)
	}
	report, err := engine.Analyze(engine.AnalyzeInput{Source: source, HubVersion: cfg.Spec.HubVersion, Spokes: ruleSets})
	if err != nil {
		r.setInvalid(cfg, wasApplied, fmt.Sprintf("analysis failed: %v", err))
		return ctrl.Result{}, r.patchStatus(ctx, orig, cfg)
	}

	populateCRDSpokeStatuses(cfg, report)
	cfg.Status.OverallLossless = report.OverallLossless()
	cfg.Status.SchemaHash = computeCRDSchemaHash(cfg, report)

	if report.HasErrors() {
		msg := "one or more spoke versions failed validation; see status.spokeStatuses for details"
		var revertErr error
		if failClosedShouldRevert(cfg.Spec.DriftPolicy, cfg.Status.Phase, cfg.Status.Conditions) {
			if err := r.revertCRD(ctx, cfg.Spec.TargetCRD.Name); err != nil {
				// Do not claim a successful revert: the CRD may still be
				// serving webhook conversion. Keep Phase=Failed with an
				// honest message, surface RevertFailed on ConditionApplied,
				// and return the error so reconcile requeues with backoff.
				revertErr = err
				logger := log.FromContext(ctx)
				logger.Error(err, "failed to revert CRD after drift under FailClosed policy")
				cfg.Status.Phase = teraskyv1alpha1.PhaseFailed
				meta.RemoveStatusCondition(&cfg.Status.Conditions, teraskyv1alpha1.ConditionStale)
				msg = fmt.Sprintf("schema drift invalidated a previously-applied config; failed to revert to strategy=None per driftPolicy=FailClosed: %v", err)
				meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
					Type: teraskyv1alpha1.ConditionApplied, Status: metav1.ConditionFalse, Reason: teraskyv1alpha1.ReasonRevertFailed, Message: msg,
				})
			} else {
				cfg.Status.Phase = teraskyv1alpha1.PhaseFailed
				meta.RemoveStatusCondition(&cfg.Status.Conditions, teraskyv1alpha1.ConditionStale)
				msg = "schema drift invalidated a previously-applied config; reverted to strategy=None per driftPolicy=FailClosed"
				meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
					Type: teraskyv1alpha1.ConditionApplied, Status: metav1.ConditionFalse, Reason: teraskyv1alpha1.ReasonReverted, Message: msg,
				})
			}
		} else if wasApplied {
			msg = "schema drift invalidated this config, but the previously-applied webhook configuration is left untouched (driftPolicy=KeepServingStale); fix the config or the CRD to clear this"
			setPhaseStale(&cfg.Status.Conditions, &cfg.Status.Phase, "SchemaDrift", msg)
		} else {
			cfg.Status.Phase = teraskyv1alpha1.PhaseInvalid
			meta.RemoveStatusCondition(&cfg.Status.Conditions, teraskyv1alpha1.ConditionStale)
		}
		meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
			Type: teraskyv1alpha1.ConditionValidated, Status: metav1.ConditionFalse, Reason: "ValidationFailed", Message: msg,
		})
		cfg.Status.Message = msg
		if err := r.patchStatus(ctx, orig, cfg); err != nil {
			return ctrl.Result{}, err
		}
		if revertErr != nil {
			return ctrl.Result{}, fmt.Errorf("reverting CRD after FailClosed drift: %w", revertErr)
		}
		return ctrl.Result{}, nil
	}

	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.ConditionValidated, Status: metav1.ConditionTrue, Reason: "Validated", Message: "configuration validated against the live CRD schema",
	})

	// Step 4: resolve the assigned webhook server.
	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := r.List(ctx, &servers); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing ConversionWebhookServers: %w", err)
	}
	serverName, err := assign.ResolveAssignment(cfg, servers.Items)
	if err != nil {
		r.setInvalid(cfg, wasApplied, fmt.Sprintf("could not resolve a ConversionWebhookServer: %v", err))
		return ctrl.Result{}, r.patchStatus(ctx, orig, cfg)
	}
	cfg.Status.AssignedWebhookServer = serverName

	// Step 5: CRD health gate.
	if !crdadapter.Established(&crd) {
		msg := "target CustomResourceDefinition is not yet Established"
		meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
			Type: teraskyv1alpha1.ConditionCRDHealthy, Status: metav1.ConditionFalse, Reason: "NotEstablished", Message: msg,
		})
		setPhasePendingOrStale(&cfg.Status.Conditions, &cfg.Status.Phase, wasApplied, "NotEstablished", msg)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, r.patchStatus(ctx, orig, cfg)
	}
	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.ConditionCRDHealthy, Status: metav1.ConditionTrue, Reason: "Established", Message: "target CustomResourceDefinition is Established",
	})

	// Step 6: webhook server health gate.
	var server teraskyv1alpha1.ConversionWebhookServer
	if err := r.Get(ctx, types.NamespacedName{Name: serverName}, &server); err != nil {
		return ctrl.Result{}, fmt.Errorf("getting assigned ConversionWebhookServer %q: %w", serverName, err)
	}
	if !isServerReady(&server) {
		msg := fmt.Sprintf("ConversionWebhookServer %q is not yet Available", serverName)
		meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
			Type: teraskyv1alpha1.ConditionWebhookServerReady, Status: metav1.ConditionFalse, Reason: "ServerNotReady", Message: msg,
		})
		setPhasePendingOrStale(&cfg.Status.Conditions, &cfg.Status.Phase, wasApplied, "ServerNotReady", msg)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, r.patchStatus(ctx, orig, cfg)
	}
	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.ConditionWebhookServerReady, Status: metav1.ConditionTrue, Reason: "ServerReady", Message: fmt.Sprintf("ConversionWebhookServer %q is Available", serverName),
	})

	// Step 7: only now, patch the CRD.
	caBundle, err := r.readCABundle(ctx, &server)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading CA bundle for server %q: %w", serverName, err)
	}
	serverNamespace := server.Spec.Namespace
	if serverNamespace == "" {
		serverNamespace = r.DefaultServerNamespace
	}
	port := server.Spec.Service.Port
	if port == 0 {
		port = 443
	}
	path := "/convert/" + cfg.Spec.TargetCRD.Name
	reviewVersions := cfg.Spec.ConversionReviewVersions
	if len(reviewVersions) == 0 {
		reviewVersions = []string{"v1"}
	}

	if err := r.applyConversionPatch(ctx, cfg, serverNamespace, cwsServiceName(serverName), path, port, caBundle, reviewVersions); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching CRD conversion webhook config: %w", err)
	}

	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	cfg.Status.WebhookPath = path
	cfg.Status.WebhookURL = fmt.Sprintf("https://%s.%s.svc%s", cwsServiceName(serverName), serverNamespace, path)
	cfg.Status.LastAppliedPlanHash = cfg.Status.SchemaHash
	cfg.Status.Message = "conversion webhook configuration applied to the target CRD"
	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.ConditionApplied, Status: metav1.ConditionTrue, Reason: "Applied", Message: "CRD spec.conversion has been patched",
	})
	meta.RemoveStatusCondition(&cfg.Status.Conditions, teraskyv1alpha1.ConditionStale)

	if err := r.patchStatus(ctx, orig, cfg); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *CRDConversionConfigReconciler) setInvalid(cfg *teraskyv1alpha1.CRDConversionConfig, wasApplied bool, msg string) {
	if wasApplied {
		setPhaseStale(&cfg.Status.Conditions, &cfg.Status.Phase, "PostApplyInvalid", msg)
	} else {
		cfg.Status.Phase = teraskyv1alpha1.PhaseInvalid
		meta.RemoveStatusCondition(&cfg.Status.Conditions, teraskyv1alpha1.ConditionStale)
	}
	cfg.Status.Message = msg
	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.ConditionValidated, Status: metav1.ConditionFalse, Reason: "Invalid", Message: msg,
	})
}

func populateCRDSpokeStatuses(cfg *teraskyv1alpha1.CRDConversionConfig, report engine.AnalyzeReport) {
	statuses := make([]teraskyv1alpha1.SpokeConversionStatus, 0, len(report.SpokeReports))
	for _, sr := range report.SpokeReports {
		s := teraskyv1alpha1.SpokeConversionStatus{
			Version:              sr.Version,
			Lossless:             teraskyv1alpha1.LosslessVerdict{HubToSpoke: sr.Lossless.HubToSpoke, SpokeToHub: sr.Lossless.SpokeToHub},
			FieldsUncoveredHub:   sr.Uncovered.UncoveredHub,
			FieldsUncoveredSpoke: sr.Uncovered.UncoveredSpoke,
		}
		for _, d := range sr.Errors {
			s.Errors = append(s.Errors, d.Message)
		}
		for _, d := range sr.Warnings {
			s.Warnings = append(s.Warnings, d.Message)
		}
		for _, rr := range sr.RuleResults {
			s.RuleResults = append(s.RuleResults, teraskyv1alpha1.RuleResult{
				Index: rr.Index, Strategy: teraskyv1alpha1.Strategy(rr.Strategy),
				HubPaths: rr.HubPaths, SpokePaths: rr.SpokePaths,
				Lossless: teraskyv1alpha1.LosslessVerdict{HubToSpoke: rr.Lossless.HubToSpoke, SpokeToHub: rr.Lossless.SpokeToHub},
				Errors:   rr.Errors, Warnings: rr.Warnings,
			})
		}
		statuses = append(statuses, s)
	}
	cfg.Status.SpokeStatuses = statuses
}

func computeCRDSchemaHash(cfg *teraskyv1alpha1.CRDConversionConfig, report engine.AnalyzeReport) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "gen=%d;hub=%s;", report.ResourceGeneration, cfg.Spec.HubVersion)
	_, _ = fmt.Fprintf(h, "cfgGen=%d", cfg.Generation)
	return "sha256:" + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// readCABundle mirrors XRDConversionConfigReconciler.readCABundle exactly
// (unexported, so duplicated rather than shared — see that method's doc
// comment for why cert-manager's CA injector can't be used here either:
// CustomResourceDefinition isn't a supported injection target any more
// than CompositeResourceDefinition is).
func (r *CRDConversionConfigReconciler) readCABundle(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer) (string, error) {
	ns := server.Spec.Namespace
	if ns == "" {
		ns = r.DefaultServerNamespace
	}
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: cwsCertificateSecretName(server.Name), Namespace: ns}, &secret); err != nil {
		return "", fmt.Errorf("getting certificate secret: %w", err)
	}
	if ca, ok := secret.Data["ca.crt"]; ok && len(ca) > 0 {
		return base64.StdEncoding.EncodeToString(ca), nil
	}
	if crt, ok := secret.Data["tls.crt"]; ok && len(crt) > 0 {
		return base64.StdEncoding.EncodeToString(crt), nil
	}
	return "", fmt.Errorf("certificate secret %q has neither ca.crt nor tls.crt", secret.Name)
}

// applyConversionPatch server-side-applies just spec.conversion onto the
// target CRD, using a typed apply configuration (CustomResourceDefinition
// is a vendored core Kubernetes type, unlike the Crossplane XRD this
// mirrors) with a field manager scoped to exactly that field.
func (r *CRDConversionConfigReconciler) applyConversionPatch(ctx context.Context, cfg *teraskyv1alpha1.CRDConversionConfig, serviceNamespace, serviceName, path string, port int32, caBundle string, reviewVersions []string) error {
	caBundleBytes, err := base64.StdEncoding.DecodeString(caBundle)
	if err != nil {
		return fmt.Errorf("decoding CA bundle: %w", err)
	}
	patch := applyextv1.CustomResourceDefinition(cfg.Spec.TargetCRD.Name).
		WithAnnotations(map[string]string{
			"conversion.terasky.com/managed-by": cfg.Name,
			"conversion.terasky.com/plan-hash":  cfg.Status.SchemaHash,
		}).
		WithSpec(applyextv1.CustomResourceDefinitionSpec().
			WithConversion(applyextv1.CustomResourceConversion().
				WithStrategy(extv1.WebhookConverter).
				WithWebhook(applyextv1.WebhookConversion().
					WithClientConfig(applyextv1.WebhookClientConfig().
						WithService(applyextv1.ServiceReference().
							WithName(serviceName).WithNamespace(serviceNamespace).WithPath(path).WithPort(port)).
						WithCABundle(caBundleBytes...)).
					WithConversionReviewVersions(reviewVersions...))))
	return r.Apply(ctx, patch, client.ForceOwnership, client.FieldOwner(FieldOwner))
}

// revertCRD resets spec.conversion to strategy=None, relinquishing this
// operator's ownership of the field.
func (r *CRDConversionConfigReconciler) revertCRD(ctx context.Context, crdName string) error {
	patch := applyextv1.CustomResourceDefinition(crdName).
		WithSpec(applyextv1.CustomResourceDefinitionSpec().
			WithConversion(applyextv1.CustomResourceConversion().WithStrategy(extv1.NoneConverter)))
	return r.Apply(ctx, patch, client.ForceOwnership, client.FieldOwner(FieldOwner))
}

// reconcileDelete implements the same safe-revert flow as
// XRDConversionConfigReconciler.reconcileDelete.
func (r *CRDConversionConfigReconciler) reconcileDelete(ctx context.Context, cfg *teraskyv1alpha1.CRDConversionConfig) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cfg, teraskyv1alpha1.CRDConversionConfigFinalizer) {
		return ctrl.Result{}, nil
	}

	if cfg.Status.Phase != teraskyv1alpha1.PhaseApplied && cfg.Status.Phase != teraskyv1alpha1.PhaseStale {
		return ctrl.Result{}, r.removeFinalizer(ctx, cfg)
	}

	var crd extv1.CustomResourceDefinition
	err := r.Get(ctx, types.NamespacedName{Name: cfg.Spec.TargetCRD.Name}, &crd)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, r.removeFinalizer(ctx, cfg)
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting target CRD during delete: %w", err)
	}

	servedCount := 0
	for _, v := range crd.Spec.Versions {
		if v.Served {
			servedCount++
		}
	}
	allowUnsafe := cfg.Annotations[teraskyv1alpha1.AllowUnsafeDeleteAnnotation] == "true"

	if servedCount > 1 && !allowUnsafe {
		orig := cfg.DeepCopy()
		meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
			Type: teraskyv1alpha1.ConditionDeletionBlocked, Status: metav1.ConditionTrue, Reason: "MultipleServedVersions",
			Message: fmt.Sprintf("target CRD %q still serves %d versions; reverting conversion would risk serving the wrong shape to clients on a non-storage version. Add annotation %q=\"true\" to force.", cfg.Spec.TargetCRD.Name, servedCount, teraskyv1alpha1.AllowUnsafeDeleteAnnotation),
		})
		if err := r.patchStatus(ctx, orig, cfg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if err := r.revertCRD(ctx, cfg.Spec.TargetCRD.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("reverting CRD conversion config: %w", err)
	}
	return ctrl.Result{}, r.removeFinalizer(ctx, cfg)
}

func (r *CRDConversionConfigReconciler) removeFinalizer(ctx context.Context, cfg *teraskyv1alpha1.CRDConversionConfig) error {
	controllerutil.RemoveFinalizer(cfg, teraskyv1alpha1.CRDConversionConfigFinalizer)
	return r.Update(ctx, cfg)
}

func (r *CRDConversionConfigReconciler) patchStatus(ctx context.Context, orig, cfg *teraskyv1alpha1.CRDConversionConfig) error {
	cfg.Status.ObservedGeneration = cfg.Generation
	return r.Status().Patch(ctx, cfg, client.MergeFrom(orig))
}

// SetupWithManager wires up watches: the primary CRDConversionConfig
// resource, plus the target CustomResourceDefinition and
// ConversionWebhookServer — mirroring
// XRDConversionConfigReconciler.SetupWithManager exactly, except the
// watched target-resource type is a real Kubernetes core type
// (CustomResourceDefinition always exists on any cluster, so unlike the
// XRD controller this watch is never at risk of failing manager startup).
func (r *CRDConversionConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &teraskyv1alpha1.CRDConversionConfig{}, TargetCRDNameIndex, func(obj client.Object) []string {
		cfg := obj.(*teraskyv1alpha1.CRDConversionConfig)
		if cfg.Spec.TargetCRD.Name == "" {
			return nil
		}
		return []string{cfg.Spec.TargetCRD.Name}
	}); err != nil {
		return fmt.Errorf("indexing %s: %w", TargetCRDNameIndex, err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&teraskyv1alpha1.CRDConversionConfig{}).
		Watches(&extv1.CustomResourceDefinition{}, handler.EnqueueRequestsFromMapFunc(r.mapCRDToConfigs)).
		Watches(&teraskyv1alpha1.ConversionWebhookServer{}, handler.EnqueueRequestsFromMapFunc(r.mapAnyServerToAllConfigs)).
		Named("crdconversionconfig").
		Complete(r)
}

func (r *CRDConversionConfigReconciler) mapCRDToConfigs(ctx context.Context, obj client.Object) []reconcile.Request {
	var list teraskyv1alpha1.CRDConversionConfigList
	if err := r.List(ctx, &list, client.MatchingFields{TargetCRDNameIndex: obj.GetName()}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, c := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: c.Name}})
	}
	return reqs
}

func (r *CRDConversionConfigReconciler) mapAnyServerToAllConfigs(ctx context.Context, _ client.Object) []reconcile.Request {
	var list teraskyv1alpha1.CRDConversionConfigList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, c := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: c.Name}})
	}
	return reqs
}
