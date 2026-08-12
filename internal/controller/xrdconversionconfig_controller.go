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
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// TargetXRDNameIndex is the field index used to map an XRD name back to the
// XRDConversionConfig(s) that reference it — used both by watch mapping and
// by the admission webhook's one-config-per-XRD uniqueness check.
const TargetXRDNameIndex = "spec.targetXRD.name"

// XRDConversionConfigReconciler reconciles an XRDConversionConfig.
type XRDConversionConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// DefaultServerNamespace is where a ConversionWebhookServer's child
	// resources live when spec.namespace is unset — normally the
	// operator's own install namespace.
	DefaultServerNamespace string
}

// +kubebuilder:rbac:groups=terasky.com,resources=xrdconversionconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=terasky.com,resources=xrdconversionconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=terasky.com,resources=xrdconversionconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=terasky.com,resources=conversionwebhookservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=apiextensions.crossplane.io,resources=compositeresourcedefinitions,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *XRDConversionConfigReconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var cfg teraskyv1alpha1.XRDConversionConfig
	if err := r.Get(ctx, req.NamespacedName, &cfg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !cfg.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &cfg)
	}

	if !controllerutil.ContainsFinalizer(&cfg, teraskyv1alpha1.XRDConversionConfigFinalizer) {
		controllerutil.AddFinalizer(&cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
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

func (r *XRDConversionConfigReconciler) reconcileNormal(ctx context.Context, cfg *teraskyv1alpha1.XRDConversionConfig) (ctrl.Result, error) {
	// PhaseStale means a prior apply still stands (KeepServingStale /
	// health-gate hold). Treat it like Applied so a later reconcile does
	// not demote persistent drift to Invalid or drop ConditionStale.
	wasApplied := cfg.Status.Phase == teraskyv1alpha1.PhaseApplied ||
		cfg.Status.Phase == teraskyv1alpha1.PhaseStale
	orig := cfg.DeepCopy()

	// Step 1: fetch the target XRD.
	xrd := &unstructured.Unstructured{}
	xrd.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(ctx, types.NamespacedName{Name: cfg.Spec.TargetXRD.Name}, xrd); err != nil {
		if apierrors.IsNotFound(err) {
			r.setInvalid(cfg, wasApplied, fmt.Sprintf("target XRD %q not found", cfg.Spec.TargetXRD.Name))
			return ctrl.Result{}, r.patchStatus(ctx, orig, cfg)
		}
		return ctrl.Result{}, fmt.Errorf("getting target XRD: %w", err)
	}
	cfg.Status.ObservedXRDGeneration = xrd.GetGeneration()

	// Step 2+3: analyze against the live schema.
	source := xrdadapter.New(xrd)
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

	populateSpokeStatuses(cfg, report)
	cfg.Status.OverallLossless = report.OverallLossless()
	cfg.Status.SchemaHash = computeSchemaHash(cfg, report)

	if report.HasErrors() {
		msg := "one or more spoke versions failed validation; see status.spokeStatuses for details"
		var revertErr error
		if failClosedShouldRevert(cfg.Spec.DriftPolicy, cfg.Status.Phase, cfg.Status.Conditions) {
			if err := r.revertXRD(ctx, cfg.Spec.TargetXRD.Name); err != nil {
				// Do not claim a successful revert: the XRD may still be
				// serving webhook conversion. Keep Phase=Failed with an
				// honest message, surface RevertFailed on ConditionApplied,
				// and return the error so reconcile requeues with backoff.
				revertErr = err
				logger := log.FromContext(ctx)
				logger.Error(err, "failed to revert XRD after drift under FailClosed policy")
				cfg.Status.Phase = teraskyv1alpha1.PhaseFailed
				meta.RemoveStatusCondition(&cfg.Status.Conditions, teraskyv1alpha1.ConditionStale)
				msg = fmt.Sprintf("schema drift invalidated a previously-applied config; failed to revert to strategy=None per driftPolicy=FailClosed: %v", err)
				meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
					Type: teraskyv1alpha1.ConditionApplied, Status: metav1.ConditionFalse, Reason: "RevertFailed", Message: msg,
				})
			} else {
				cfg.Status.Phase = teraskyv1alpha1.PhaseFailed
				meta.RemoveStatusCondition(&cfg.Status.Conditions, teraskyv1alpha1.ConditionStale)
				msg = "schema drift invalidated a previously-applied config; reverted to strategy=None per driftPolicy=FailClosed"
				meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
					Type: teraskyv1alpha1.ConditionApplied, Status: metav1.ConditionFalse, Reason: "Reverted", Message: msg,
				})
			}
		} else if wasApplied {
			// Conservative default: keep serving the last known-good
			// compiled plan (unchanged on the XRD) and mark Stale rather
			// than risk an outage by touching a working conversion setup.
			msg = "schema drift invalidated this config, but the previously-applied webhook configuration is left untouched (driftPolicy=KeepServingStale); fix the config or the XRD to clear this"
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
			return ctrl.Result{}, fmt.Errorf("reverting XRD after FailClosed drift: %w", revertErr)
		}
		return ctrl.Result{}, nil
	}

	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.ConditionValidated, Status: metav1.ConditionTrue, Reason: "Validated", Message: "configuration validated against the live XRD schema",
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

	// Step 5: XRD health gate.
	if !xrdadapter.Established(xrd) {
		msg := "target XRD is not yet Established"
		meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
			Type: teraskyv1alpha1.ConditionXRDHealthy, Status: metav1.ConditionFalse, Reason: "NotEstablished", Message: msg,
		})
		setPhasePendingOrStale(&cfg.Status.Conditions, &cfg.Status.Phase, wasApplied, "NotEstablished", msg)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, r.patchStatus(ctx, orig, cfg)
	}
	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.ConditionXRDHealthy, Status: metav1.ConditionTrue, Reason: "Established", Message: "target XRD is Established",
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

	// Step 7: only now, patch the XRD.
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
	path := "/convert/" + cfg.Spec.TargetXRD.Name
	reviewVersions := cfg.Spec.ConversionReviewVersions
	if len(reviewVersions) == 0 {
		reviewVersions = []string{"v1"}
	}

	if err := r.applyConversionPatch(ctx, cfg, serverNamespace, cwsServiceName(serverName), path, port, caBundle, reviewVersions); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching XRD conversion webhook config: %w", err)
	}

	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	cfg.Status.WebhookPath = path
	cfg.Status.WebhookURL = fmt.Sprintf("https://%s.%s.svc%s", cwsServiceName(serverName), serverNamespace, path)
	cfg.Status.LastAppliedPlanHash = cfg.Status.SchemaHash
	cfg.Status.Message = "conversion webhook configuration applied to the target XRD"
	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.ConditionApplied, Status: metav1.ConditionTrue, Reason: "Applied", Message: "XRD spec.conversion has been patched",
	})
	meta.RemoveStatusCondition(&cfg.Status.Conditions, teraskyv1alpha1.ConditionStale)

	if err := r.patchStatus(ctx, orig, cfg); err != nil {
		return ctrl.Result{}, err
	}
	// Periodic self-check safety net: re-validate against the live XRD
	// even if no watched object changes in the meantime.
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// phasePending returns the phase to report while waiting on a gate
// (XRD/server health) for a config that hasn't previously been applied.
// Once applied, drift/wait situations report Stale instead, since the XRD
// still carries a working (if now-unverified) conversion configuration.
func phasePending(wasApplied bool) string {
	if wasApplied {
		return teraskyv1alpha1.PhaseStale
	}
	return teraskyv1alpha1.PhasePending
}

