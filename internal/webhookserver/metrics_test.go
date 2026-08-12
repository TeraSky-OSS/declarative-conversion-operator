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

	"github.com/prometheus/client_golang/prometheus/testutil"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

func TestSyncRegistryMetrics_ReflectsLoadedAndErrorEntries(t *testing.T) {
	reg := NewRegistry()
	metrics := NewMetrics(newTestRegisterer())

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
	metrics := NewMetrics(newTestRegisterer())
	r := &Reconciler{
		Client: c, ServerName: "srv", Registry: NewRegistry(),
		EnableXRDSupport: true, Metrics: metrics,
	}

	if err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := testutil.ToFloat64(metrics.RegistrySize); got != 1 {
		t.Fatalf("expected registry_size=1 after compile, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.RegistryEntryLoaded.WithLabelValues("xfoos.example.org")); got != 1 {
		t.Fatalf("expected entry_loaded=1 after compile, got %v", got)
	}
}
