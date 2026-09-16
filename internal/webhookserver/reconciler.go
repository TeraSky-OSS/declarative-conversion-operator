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
	"runtime"
	"sync"
	"sync/atomic"
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
	"github.com/terasky-oss/declarative-conversion-operator/internal/watchmap"
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
	// Publisher, when set, is told after every registry change so this
	// replica's served-target Lease keeps up with what it can actually
	// serve. Nil in tests and on a replica with no downward-API
	// identity; a nil Publisher's Notify is a no-op.
	Publisher *TargetPublisher
	// InitialSyncWorkers bounds the parallelism of InitialSync's
	// startup pass. Zero means DefaultInitialSyncWorkers(). It has no
	// effect on the watch-driven path, whose concurrency is
	// controller-runtime's to decide.
	InitialSyncWorkers int
	// TargetDrainPeriod is how long this replica keeps serving a target
	// after the target has stopped naming it. Zero means
	// DefaultTargetDrainPeriod; see handover.go for why it is not zero.
	TargetDrainPeriod time.Duration
	// now is time.Now, overridden in tests that need to advance the drain
	// clock without sleeping through it.
	now func() time.Time

	// bulkSync suppresses the per-target registry gauge refresh while a
	// bulk pass (InitialSync) is running; see syncRegistryMetrics.
	bulkSync atomic.Bool

	mu             sync.Mutex
	configToTarget map[string]string
	// drainUntil holds, per config key, the moment this replica may stop
	// serving a target it no longer owns. See handover.go.
	drainUntil map[string]time.Time
}