// failClosedShouldRevert reports whether FailClosed drift handling must
// (re)attempt tearing down the live webhook conversion config. True while
// Phase is still Applied, and also while a previous FailClosed revert failed
// (Phase=Failed with ConditionApplied Reason=RevertFailed) so we keep
// retrying instead of treating status as a successful tear-down.
func failClosedShouldRevert(driftPolicy teraskyv1alpha1.DriftPolicy, phase string, conditions []metav1.Condition) bool {
	if driftPolicy != teraskyv1alpha1.DriftPolicyFailClosed {
		return false
	}
	if phase == teraskyv1alpha1.PhaseApplied {
		return true
	}
	if phase == teraskyv1alpha1.PhaseFailed {
		cond := meta.FindStatusCondition(conditions, teraskyv1alpha1.ConditionApplied)
		return cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "RevertFailed"
	}
	return false
}

// setPhaseStale sets Phase=Stale and ConditionStale=True so monitors that
// watch status.conditions see the same signal as status.phase.
func setPhaseStale(conditions *[]metav1.Condition, phase *string, reason, message string) {
	*phase = teraskyv1alpha1.PhaseStale
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:    teraskyv1alpha1.ConditionStale,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: message,
	})
}

// setPhasePendingOrStale sets Pending for never-applied configs, or Stale
// (phase + condition) when a previously-applied config is waiting on a gate.
func setPhasePendingOrStale(conditions *[]metav1.Condition, phase *string, wasApplied bool, reason, message string) {
	if wasApplied {
		setPhaseStale(conditions, phase, reason, message)
		return
	}
	*phase = teraskyv1alpha1.PhasePending
	meta.RemoveStatusCondition(conditions, teraskyv1alpha1.ConditionStale)
}

