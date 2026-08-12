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

package webhookserver

import (
	"context"
	"fmt"
	"sync"
	"time"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/assign"
	"github.com/terasky-oss/declarative-conversion-operator/internal/enqueue"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/crdadapter"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// TargetXRDNameIndex/TargetCRDNameIndex mirror internal/controller's
// indexes of the same name. They're redefined here (not imported) because
// this is a genuinely separate process/binary with its own cache —
// duplicating two small indexes avoids a coupling between the manager and
// webhook-server binaries that neither needs.
const (
	TargetXRDNameIndex = "spec.targetXRD.name"
	TargetCRDNameIndex = "spec.targetCRD.name"
)

// Reconciler keeps a Registry in sync with live XRDConversionConfig and/or
// CRDConversionConfig objects (whichever are enabled), each alongside
// their target resource and ConversionWebhookServer objects, filtered to
// only the configs assigned to ServerName. Registry entries are keyed by
// target resource name (matching the /convert/{name} HTTP path — see the
// package-level note below on the one collision case this doesn't guard
// against), while reconcile requests are keyed by config name;
// configToTarget tracks the mapping so a deleted config can be removed
// from the registry by the right key even after the object itself is
// gone.
//
// A single bad config never crash-loops or de-readies the whole pod:
// business-logic failures (unresolvable assignment, analysis errors) are
// recorded into the Registry and reported via Reconcile returning a nil
// error, since retrying immediately can't fix a bad config — only a future
// watch event (the config or its target resource changing) will. Transient
// infrastructure errors (a failed API call) are returned as real errors so
// controller-runtime's normal backoff-and-retry applies.
//
// Note: registry keys are the bare target-resource name, shared between
// the XRD and CRD paths. Cross-kind collisions (an XRDConversionConfig and
// a CRDConversionConfig targeting the same name) are rejected at admission
// by internal/webhook.validateRegistryKeyAvailable.
type Reconciler struct {
	client.Client
	ServerName string
	Registry   *Registry
	Metrics    *Metrics
	// EnableXRDSupport/EnableCRDSupport mirror cmd/webhook-server's flags
	// of the same name (in turn set from the operator's own
	// --enable-xrd-support/--enable-crd-support). When XRD support is
	// disabled, this reconciler never watches Crossplane's
	// CompositeResourceDefinition GVK at all — establishing that watch is
	// fatal at startup on a cluster without Crossplane installed.
	EnableXRDSupport bool
	EnableCRDSupport bool

	mu             sync.Mutex
	configToTarget map[string]string
}

// reconcileXRD/reconcileCRD are the two watch-driven entry points,
// registered as separate controllers in SetupWithManager (native
// controller-runtime has no notion of "one Reconciler, two watched
// types" — each controller needs its own entry point).
func (r *Reconciler) reconcileXRD(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	if err := r.reconcileOneXRD(ctx, req.Name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) reconcileCRD(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	if err := r.reconcileOneCRD(ctx, req.Name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) ensureConfigToTarget() {
	r.mu.Lock()
	if r.configToTarget == nil {
		r.configToTarget = map[string]string{}
	}
	r.mu.Unlock()
}

// configKey namespaces configToTarget entries by kind, so an
// XRDConversionConfig and a CRDConversionConfig that happen to share a
// name (they're separate CRDs; nothing prevents this) can't clobber each
// other's forget/remember bookkeeping.
func configKey(kind, name string) string { return kind + "/" + name }

// reconcileOneXRD is the shared core for the XRD path, used both by the
// watch-driven Reconcile loop and by InitialSync's synchronous startup
// pass. It returns an error only for transient infrastructure failures
// worth an automatic retry; business-logic failures are recorded into the
// Registry instead.
func (r *Reconciler) reconcileOneXRD(ctx context.Context, name string) error {
	r.ensureConfigToTarget()
	key := configKey("xrd", name)

	var cfg teraskyv1alpha1.XRDConversionConfig
	err := r.Get(ctx, types.NamespacedName{Name: name}, &cfg)
	if apierrors.IsNotFound(err) {
		r.forgetConfig(key)
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting XRDConversionConfig %q: %w", name, err)
	}
	if !cfg.DeletionTimestamp.IsZero() {
		r.forgetConfig(key)
		return nil
	}
	r.rememberConfig(key, cfg.Spec.TargetXRD.Name)

	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := r.List(ctx, &servers); err != nil {
		return fmt.Errorf("listing ConversionWebhookServers: %w", err)
	}
	assigned, err := assign.ResolveAssignment(&cfg, servers.Items)
	if err != nil || assigned != r.ServerName {
		r.Registry.Remove(cfg.Spec.TargetXRD.Name)
		if r.Metrics != nil {
			r.Metrics.SyncRegistryMetrics(r.Registry)
		}
		return nil
	}

	xrd := &unstructured.Unstructured{}
	xrd.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(ctx, types.NamespacedName{Name: cfg.Spec.TargetXRD.Name}, xrd); err != nil {
		if apierrors.IsNotFound(err) {
			r.recordFailure(cfg.Spec.TargetXRD.Name, "XRDNotFound", fmt.Sprintf("target XRD %q not found", cfg.Spec.TargetXRD.Name))
			return nil
		}
		return fmt.Errorf("getting target XRD %q: %w", cfg.Spec.TargetXRD.Name, err)
	}

	ruleSets, err := cfg.ToRuleSets()
	if err != nil {
		r.recordFailure(cfg.Spec.TargetXRD.Name, "InvalidRules", fmt.Sprintf("invalid rule configuration: %v", err))
		return nil
	}
	report, err := engine.Analyze(engine.AnalyzeInput{Source: xrdadapter.New(xrd), HubVersion: cfg.Spec.HubVersion, Spokes: ruleSets})
	if err != nil {
		r.recordFailure(cfg.Spec.TargetXRD.Name, "AnalyzeFailed", fmt.Sprintf("analysis failed: %v", err))
		return nil
	}
	if report.HasErrors() {
		r.recordFailure(cfg.Spec.TargetXRD.Name, "ValidationErrors", "analysis produced validation errors; keeping any previously compiled plan in place")
		return nil
	}

	r.compileAndRegister(cfg.Spec.TargetXRD.Name, cfg.Spec.HubVersion, cfg.Spec.ConversionReviewVersions, report, fmt.Sprintf("gen=%d/%d", xrd.GetGeneration(), cfg.Generation))
	return nil
}

// reconcileOneCRD is reconcileOneXRD's counterpart for CRDConversionConfig.
func (r *Reconciler) reconcileOneCRD(ctx context.Context, name string) error {
	r.ensureConfigToTarget()
	key := configKey("crd", name)

	var cfg teraskyv1alpha1.CRDConversionConfig
	err := r.Get(ctx, types.NamespacedName{Name: name}, &cfg)
	if apierrors.IsNotFound(err) {
		r.forgetConfig(key)
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting CRDConversionConfig %q: %w", name, err)
	}
	if !cfg.DeletionTimestamp.IsZero() {
		r.forgetConfig(key)
		return nil
	}
	r.rememberConfig(key, cfg.Spec.TargetCRD.Name)

	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := r.List(ctx, &servers); err != nil {
		return fmt.Errorf("listing ConversionWebhookServers: %w", err)
	}
	assigned, err := assign.ResolveAssignment(&cfg, servers.Items)
	if err != nil || assigned != r.ServerName {
		r.Registry.Remove(cfg.Spec.TargetCRD.Name)
		if r.Metrics != nil {
			r.Metrics.SyncRegistryMetrics(r.Registry)
		}
		return nil
	}

	var crd extv1.CustomResourceDefinition
	if err := r.Get(ctx, types.NamespacedName{Name: cfg.Spec.TargetCRD.Name}, &crd); err != nil {
		if apierrors.IsNotFound(err) {
			r.recordFailure(cfg.Spec.TargetCRD.Name, "CRDNotFound", fmt.Sprintf("target CRD %q not found", cfg.Spec.TargetCRD.Name))
			return nil
		}
		return fmt.Errorf("getting target CRD %q: %w", cfg.Spec.TargetCRD.Name, err)
	}

	ruleSets, err := cfg.ToRuleSets()
	if err != nil {
		r.recordFailure(cfg.Spec.TargetCRD.Name, "InvalidRules", fmt.Sprintf("invalid rule configuration: %v", err))
		return nil
	}
	report, err := engine.Analyze(engine.AnalyzeInput{Source: crdadapter.New(&crd), HubVersion: cfg.Spec.HubVersion, Spokes: ruleSets})
	if err != nil {
		r.recordFailure(cfg.Spec.TargetCRD.Name, "AnalyzeFailed", fmt.Sprintf("analysis failed: %v", err))
		return nil
	}
	if report.HasErrors() {
		r.recordFailure(cfg.Spec.TargetCRD.Name, "ValidationErrors", "analysis produced validation errors; keeping any previously compiled plan in place")
		return nil
	}

	r.compileAndRegister(cfg.Spec.TargetCRD.Name, cfg.Spec.HubVersion, cfg.Spec.ConversionReviewVersions, report, fmt.Sprintf("gen=%d/%d", crd.Generation, cfg.Generation))
	return nil
}

// compileAndRegister builds the CompiledEntry from an analysis report and
// installs it into the Registry — the shared tail of both
// reconcileOneXRD and reconcileOneCRD.
func (r *Reconciler) compileAndRegister(targetName, hubVersion string, reviewVersions []string, report engine.AnalyzeReport, planHash string) {
	plans := map[string]*engine.Plan{}
	lossless := map[string]engine.LosslessVerdict{}
	for _, sr := range report.SpokeReports {
		plans[sr.Version] = sr.CompiledPlan
		lossless[sr.Version] = sr.Lossless
	}
	if len(reviewVersions) == 0 {
		reviewVersions = []string{"v1"}
	}

	entry := &CompiledEntry{
		Router:                   &engine.Router{Hub: hubVersion, Plans: plans},
		ConversionReviewVersions: reviewVersions,
		Lossless:                 lossless,
		PlanHash:                 planHash,
		CompiledAt:               time.Now(),
	}
	r.Registry.Set(targetName, entry)
	if r.Metrics != nil {
		r.Metrics.RegistryReloadTotal.WithLabelValues(targetName, "success").Inc()
		r.Metrics.RegistryLastReload.WithLabelValues(targetName).Set(float64(time.Now().Unix()))
		r.Metrics.SyncRegistryMetrics(r.Registry)
	}
}

func (r *Reconciler) rememberConfig(key, targetName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configToTarget[key] = targetName
}

func (r *Reconciler) forgetConfig(key string) {
	r.mu.Lock()
	targetName, ok := r.configToTarget[key]
	delete(r.configToTarget, key)
	r.mu.Unlock()
	if ok {
		r.Registry.Remove(targetName)
		if r.Metrics != nil {
			r.Metrics.SyncRegistryMetrics(r.Registry)
		}
	}
}

func (r *Reconciler) recordFailure(targetName, reason, msg string) {
	r.Registry.RecordError(targetName, msg)
	if r.Metrics != nil {
		r.Metrics.RegistryReloadTotal.WithLabelValues(targetName, "error").Inc()
		r.Metrics.RegistryCompileErr.WithLabelValues(targetName, reason).Inc()
		r.Metrics.SyncRegistryMetrics(r.Registry)
	}
}

// InitialSync runs reconcileOneXRD/reconcileOneCRD synchronously for every
// currently-existing config of whichever kinds are enabled, so the caller
// can gate readiness on "cache synced AND every config has been through at
// least one reconcile attempt" rather than cache-sync alone — closing the
// classic gap where a pod is added to a Service's endpoints before its
// registry reflects reality.
func (r *Reconciler) InitialSync(ctx context.Context) error {
	if r.EnableXRDSupport {
		var list teraskyv1alpha1.XRDConversionConfigList
		if err := r.List(ctx, &list); err != nil {
			return fmt.Errorf("listing XRDConversionConfigs for initial sync: %w", err)
		}
		for _, cfg := range list.Items {
			_ = r.reconcileOneXRD(ctx, cfg.Name) // best-effort; the watch-driven reconciler retries transient failures.
		}
	}
	if r.EnableCRDSupport {
		var list teraskyv1alpha1.CRDConversionConfigList
		if err := r.List(ctx, &list); err != nil {
			return fmt.Errorf("listing CRDConversionConfigs for initial sync: %w", err)
		}
		for _, cfg := range list.Items {
			_ = r.reconcileOneCRD(ctx, cfg.Name) // best-effort; the watch-driven reconciler retries transient failures.
		}
	}
	return nil
}

// SetupWithManager wires up one controller per enabled config kind, each
// indexed and watched identically to internal/controller's corresponding
// XRDConversionConfig/CRDConversionConfig controller.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.EnableXRDSupport {
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

		if err := ctrl.NewControllerManagedBy(mgr).
			For(&teraskyv1alpha1.XRDConversionConfig{}).
			Watches(xrdObj, handler.EnqueueRequestsFromMapFunc(r.mapXRDToConfigs)).
			Watches(&teraskyv1alpha1.ConversionWebhookServer{}, enqueue.PacedMapFunc(r.mapServerToAssignedXRDConfigs, enqueue.CWSConfigEnqueueQPS)).
			Named("webhookserver-registry-xrd").
			Complete(reconcile.Func(r.reconcileXRD)); err != nil {
			return fmt.Errorf("setting up XRD registry controller: %w", err)
		}
	}

	if r.EnableCRDSupport {
		if err := mgr.GetFieldIndexer().IndexField(context.Background(), &teraskyv1alpha1.CRDConversionConfig{}, TargetCRDNameIndex, func(obj client.Object) []string {
			cfg := obj.(*teraskyv1alpha1.CRDConversionConfig)
			if cfg.Spec.TargetCRD.Name == "" {
				return nil
			}
			return []string{cfg.Spec.TargetCRD.Name}
		}); err != nil {
			return fmt.Errorf("indexing %s: %w", TargetCRDNameIndex, err)
		}

		if err := ctrl.NewControllerManagedBy(mgr).
			For(&teraskyv1alpha1.CRDConversionConfig{}).
			Watches(&extv1.CustomResourceDefinition{}, handler.EnqueueRequestsFromMapFunc(r.mapCRDToConfigs)).
			Watches(&teraskyv1alpha1.ConversionWebhookServer{}, enqueue.PacedMapFunc(r.mapServerToAssignedCRDConfigs, enqueue.CWSConfigEnqueueQPS)).
			Named("webhookserver-registry-crd").
			Complete(reconcile.Func(r.reconcileCRD)); err != nil {
			return fmt.Errorf("setting up CRD registry controller: %w", err)
		}
	}

	return nil
}

func (r *Reconciler) mapXRDToConfigs(ctx context.Context, obj client.Object) []reconcile.Request {
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

func (r *Reconciler) mapCRDToConfigs(ctx context.Context, obj client.Object) []reconcile.Request {
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

func (r *Reconciler) mapServerToAssignedXRDConfigs(ctx context.Context, obj client.Object) []reconcile.Request {
	var list teraskyv1alpha1.XRDConversionConfigList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := r.List(ctx, &servers); err != nil {
		return nil
	}
	assigned := assign.ConfigsAssignedTo(list.Items, servers.Items, obj.GetName())
	reqs := make([]reconcile.Request, 0, len(assigned))
	for _, cfg := range assigned {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name}})
	}
	return reqs
}

func (r *Reconciler) mapServerToAssignedCRDConfigs(ctx context.Context, obj client.Object) []reconcile.Request {
	var list teraskyv1alpha1.CRDConversionConfigList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := r.List(ctx, &servers); err != nil {
		return nil
	}
	assigned := assign.ConfigsAssignedTo(list.Items, servers.Items, obj.GetName())
	reqs := make([]reconcile.Request, 0, len(assigned))
	for _, cfg := range assigned {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name}})
	}
	return reqs
}