// reconcileXRD/reconcileCRD are the two watch-driven entry points,
// registered as separate controllers in SetupWithManager (native
// controller-runtime has no notion of "one Reconciler, two watched
// types" — each controller needs its own entry point).
func (r *Reconciler) reconcileXRD(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	requeue, err := r.reconcileOneXRD(ctx, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

func (r *Reconciler) reconcileCRD(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	requeue, err := r.reconcileOneCRD(ctx, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

func (r *Reconciler) ensureConfigToTarget() {
	r.mu.Lock()
	if r.configToTarget == nil {
		r.configToTarget = map[string]string{}
	}
	if r.drainUntil == nil {
		r.drainUntil = map[string]time.Time{}
	}
	r.mu.Unlock()
}

// timeNow is time.Now unless a test has overridden it.
func (r *Reconciler) timeNow() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// drainRemaining decides whether this replica may drop its plan for a
// target that no longer names it, and how long to wait if not.
//
// See handover.go: the apiserver refreshes a CRD's conversion
// configuration asynchronously after the write that changed it, so for a
// short window after the operator repoints a target the apiserver is still
// calling this replica. Dropping the plan the instant the object changes
// answers those calls with a 503, which the apiserver reports as a failed
// read or write on a resource that was merely being rebalanced.
//
// Returns 0 when the drain has elapsed (or there is nothing to drain), and
// the remaining time otherwise, which the caller turns into a requeue.
func (r *Reconciler) drainRemaining(key, targetName string) time.Duration {
	period := r.TargetDrainPeriod
	if period == 0 {
		period = DefaultTargetDrainPeriod
	}
	if period < 0 {
		return 0
	}
	// Nothing to protect: this replica has no plan for the target, so
	// there is no window in which it could answer wrongly. Requeueing for
	// thirty seconds to remove something that is not there would be pure
	// churn — and on a replica that serves none of a large fleet, it
	// would be that churn once per config.
	if _, held := r.Registry.Get(targetName); !held {
		r.cancelDrain(key)
		return 0
	}
	now := r.timeNow()

	r.mu.Lock()
	defer r.mu.Unlock()
	deadline, started := r.drainUntil[key]
	if !started {
		r.drainUntil[key] = now.Add(period)
		return period
	}
	if remaining := deadline.Sub(now); remaining > 0 {
		return remaining
	}
	delete(r.drainUntil, key)
	return 0
}

// cancelDrain forgets a pending removal, called whenever the target turns
// out to still be this replica's after all — a rebalance that reverted, or
// a move that was abandoned.
func (r *Reconciler) cancelDrain(key string) {
	r.mu.Lock()
	delete(r.drainUntil, key)
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
func (r *Reconciler) reconcileOneXRD(ctx context.Context, name string) (time.Duration, error) {
	r.ensureConfigToTarget()
	key := configKey("xrd", name)

	var cfg teraskyv1alpha1.XRDConversionConfig
	err := r.Get(ctx, types.NamespacedName{Name: name}, &cfg)
	if apierrors.IsNotFound(err) {
		r.forgetConfig(key)
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("getting XRDConversionConfig %q: %w", name, err)
	}
	if !cfg.DeletionTimestamp.IsZero() {
		r.forgetConfig(key)
		return 0, nil
	}
	r.rememberConfig(key, cfg.Spec.TargetXRD.Name)

	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := r.List(ctx, &servers); err != nil {
		return 0, fmt.Errorf("listing ConversionWebhookServers: %w", err)
	}
	assigned, assignErr := assign.ResolveAssignment(&cfg, servers.Items)
	// An unresolvable assignment is a bad config, not a transient failure:
	// retrying cannot fix it, and this replica does not serve the target
	// on that basis either way.
	mine := assignErr == nil && assigned == r.ServerName

	xrd := &unstructured.Unstructured{}
	xrd.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(ctx, types.NamespacedName{Name: cfg.Spec.TargetXRD.Name}, xrd); err != nil {
		if apierrors.IsNotFound(err) {
			if !mine {
				// A deleted target cannot be pointing at anybody, so
				// there is nothing for a drain to protect.
				r.dropTarget(key, cfg.Spec.TargetXRD.Name)
				return 0, nil
			}
			r.recordFailure(cfg.Spec.TargetXRD.Name, "XRDNotFound", fmt.Sprintf("target XRD %q not found", cfg.Spec.TargetXRD.Name))
			return 0, nil
		}
		return 0, fmt.Errorf("getting target XRD %q: %w", cfg.Spec.TargetXRD.Name, err)
	}

	// Not ours by assignment, and the XRD no longer points its conversion
	// webhook here either: the handover is over, bar the drain. See
	// handover.go for why both clauses exist and why the drain does.
	if !mine && !xrdPointsAtServer(xrd, r.ServerName) {
		if wait := r.drainRemaining(key, cfg.Spec.TargetXRD.Name); wait > 0 {
			return wait, nil
		}
		r.dropTarget(key, cfg.Spec.TargetXRD.Name)
		return 0, nil
	}
	// Still ours, so any drain in progress was for a move that did not
	// happen, or reverted.
	r.cancelDrain(key)

	ruleSets, err := cfg.ToRuleSets()
	if err != nil {
		r.recordFailure(cfg.Spec.TargetXRD.Name, "InvalidRules", fmt.Sprintf("invalid rule configuration: %v", err))
		return 0, nil
	}
	report, err := engine.Analyze(engine.AnalyzeInput{Source: xrdadapter.New(xrd), HubVersion: cfg.Spec.HubVersion, Spokes: ruleSets})
	if err != nil {
		r.recordFailure(cfg.Spec.TargetXRD.Name, "AnalyzeFailed", fmt.Sprintf("analysis failed: %v", err))
		return 0, nil
	}
	if report.HasErrors() {
		r.recordFailure(cfg.Spec.TargetXRD.Name, "ValidationErrors", "analysis produced validation errors; keeping any previously compiled plan in place")
		return 0, nil
	}

	r.compileAndRegister(cfg.Spec.TargetXRD.Name, cfg.Spec.HubVersion, cfg.Spec.ConversionReviewVersions, report, fmt.Sprintf("gen=%d/%d", xrd.GetGeneration(), cfg.Generation))
	return 0, nil
}

// reconcileOneCRD is reconcileOneXRD's counterpart for CRDConversionConfig.
func (r *Reconciler) reconcileOneCRD(ctx context.Context, name string) (time.Duration, error) {
	r.ensureConfigToTarget()
	key := configKey("crd", name)

	var cfg teraskyv1alpha1.CRDConversionConfig
	err := r.Get(ctx, types.NamespacedName{Name: name}, &cfg)
	if apierrors.IsNotFound(err) {
		r.forgetConfig(key)
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("getting CRDConversionConfig %q: %w", name, err)
	}
	if !cfg.DeletionTimestamp.IsZero() {
		r.forgetConfig(key)
		return 0, nil
	}
	r.rememberConfig(key, cfg.Spec.TargetCRD.Name)

	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := r.List(ctx, &servers); err != nil {
		return 0, fmt.Errorf("listing ConversionWebhookServers: %w", err)
	}
	assigned, assignErr := assign.ResolveAssignment(&cfg, servers.Items)
	mine := assignErr == nil && assigned == r.ServerName

	var crd extv1.CustomResourceDefinition
	if err := r.Get(ctx, types.NamespacedName{Name: cfg.Spec.TargetCRD.Name}, &crd); err != nil {
		if apierrors.IsNotFound(err) {
			if !mine {
				r.dropTarget(key, cfg.Spec.TargetCRD.Name)
				return 0, nil
			}
			r.recordFailure(cfg.Spec.TargetCRD.Name, "CRDNotFound", fmt.Sprintf("target CRD %q not found", cfg.Spec.TargetCRD.Name))
			return 0, nil
		}
		return 0, fmt.Errorf("getting target CRD %q: %w", cfg.Spec.TargetCRD.Name, err)
	}

	// See the XRD path, and handover.go.
	if !mine && !crdPointsAtServer(&crd, r.ServerName) {
		if wait := r.drainRemaining(key, cfg.Spec.TargetCRD.Name); wait > 0 {
			return wait, nil
		}
		r.dropTarget(key, cfg.Spec.TargetCRD.Name)
		return 0, nil
	}
	r.cancelDrain(key)

	ruleSets, err := cfg.ToRuleSets()
	if err != nil {
		r.recordFailure(cfg.Spec.TargetCRD.Name, "InvalidRules", fmt.Sprintf("invalid rule configuration: %v", err))
		return 0, nil
	}
	report, err := engine.Analyze(engine.AnalyzeInput{Source: crdadapter.New(&crd), HubVersion: cfg.Spec.HubVersion, Spokes: ruleSets})
	if err != nil {
		r.recordFailure(cfg.Spec.TargetCRD.Name, "AnalyzeFailed", fmt.Sprintf("analysis failed: %v", err))
		return 0, nil
	}
	if report.HasErrors() {
		r.recordFailure(cfg.Spec.TargetCRD.Name, "ValidationErrors", "analysis produced validation errors; keeping any previously compiled plan in place")
		return 0, nil
	}

	r.compileAndRegister(cfg.Spec.TargetCRD.Name, cfg.Spec.HubVersion, cfg.Spec.ConversionReviewVersions, report, fmt.Sprintf("gen=%d/%d", crd.Generation, cfg.Generation))
	return 0, nil
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
	}
	r.registryChanged()
}

func (r *Reconciler) rememberConfig(key, targetName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configToTarget[key] = targetName
}

// dropTarget removes a target this replica no longer serves and clears
// any drain bookkeeping for it.
func (r *Reconciler) dropTarget(key, targetName string) {
	r.cancelDrain(key)
	r.Registry.Remove(targetName)
	r.registryChanged()
}

func (r *Reconciler) forgetConfig(key string) {
	r.mu.Lock()
	targetName, ok := r.configToTarget[key]
	delete(r.configToTarget, key)
	// A config deleted mid-drain would otherwise leave its deadline
	// behind forever — one map entry per config that ever churned.
	delete(r.drainUntil, key)
	r.mu.Unlock()
	if ok {
		r.Registry.Remove(targetName)
		r.registryChanged()
	}
}

// registryChanged republishes everything derived from the registry: the
// per-target gauges, and this replica's served-target Lease. Suppressed
// while a bulk pass (InitialSync) is running.
//
// SyncRegistryMetrics rebuilds every series from a full snapshot, so it is
// O(targets) per call. On the watch-driven path that is one call per
// change and unnoticeable. During InitialSync it would be one call per
// target — quadratic in the target count, on the cold start this phase
// exists to shorten, and every intermediate state it publishes is
// immediately superseded anyway. InitialSync therefore suppresses it and
// syncs once at the end, which is the only state a scrape can observe: the
// replica is not in the Service's endpoints until it reports ready. The
// same reasoning applies with more force to the Lease, which is an API
// write.
func (r *Reconciler) registryChanged() {
	if r.bulkSync.Load() {
		return
	}
	if r.Metrics != nil {
		r.Metrics.SyncRegistryMetrics(r.Registry)
	}
	r.Publisher.Notify()
}

func (r *Reconciler) recordFailure(targetName, reason, msg string) {
	r.Registry.RecordError(targetName, msg)
	if r.Metrics != nil {
		r.Metrics.RegistryReloadTotal.WithLabelValues(targetName, "error").Inc()
		r.Metrics.RegistryCompileErr.WithLabelValues(targetName, reason).Inc()
	}
	r.registryChanged()
}

// InitialSyncStats is what one InitialSync did: how many configs it walked
// and how long that took. Returned rather than logged from inside so the
// caller owns the log line and the metric — cmd/webhook-server is the only
// place that knows whether this replica is about to become ready.
type InitialSyncStats struct {
	Targets  int
	Duration time.Duration
}

// DefaultInitialSyncWorkers is the bound on InitialSync's worker pool when
// InitialSyncWorkers is left at zero. Compilation is CPU-bound and
// independent per target, so the useful parallelism is the number of cores
// the container is actually allowed to use — GOMAXPROCS, which respects a
// CPU limit when the runtime is configured for it. The pool is bounded
// rather than unbounded because every worker holds a decoded schema and a
// half-built plan; an unbounded fan-out across a thousand targets would
// turn a CPU problem into a memory one at exactly the moment the replica
// has the least headroom.
func DefaultInitialSyncWorkers() int {
	if n := runtime.GOMAXPROCS(0); n > 0 {
		return n
	}
	return 1
}

// InitialSync runs reconcileOneXRD/reconcileOneCRD for every
// currently-existing config of whichever kinds are enabled, so the caller
// can gate readiness on "cache synced AND every config has been through at
// least one reconcile attempt" rather than cache-sync alone — closing the
// classic gap where a pod is added to a Service's endpoints before its
// registry reflects reality.
//
// The walk is parallel across a bounded pool (see InitialSyncWorkers),
// because it is the whole of a replica's cold start: with hundreds of
// targets, compiling them one at a time is the difference between a pod
// that is ready in a second and one a startupProbe has to be told to wait
// for. Parallelism is safe by construction — each call reads one config
// and its target from the shared informer cache and writes one entry into
// the copy-on-write Registry under its own lock, and the two calls never
// share intermediate state. It is also *observationally* identical to the
// serial walk: distinct configs write distinct registry keys, so no
// ordering between them is visible in the result.
func (r *Reconciler) InitialSync(ctx context.Context) (InitialSyncStats, error) {
	start := time.Now()
	var stats InitialSyncStats

	// Both lists are read before any compiling starts, so a slow compile
	// cannot make the target count a moving target.
	var work []func(context.Context)
	if r.EnableXRDSupport {
		var list teraskyv1alpha1.XRDConversionConfigList
		if err := r.List(ctx, &list); err != nil {
			return stats, fmt.Errorf("listing XRDConversionConfigs for initial sync: %w", err)
		}
		for _, cfg := range list.Items {
			work = append(work, func(ctx context.Context) {
				_, _ = r.reconcileOneXRD(ctx, cfg.Name) // best-effort; the watch-driven reconciler retries transient failures.
			})
		}
	}
	if r.EnableCRDSupport {
		var list teraskyv1alpha1.CRDConversionConfigList
		if err := r.List(ctx, &list); err != nil {
			return stats, fmt.Errorf("listing CRDConversionConfigs for initial sync: %w", err)
		}
		for _, cfg := range list.Items {
			work = append(work, func(ctx context.Context) {
				_, _ = r.reconcileOneCRD(ctx, cfg.Name) // best-effort; the watch-driven reconciler retries transient failures.
			})
		}
	}

	stats.Targets = len(work)
	r.bulkSync.Store(true)
	r.runInitialSyncWork(ctx, work)
	r.bulkSync.Store(false)
	r.registryChanged()

	stats.Duration = time.Since(start)
	if r.Metrics != nil {
		r.Metrics.InitialSyncTargets.Set(float64(stats.Targets))
		r.Metrics.InitialSyncDuration.Set(stats.Duration.Seconds())
	}
	return stats, nil
}

// runInitialSyncWork drains work across at most InitialSyncWorkers
// goroutines. It deliberately does not stop early on a cancelled context:
// each unit is already best-effort and bounded, and a partially-populated
// registry that then reports ready is precisely the failure mode
// InitialSync exists to prevent. Cancellation reaches the individual API
// reads through ctx, which is what actually makes a cancelled sync fast.
func (r *Reconciler) runInitialSyncWork(ctx context.Context, work []func(context.Context)) {
	if len(work) == 0 {
		return
	}
	workers := r.InitialSyncWorkers
	if workers <= 0 {
		workers = DefaultInitialSyncWorkers()
	}
	if workers > len(work) {
		workers = len(work)
	}
	if workers <= 1 {
		for _, fn := range work {
			fn(ctx)
		}
		return
	}

	next := make(chan func(context.Context))
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for fn := range next {
				fn(ctx)
			}
		}()
	}
	for _, fn := range work {
		next <- fn
	}
	close(next)
	wg.Wait()
}

// SetupWithManager wires up one controller per enabled config kind, each
// indexed and watched identically to internal/controller's corresponding
// XRDConversionConfig/CRDConversionConfig controller.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.EnableXRDSupport {
		if err := mgr.GetFieldIndexer().IndexField(context.Background(), &teraskyv1alpha1.XRDConversionConfig{}, TargetXRDNameIndex, func(obj client.Object) []string {
			// Checked rather than asserted: an index function panicking
			// takes the whole process down, and this one runs on every
			// object the informer sees.
			cfg, ok := obj.(*teraskyv1alpha1.XRDConversionConfig)
			if !ok || cfg.Spec.TargetXRD.Name == "" {
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
			Watches(&teraskyv1alpha1.ConversionWebhookServer{}, enqueue.PacedMapFuncs(r.mapServerToAssignedXRDConfigs, r.mapServerTransitionToAssignedXRDConfigs, enqueue.CWSConfigEnqueueQPS)).
			Named("webhookserver-registry-xrd").
			Complete(reconcile.Func(r.reconcileXRD)); err != nil {
			return fmt.Errorf("setting up XRD registry controller: %w", err)
		}
	}

	if r.EnableCRDSupport {
		if err := mgr.GetFieldIndexer().IndexField(context.Background(), &teraskyv1alpha1.CRDConversionConfig{}, TargetCRDNameIndex, func(obj client.Object) []string {
			cfg, ok := obj.(*teraskyv1alpha1.CRDConversionConfig)
			if !ok || cfg.Spec.TargetCRD.Name == "" {
				return nil
			}
			return []string{cfg.Spec.TargetCRD.Name}
		}); err != nil {
			return fmt.Errorf("indexing %s: %w", TargetCRDNameIndex, err)
		}

		if err := ctrl.NewControllerManagedBy(mgr).
			For(&teraskyv1alpha1.CRDConversionConfig{}).
			Watches(&extv1.CustomResourceDefinition{}, handler.EnqueueRequestsFromMapFunc(r.mapCRDToConfigs)).
			Watches(&teraskyv1alpha1.ConversionWebhookServer{}, enqueue.PacedMapFuncs(r.mapServerToAssignedCRDConfigs, r.mapServerTransitionToAssignedCRDConfigs, enqueue.CWSConfigEnqueueQPS)).
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
		return watchmap.ListError(ctx, "webhookserver.mapXRDToConfigs", err)
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
		return watchmap.ListError(ctx, "webhookserver.mapCRDToConfigs", err)
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, c := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: c.Name}})
	}
	return reqs
}