func (r *XRDConversionConfigReconciler) setInvalid(cfg *teraskyv1alpha1.XRDConversionConfig, wasApplied bool, msg string) {
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

func populateSpokeStatuses(cfg *teraskyv1alpha1.XRDConversionConfig, report engine.AnalyzeReport) {
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

func computeSchemaHash(cfg *teraskyv1alpha1.XRDConversionConfig, report engine.AnalyzeReport) string {
	h := sha256.New()
	// hash.Hash.Write never returns an error (guaranteed by the interface
	// contract), so the Fprintf return values are safe to discard.
	_, _ = fmt.Fprintf(h, "gen=%d;hub=%s;", report.ResourceGeneration, cfg.Spec.HubVersion)
	// The rule content itself is already part of the reconciled object's
	// generation on cfg, so folding cfg.Generation in captures rule
	// changes too, alongside the XRD's own schema generation.
	_, _ = fmt.Fprintf(h, "cfgGen=%d", cfg.Generation)
	return "sha256:" + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func isServerReady(server *teraskyv1alpha1.ConversionWebhookServer) bool {
	if server.Status.ReadyReplicas == 0 {
		return false
	}
	for _, want := range []string{teraskyv1alpha1.CWSConditionAvailable, teraskyv1alpha1.CWSConditionCertificateReady, teraskyv1alpha1.CWSConditionServiceReady} {
		if !meta.IsStatusConditionTrue(server.Status.Conditions, want) {
			return false
		}
	}
	return true
}

// readCABundle reads the CA bundle out of the Secret cert-manager writes
// for the assigned server's Certificate. cert-manager's CA injector does
// not support CompositeResourceDefinition as an injection target, so this
// operator reads and embeds it explicitly instead.
func (r *XRDConversionConfigReconciler) readCABundle(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer) (string, error) {
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

// applyConversionPatch server-side-applies just spec.conversion (plus a
// couple of tracking annotations) onto the target XRD, using a field
// manager scoped to exactly those fields so this operator never fights any
// other owner of the XRD's spec.
func (r *XRDConversionConfigReconciler) applyConversionPatch(ctx context.Context, cfg *teraskyv1alpha1.XRDConversionConfig, serviceNamespace, serviceName, path string, port int32, caBundle string, reviewVersions []string) error {
	patch := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": xrdadapter.GroupVersionKind.GroupVersion().String(),
		"kind":       xrdadapter.GroupVersionKind.Kind,
		"metadata": map[string]any{
			"name": cfg.Spec.TargetXRD.Name,
			"annotations": map[string]any{
				"conversion.terasky.com/managed-by": cfg.Name,
				"conversion.terasky.com/plan-hash":  cfg.Status.SchemaHash,
			},
		},
		"spec": map[string]any{
			"conversion": map[string]any{
				"strategy": "Webhook",
				"webhook": map[string]any{
					"clientConfig": map[string]any{
						"service": map[string]any{
							"name":      serviceName,
							"namespace": serviceNamespace,
							"path":      path,
							"port":      int64(port),
						},
						"caBundle": caBundle,
					},
					"conversionReviewVersions": toAnySlice(reviewVersions),
				},
			},
		},
	}}
	return r.Apply(ctx, client.ApplyConfigurationFromUnstructured(patch), client.ForceOwnership, client.FieldOwner(FieldOwner))
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// revertXRD resets spec.conversion to strategy=None, relinquishing this
// operator's ownership of the field.
func (r *XRDConversionConfigReconciler) revertXRD(ctx context.Context, xrdName string) error {
	patch := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": xrdadapter.GroupVersionKind.GroupVersion().String(),
		"kind":       xrdadapter.GroupVersionKind.Kind,
		"metadata":   map[string]any{"name": xrdName},
		"spec": map[string]any{
			"conversion": map[string]any{"strategy": "None"},
		},
	}}
	return r.Apply(ctx, client.ApplyConfigurationFromUnstructured(patch), client.ForceOwnership, client.FieldOwner(FieldOwner))
}

// reconcileDelete implements the safe-revert flow: never remove the
// finalizer (and thus never let the CR disappear) while reverting the
// XRD's conversion webhook config would risk serving CRs in the wrong
// shape to a client requesting a still-served, non-storage version.
func (r *XRDConversionConfigReconciler) reconcileDelete(ctx context.Context, cfg *teraskyv1alpha1.XRDConversionConfig) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer) {
		return ctrl.Result{}, nil
	}

	if cfg.Status.Phase != teraskyv1alpha1.PhaseApplied && cfg.Status.Phase != teraskyv1alpha1.PhaseStale {
		return ctrl.Result{}, r.removeFinalizer(ctx, cfg)
	}

	xrd := &unstructured.Unstructured{}
	xrd.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	err := r.Get(ctx, types.NamespacedName{Name: cfg.Spec.TargetXRD.Name}, xrd)
	if apierrors.IsNotFound(err) {
		// Nothing to revert.
		return ctrl.Result{}, r.removeFinalizer(ctx, cfg)
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting target XRD during delete: %w", err)
	}

	servedCount := countServedVersions(xrd)
	// Checked live at the moment of this reconcile, not a historically-set
	// flag — the annotation must be present on the object being deleted
	// right now for the break-glass override to apply.
	allowUnsafe := cfg.Annotations[teraskyv1alpha1.AllowUnsafeDeleteAnnotation] == "true"

	if servedCount > 1 && !allowUnsafe {
		orig := cfg.DeepCopy()
		meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
			Type: teraskyv1alpha1.ConditionDeletionBlocked, Status: metav1.ConditionTrue, Reason: "MultipleServedVersions",
			Message: fmt.Sprintf("target XRD %q still serves %d versions; reverting conversion would risk serving the wrong shape to clients on a non-storage version. Add annotation %q=\"true\" to force.", cfg.Spec.TargetXRD.Name, servedCount, teraskyv1alpha1.AllowUnsafeDeleteAnnotation),
		})
		if err := r.patchStatus(ctx, orig, cfg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if err := r.revertXRD(ctx, cfg.Spec.TargetXRD.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("reverting XRD conversion config: %w", err)
	}
	return ctrl.Result{}, r.removeFinalizer(ctx, cfg)
}

