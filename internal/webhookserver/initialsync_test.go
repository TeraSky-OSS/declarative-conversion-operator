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
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// coldStartFleet builds n XRD targets and n matching configs, plus the one
// default ConversionWebhookServer they all resolve to. One of them names a
// target that does not exist, so the fixture covers the error path as well
// as the happy one — a parallel walk that lost a recorded failure would
// look identical to a serial one that never had it otherwise.
func coldStartFleet(n int) []runtime.Object {
	server := &teraskyv1alpha1.ConversionWebhookServer{}
	server.Name = "srv"
	server.Spec.Default = true

	objs := []runtime.Object{server}
	for i := 0; i < n; i++ {
		target := fmt.Sprintf("xfoos%d.example.org", i)
		objs = append(objs, renameRuleXRDConfig(fmt.Sprintf("cfg-%d", i), target))
		if i%10 == 3 {
			continue // no XRD for this one: recordFailure path.
		}
		objs = append(objs, establishedXRD(target))
	}
	return objs
}

// registryFingerprint reduces a registry to the facts a caller can observe:
// which targets are present, whether each has a servable plan, and what
// error (if any) is recorded against it.
func registryFingerprint(r *Registry) []string {
	snap := r.Snapshot()
	out := make([]string, 0, len(snap))
	for name, entry := range snap {
		servable := entry != nil && entry.Router != nil
		lastErr := ""
		if entry != nil {
			lastErr = entry.LastError
		}
		out = append(out, fmt.Sprintf("%s servable=%t err=%q", name, servable, lastErr))
	}
	sort.Strings(out)
	return out
}

func TestInitialSync_ParallelMatchesSerial(t *testing.T) {
	const targets = 40
	objs := coldStartFleet(targets)

	serial := &Reconciler{
		Client: newFakeClient(objs...).Build(), ServerName: "srv",
		Registry: NewRegistry(), EnableXRDSupport: true, InitialSyncWorkers: 1,
	}
	parallel := &Reconciler{
		Client: newFakeClient(objs...).Build(), ServerName: "srv",
		Registry: NewRegistry(), EnableXRDSupport: true, InitialSyncWorkers: 8,
	}

	serialStats, err := serial.InitialSync(context.Background())
	if err != nil {
		t.Fatalf("serial sync: %v", err)
	}
	parallelStats, err := parallel.InitialSync(context.Background())
	if err != nil {
		t.Fatalf("parallel sync: %v", err)
	}

	if serialStats.Targets != targets || parallelStats.Targets != targets {
		t.Fatalf("expected both walks to report %d targets, got serial=%d parallel=%d",
			targets, serialStats.Targets, parallelStats.Targets)
	}

	want, got := registryFingerprint(serial.Registry), registryFingerprint(parallel.Registry)
	if len(want) != len(got) {
		t.Fatalf("registry size differs: serial=%d parallel=%d", len(want), len(got))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("registry entry %d differs:\n serial: %s\n parallel: %s", i, want[i], got[i])
		}
	}
	// Guard against the fixture quietly becoming all-happy-path: if no
	// entry ever records a failure, the comparison above proves less than
	// it looks like it does.
	failures := 0
	for _, line := range want {
		if !strings.HasSuffix(line, `err=""`) {
			failures++
		}
	}
	if failures == 0 {
		t.Fatal("expected the fleet fixture to include at least one failing target")
	}
}

func TestInitialSync_PublishesColdStartMetrics(t *testing.T) {
	objs := coldStartFleet(5)
	m := newTestMetrics()
	r := &Reconciler{
		Client: newFakeClient(objs...).Build(), ServerName: "srv",
		Registry: NewRegistry(), Metrics: m, EnableXRDSupport: true,
	}

	stats, err := r.InitialSync(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := testutil.ToFloat64(m.InitialSyncTargets); got != 5 {
		t.Fatalf("dco_webhook_initial_sync_targets = %v, want 5", got)
	}
	if got := testutil.ToFloat64(m.InitialSyncDuration); got <= 0 {
		t.Fatalf("dco_webhook_initial_sync_duration_seconds = %v, want a positive elapsed time", got)
	}
	if stats.Duration <= 0 {
		t.Fatalf("stats.Duration = %v, want a positive elapsed time", stats.Duration)
	}
	// The per-target gauge refresh is suppressed during the walk; the one
	// sync at the end is what has to leave the gauges correct.
	if got := testutil.ToFloat64(m.RegistrySize); int(got) != r.Registry.Len() {
		t.Fatalf("dco_webhook_registry_size = %v after the bulk pass, want %d", got, r.Registry.Len())
	}
}

// An infrastructure failure during the startup pass has to come back as an
// error, because readiness is gated on this returning cleanly. A bad
// config does not: reconcileOne* records that into the registry and
// returns nil, since retrying cannot fix it.
func TestInitialSync_ReportsInfrastructureErrors(t *testing.T) {
	objs := coldStartFleet(6)
	failing := newFakeClient(objs...).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if u, ok := obj.(*unstructured.Unstructured); ok && u.GroupVersionKind() == xrdadapter.GroupVersionKind && key.Name == "xfoos1.example.org" {
				return errors.New("the apiserver said no")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()

	r := &Reconciler{Client: failing, ServerName: "srv", Registry: NewRegistry(), EnableXRDSupport: true}
	_, err := r.InitialSync(context.Background())
	if err == nil {
		t.Fatal("expected a failed target read to be reported; readiness is gated on this returning cleanly")
	}
	if !strings.Contains(err.Error(), "the apiserver said no") {
		t.Fatalf("the error should carry the cause: %v", err)
	}

	// And the other targets still loaded: one bad read must not abandon
	// the pass.
	if got := r.Registry.Len(); got < 4 {
		t.Fatalf("registry holds %d entries; the other targets should still have compiled", got)
	}
}

// A config whose target does not exist is a configuration problem, not an
// infrastructure one. It is recorded against the registry and must not
// keep the replica from reporting ready — otherwise one broken config
// would hold the whole replica out of service.
func TestInitialSync_BadConfigIsNotAnError(t *testing.T) {
	// coldStartFleet deliberately leaves one config with no XRD.
	r := &Reconciler{
		Client: newFakeClient(coldStartFleet(12)...).Build(), ServerName: "srv",
		Registry: NewRegistry(), EnableXRDSupport: true,
	}
	if _, err := r.InitialSync(context.Background()); err != nil {
		t.Fatalf("a config with a missing target must not fail the sync: %v", err)
	}
}