func (r *Reconciler) mapServerToAssignedXRDConfigs(ctx context.Context, obj client.Object) []reconcile.Request {
	reqs, err := mapAssignedXRD(ctx, r.Client, obj.GetName(), asCWS(obj))
	if err != nil {
		return watchmap.ListError(ctx, "webhookserver.mapServerToAssignedXRDConfigs", err)
	}
	return reqs
}

func (r *Reconciler) mapServerTransitionToAssignedXRDConfigs(ctx context.Context, oldObj, newObj client.Object) []reconcile.Request {
	name := objectName(oldObj, newObj)
	reqs, err := mapAssignedXRD(ctx, r.Client, name, asCWS(oldObj), asCWS(newObj))
	if err != nil {
		return watchmap.ListError(ctx, "webhookserver.mapServerTransitionToAssignedXRDConfigs", err)
	}
	return reqs
}

func (r *Reconciler) mapServerToAssignedCRDConfigs(ctx context.Context, obj client.Object) []reconcile.Request {
	reqs, err := mapAssignedCRD(ctx, r.Client, obj.GetName(), asCWS(obj))
	if err != nil {
		return watchmap.ListError(ctx, "webhookserver.mapServerToAssignedCRDConfigs", err)
	}
	return reqs
}

func (r *Reconciler) mapServerTransitionToAssignedCRDConfigs(ctx context.Context, oldObj, newObj client.Object) []reconcile.Request {
	name := objectName(oldObj, newObj)
	reqs, err := mapAssignedCRD(ctx, r.Client, name, asCWS(oldObj), asCWS(newObj))
	if err != nil {
		return watchmap.ListError(ctx, "webhookserver.mapServerTransitionToAssignedCRDConfigs", err)
	}
	return reqs
}