func countServedVersions(xrd *unstructured.Unstructured) int {
	versions, found, err := unstructured.NestedSlice(xrd.Object, "spec", "versions")
	if err != nil || !found {
		return 0
	}
	count := 0
	for _, v := range versions {
		vm, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if served, _, _ := unstructured.NestedBool(vm, "served"); served {
			count++
		}
	}
	return count
}

func (r *XRDConversionConfigReconciler) removeFinalizer(ctx context.Context, cfg *teraskyv1alpha1.XRDConversionConfig) error {
	controllerutil.RemoveFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	return r.Update(ctx, cfg)
}

func (r *XRDConversionConfigReconciler) patchStatus(ctx context.Context, orig, cfg *teraskyv1alpha1.XRDConversionConfig) error {
	cfg.Status.ObservedGeneration = cfg.Generation
	return r.Status().Patch(ctx, cfg, client.MergeFrom(orig))
}

// SetupWithManager wires up watches: the primary XRDConversionConfig
// resource, plus the target XRD (via a field index mapping XRD name back
// to referencing configs) and ConversionWebhookServer (any change
// re-evaluates every config, since server readiness/assignment affects all
// of them and this is a low-QPS, admin-frequency object).
func (r *XRDConversionConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &teraskyv1alpha1.XRDConversionConfig{}, TargetXRDNameIndex, func(obj client.Object) []string {
		cfg := obj.(*teraskyv1alpha1.XRDConversionConfig)
		if cfg.Spec.TargetXRD.Name == "" {
			return nil
		}
		return []string{cfg.Spec.TargetXRD.Name}
	}); err != nil {
		return fmt.Errorf("indexing %s: %w", TargetXRDNameIndex, err)
	}

	xrdObj := &unstructured.Unstructured{}
	xrdObj.SetGroupVersionKind(xrdadapter.GroupVersionKind)

	return ctrl.NewControllerManagedBy(mgr).
		For(&teraskyv1alpha1.XRDConversionConfig{}).
		Watches(xrdObj, handler.EnqueueRequestsFromMapFunc(r.mapXRDToConfigs)).
		Watches(&teraskyv1alpha1.ConversionWebhookServer{}, handler.EnqueueRequestsFromMapFunc(r.mapAnyServerToAllConfigs)).
		Named("xrdconversionconfig").
		Complete(r)
}

func (r *XRDConversionConfigReconciler) mapXRDToConfigs(ctx context.Context, obj client.Object) []reconcile.Request {
	var list teraskyv1alpha1.XRDConversionConfigList
	if err := r.List(ctx, &list, client.MatchingFields{TargetXRDNameIndex: obj.GetName()}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, c := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: c.Name}})
	}
	return reqs
}

func (r *XRDConversionConfigReconciler) mapAnyServerToAllConfigs(ctx context.Context, _ client.Object) []reconcile.Request {
	var list teraskyv1alpha1.XRDConversionConfigList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, c := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: c.Name}})
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].Name < reqs[j].Name })
	return reqs
}
