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
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/client-go/util/workqueue"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

func TestSyncRegistryMetrics_ReflectsLoadedAndErrorEntries(t *testing.T) {
	reg := NewRegistry()
	metrics := newTestMetrics()

	reg.Set("ready.example.org", &CompiledEntry{
		Router: &engine.Router{Hub: "v2", Plans: map[string]*engine.Plan{"v1": {}}},
	})
	reg.RecordError("broken.example.org", "compile failed")
	metrics.SyncRegistryMetrics(reg)

	if got := testutil.ToFloat64(metrics.RegistrySize); got != 2 {
		t.Fatalf("expected registry_size=2, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.RegistryEntryLoaded.WithLabelValues("ready.example.org")); got != 1 {
		t.Fatalf("expected loaded=1 for ready target, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.RegistryEntryLoaded.WithLabelValues("broken.example.org")); got != 0 {
		t.Fatalf("expected loaded=0 for error-only target, got %v", got)
	}

	reg.Remove("ready.example.org")
	metrics.SyncRegistryMetrics(reg)
	if got := testutil.ToFloat64(metrics.RegistrySize); got != 1 {
		t.Fatalf("expected registry_size=1 after remove, got %v", got)
	}
}

func TestReconcileOneXRD_UpdatesRegistryReadinessMetrics(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	server := &teraskyv1alpha1.ConversionWebhookServer{}
	server.Name = "srv"
	server.Spec.Default = true

	c := newFakeClient(xrd, cfg, server).Build()
	metrics := newTestMetrics()
	r := &Reconciler{
		Client: c, ServerName: "srv", Registry: NewRegistry(),
		EnableXRDSupport: true, Metrics: metrics,
	}

	if _, err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := testutil.ToFloat64(metrics.RegistrySize); got != 1 {
		t.Fatalf("expected registry_size=1 after compile, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.RegistryEntryLoaded.WithLabelValues("xfoos.example.org")); got != 1 {
		t.Fatalf("expected entry_loaded=1 after compile, got %v", got)
	}
}

// The dedicated registry and controller-runtime's package-global one are
// served from the same handler. prometheus.Gatherers fails the entire
// scrape if the two ever register the same metric name, which would take
// out every dco_webhook_* series along with the controller ones — so the
// separation is asserted rather than assumed.
func TestCombinedGatherer_NoDuplicateSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	NewMetrics(reg, reg)

	// A metric family with no series is not gathered at all, and
	// controller-runtime's workqueue vectors have no series until a queue
	// exists. Creating one materialises them through the global metrics
	// provider controller-runtime installs — the same path a real
	// controller takes.
	q := workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.DefaultTypedControllerRateLimiter[string](),
		workqueue.TypedRateLimitingQueueConfig[string]{Name: "combined-gatherer-test"},
	)
	defer q.ShutDown()
	q.Add("x")

	families, err := CombinedGatherer(reg).Gather()
	if err != nil {
		t.Fatalf("gathering both registries: %v", err)
	}

	names := map[string]int{}
	for _, f := range families {
		names[f.GetName()]++
	}
	for name, n := range names {
		if n > 1 {
			t.Errorf("metric family %q appears %d times across the two registries", name, n)
		}
	}
	if _, ok := names["dco_webhook_registry_size"]; !ok {
		t.Error("the dedicated registry's own series are missing from the combined gather")
	}
	// controller-runtime registers these from an init(), so their absence
	// would mean the combined gatherer is not reaching that registry at
	// all — which is the whole point of it.
	if _, ok := names["workqueue_depth"]; !ok {
		t.Error("workqueue_depth is missing: the webhook-server's reconcile loop still has no queue-depth signal")
	}
	// cmd/webhook-server deliberately does not register its own Go and
	// process collectors, because controller-runtime's registry already
	// has them and a second copy would be the duplicate checked above.
	// That makes their presence here load-bearing rather than incidental:
	// without them a replica's memory footprint is unmeasurable from
	// outside the pod.
	for _, name := range []string{"go_goroutines", "process_resident_memory_bytes"} {
		if _, ok := names[name]; !ok {
			t.Errorf("%s is missing: the process serving /metrics is invisible on it", name)
		}
	}
}