func objectName(oldObj, newObj client.Object) string {
	if newObj != nil {
		return newObj.GetName()
	}
	if oldObj != nil {
		return oldObj.GetName()
	}
	return ""
}

func asCWS(obj client.Object) *teraskyv1alpha1.ConversionWebhookServer {
	if obj == nil {
		return nil
	}
	s, ok := obj.(*teraskyv1alpha1.ConversionWebhookServer)
	if !ok {
		return nil
	}
	return s
}

func mapAssignedXRD(ctx context.Context, c client.Client, serverName string, views ...*teraskyv1alpha1.ConversionWebhookServer) ([]reconcile.Request, error) {
	var list teraskyv1alpha1.XRDConversionConfigList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := c.List(ctx, &servers); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var reqs []reconcile.Request
	for _, view := range views {
		if view == nil {
			continue
		}
		for _, cfg := range assign.ConfigsAssignedTo(list.Items, serversWithView(servers.Items, view), serverName) {
			if _, ok := seen[cfg.Name]; ok {
				continue
			}
			seen[cfg.Name] = struct{}{}
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name}})
		}
	}
	return reqs, nil
}

func mapAssignedCRD(ctx context.Context, c client.Client, serverName string, views ...*teraskyv1alpha1.ConversionWebhookServer) ([]reconcile.Request, error) {
	var list teraskyv1alpha1.CRDConversionConfigList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := c.List(ctx, &servers); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var reqs []reconcile.Request
	for _, view := range views {
		if view == nil {
			continue
		}
		for _, cfg := range assign.ConfigsAssignedTo(list.Items, serversWithView(servers.Items, view), serverName) {
			if _, ok := seen[cfg.Name]; ok {
				continue
			}
			seen[cfg.Name] = struct{}{}
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name}})
		}
	}
	return reqs, nil
}

func serversWithView(live []teraskyv1alpha1.ConversionWebhookServer, view *teraskyv1alpha1.ConversionWebhookServer) []teraskyv1alpha1.ConversionWebhookServer {
	out := make([]teraskyv1alpha1.ConversionWebhookServer, 0, len(live)+1)
	found := false
	for _, s := range live {
		if s.Name == view.Name {
			out = append(out, *view)
			found = true
			continue
		}
		cp := s
		if view.Spec.Default {
			cp.Spec.Default = false
		}
		out = append(out, cp)
	}
	if !found {
		out = append(out, *view)
	}
	return out
}
