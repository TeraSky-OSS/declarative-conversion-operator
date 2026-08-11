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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	teraskyv1alpha1 "github.com/vrabbi/declarative-conversion-operator/api/v1alpha1"
	"github.com/vrabbi/declarative-conversion-operator/internal/assign"
	"github.com/vrabbi/declarative-conversion-operator/pkg/engine"
	"github.com/vrabbi/declarative-conversion-operator/pkg/xrdadapter"
)

// TargetXRDNameIndex mirrors internal/controller's index of the same name.
// It's redefined here (not imported) because this is a genuinely separate
// process/binary with its own cache — duplicating one small index avoids
// a coupling between the manager and webhook-server binaries that neither
// needs.
const TargetXRDNameIndex = "spec.targetXRD.name"

// Reconciler keeps a Registry in sync with live XRDConversionConfig,
// ConversionWebhookServer, and XRD objects, filtered to only the configs
// assigned to ServerName. Registry entries are keyed by XRD name (matching
// the /convert/{xrd-name} HTTP path), while reconcile requests are keyed by
// XRDConversionConfig name — configToXRD tracks the mapping so a deleted
// config can be removed from the registry by the right key even after the
// object itself is gone.
//
// A single bad config never crash-loops or de-readies the whole pod:
// business-logic failures (unresolvable assignment, analysis errors) are
// recorded into the Registry and reported via Reconcile returning a nil
// error, since retrying immediately can't fix a bad config — only a future
// watch event (the config or its XRD changing) will. Transient
// infrastructure errors (a failed API call) are returned as real errors so
// controller-runtime's normal backoff-and-retry applies.
type Reconciler struct {
	client.Client
	ServerName string
	Registry   *Registry
	Metrics    *Metrics

	mu          sync.Mutex
	configToXRD map[string]string
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	if err := r.reconcileOne(ctx, req.Name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileOne is the shared core used both by the watch-driven Reconcile
// loop and by InitialSync's synchronous startup pass. It returns an error
// only for transient infrastructure failures that are worth an automatic
// retry; business-logic failures are recorded into the Registry instead.
func (r *Reconciler) reconcileOne(ctx context.Context, name string) error {
	r.mu.Lock()
	if r.configToXRD == nil {
		r.configToXRD = map[string]string{}
	}
	r.mu.Unlock()

	var cfg teraskyv1alpha1.XRDConversionConfig
	err := r.Get(ctx, types.NamespacedName{Name: name}, &cfg)
	if apierrors.IsNotFound(err) {
		r.forgetConfig(name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting XRDConversionConfig %q: %w", name, err)
	}
	if !cfg.DeletionTimestamp.IsZero() {
		r.forgetConfig(name)
		return nil
	}
	r.rememberConfig(name, cfg.Spec.TargetXRD.Name)

	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := r.List(ctx, &servers); err != nil {
		return fmt.Errorf("listing ConversionWebhookServers: %w", err)
	}
	assigned, err := assign.ResolveAssignment(&cfg, servers.Items)
	if err != nil || assigned != r.ServerName {
		// Not (or no longer) ours to serve.
		r.Registry.Remove(cfg.Spec.TargetXRD.Name)
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

	plans := map[string]*engine.Plan{}
	lossless := map[string]engine.LosslessVerdict{}
	for _, sr := range report.SpokeReports {
		plans[sr.Version] = sr.CompiledPlan
		lossless[sr.Version] = sr.Lossless
	}
	reviewVersions := cfg.Spec.ConversionReviewVersions
	if len(reviewVersions) == 0 {
		reviewVersions = []string{"v1"}
	}

	entry := &CompiledEntry{
		Router:                   &engine.Router{Hub: cfg.Spec.HubVersion, Plans: plans},
		ConversionReviewVersions: reviewVersions,
		Lossless:                 lossless,
		PlanHash:                 fmt.Sprintf("gen=%d/%d", xrd.GetGeneration(), cfg.Generation),
		CompiledAt:               time.Now(),
	}
	r.Registry.Set(cfg.Spec.TargetXRD.Name, entry)
	if r.Metrics != nil {
		r.Metrics.RegistryReloadTotal.WithLabelValues(cfg.Spec.TargetXRD.Name, "success").Inc()
		r.Metrics.RegistryLastReload.WithLabelValues(cfg.Spec.TargetXRD.Name).Set(float64(time.Now().Unix()))
	}
	return nil
}

func (r *Reconciler) rememberConfig(configName, xrdName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configToXRD[configName] = xrdName
}

func (r *Reconciler) forgetConfig(configName string) {
	r.mu.Lock()
	xrdName, ok := r.configToXRD[configName]
	delete(r.configToXRD, configName)
	r.mu.Unlock()
	if ok {
		r.Registry.Remove(xrdName)
	}
}

func (r *Reconciler) recordFailure(xrdName, reason, msg string) {
	r.Registry.RecordError(xrdName, msg)
	if r.Metrics != nil {
		r.Metrics.RegistryReloadTotal.WithLabelValues(xrdName, "error").Inc()
		r.Metrics.RegistryCompileErr.WithLabelValues(xrdName, reason).Inc()
	}
}

// InitialSync runs reconcileOne synchronously for every currently-existing
// XRDConversionConfig, so the caller can gate readiness on "cache synced
// AND every config has been through at least one reconcile attempt" rather
// than cache-sync alone — closing the classic gap where a pod is added to
// a Service's endpoints before its registry reflects reality.
func (r *Reconciler) InitialSync(ctx context.Context) error {
	var list teraskyv1alpha1.XRDConversionConfigList
	if err := r.List(ctx, &list); err != nil {
		return fmt.Errorf("listing XRDConversionConfigs for initial sync: %w", err)
	}
	for _, cfg := range list.Items {
		if err := r.reconcileOne(ctx, cfg.Name); err != nil {
			// Best-effort: a transient failure for one config during
			// startup shouldn't block the pod from ever becoming ready;
			// the watch-driven reconciler will retry it.
			continue
		}
	}
	return nil
}

// SetupWithManager wires up watches, indexed identically to
// internal/controller's XRDConversionConfig controller.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
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
		Named("webhookserver-registry").
		Complete(r)
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

func (r *Reconciler) mapAnyServerToAllConfigs(ctx context.Context, _ client.Object) []reconcile.Request {
	var list teraskyv1alpha1.XRDConversionConfigList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, c := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: c.Name}})
	}
	return reqs
}
