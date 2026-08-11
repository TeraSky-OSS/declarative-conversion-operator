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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func TestReconcileOneXRD_HappyPath_Compiles(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	server := &teraskyv1alpha1.ConversionWebhookServer{}
	server.Name = "srv"
	server.Spec.Default = true

	c := newFakeClient(xrd, cfg, server).Build()
	r := &Reconciler{Client: c, ServerName: "srv", Registry: NewRegistry(), EnableXRDSupport: true}

	if err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := r.Registry.Get("xfoos.example.org")
	if !ok || entry.Router == nil {
		t.Fatalf("expected a compiled entry to be registered, got ok=%v entry=%+v", ok, entry)
	}
	if entry.Router.Hub != "v2" {
		t.Fatalf("unexpected hub version: %q", entry.Router.Hub)
	}
}

func TestReconcileOneXRD_ConfigNotFound_Forgets(t *testing.T) {
	c := newFakeClient().Build()
	r := &Reconciler{Client: c, ServerName: "srv", Registry: NewRegistry(), EnableXRDSupport: true}
	r.ensureConfigToTarget()
	r.configToTarget[configKey("xrd", "cfg")] = "xfoos.example.org"
	r.Registry.Set("xfoos.example.org", &CompiledEntry{})

	if err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := r.Registry.Get("xfoos.example.org"); ok {
		t.Fatalf("expected the registry entry to be removed once the config is gone")
	}
}

func TestReconcileOneXRD_DeletionTimestamp_Forgets(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	now := metav1.Now()
	cfg.DeletionTimestamp = &now
	cfg.Finalizers = []string{"keep-it-around-for-the-fake-client"}

	c := newFakeClient(xrd, cfg).Build()
	r := &Reconciler{Client: c, ServerName: "srv", Registry: NewRegistry(), EnableXRDSupport: true}
	r.ensureConfigToTarget()
	r.configToTarget[configKey("xrd", "cfg")] = "xfoos.example.org"
	r.Registry.Set("xfoos.example.org", &CompiledEntry{})

	if err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := r.Registry.Get("xfoos.example.org"); ok {
		t.Fatalf("expected the registry entry to be removed for a config being deleted")
	}
}

func TestReconcileOneXRD_NotAssignedToThisReplica_Removed(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	otherServer := &teraskyv1alpha1.ConversionWebhookServer{}
	otherServer.Name = "other-srv"
	otherServer.Spec.Default = true

	c := newFakeClient(xrd, cfg, otherServer).Build()
	registry := NewRegistry()
	registry.Set("xfoos.example.org", &CompiledEntry{Router: nil})
	r := &Reconciler{Client: c, ServerName: "this-srv", Registry: registry, EnableXRDSupport: true}

	if err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := r.Registry.Get("xfoos.example.org"); ok {
		t.Fatalf("expected the entry to be removed since the config resolves to a different replica")
	}
}

func TestReconcileOneXRD_TargetXRDMissing_RecordsFailure(t *testing.T) {
	cfg := renameRuleXRDConfig("cfg", "missing.example.org")
	server := &teraskyv1alpha1.ConversionWebhookServer{}
	server.Name = "srv"
	server.Spec.Default = true

	c := newFakeClient(cfg, server).Build()
	r := &Reconciler{Client: c, ServerName: "srv", Registry: NewRegistry(), EnableXRDSupport: true}

	if err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := r.Registry.Get("missing.example.org")
	if !ok || entry.LastError == "" {
		t.Fatalf("expected a placeholder entry recording the missing-XRD failure, got ok=%v entry=%+v", ok, entry)
	}
}

func TestReconcileOneXRD_InvalidRules_RecordsFailure(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.Spokes[0].Rules[0].FieldRename = nil // strategy declared, no matching params
	server := &teraskyv1alpha1.ConversionWebhookServer{}
	server.Name = "srv"
	server.Spec.Default = true

	c := newFakeClient(xrd, cfg, server).Build()
	r := &Reconciler{Client: c, ServerName: "srv", Registry: NewRegistry(), EnableXRDSupport: true}

	if err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := r.Registry.Get("xfoos.example.org")
	if !ok || entry.LastError == "" {
		t.Fatalf("expected a recorded failure for invalid rules, got ok=%v entry=%+v", ok, entry)
	}
}

func TestReconcileOneXRD_AnalysisErrors_RecordsFailure(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.Spokes[0].Rules[0].FieldRename.HubPath = "spec.doesNotExist"
	server := &teraskyv1alpha1.ConversionWebhookServer{}
	server.Name = "srv"
	server.Spec.Default = true

	c := newFakeClient(xrd, cfg, server).Build()
	r := &Reconciler{Client: c, ServerName: "srv", Registry: NewRegistry(), EnableXRDSupport: true}

	if err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := r.Registry.Get("xfoos.example.org")
	if !ok || entry.LastError == "" {
		t.Fatalf("expected a recorded failure when analysis reports validation errors, got ok=%v entry=%+v", ok, entry)
	}
}

func TestReconcileOneCRD_HappyPath_Compiles(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	server := &teraskyv1alpha1.ConversionWebhookServer{}
	server.Name = "srv"
	server.Spec.Default = true

	c := newFakeClient(crd, cfg, server).Build()
	r := &Reconciler{Client: c, ServerName: "srv", Registry: NewRegistry(), EnableCRDSupport: true}

	if err := r.reconcileOneCRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := r.Registry.Get("foos.example.org")
	if !ok || entry.Router == nil {
		t.Fatalf("expected a compiled entry to be registered, got ok=%v entry=%+v", ok, entry)
	}
}

func TestReconcileOneCRD_TargetCRDMissing_RecordsFailure(t *testing.T) {
	cfg := renameRuleCRDConfig("cfg", "missing.example.org")
	server := &teraskyv1alpha1.ConversionWebhookServer{}
	server.Name = "srv"
	server.Spec.Default = true

	c := newFakeClient(cfg, server).Build()
	r := &Reconciler{Client: c, ServerName: "srv", Registry: NewRegistry(), EnableCRDSupport: true}

	if err := r.reconcileOneCRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := r.Registry.Get("missing.example.org")
	if !ok || entry.LastError == "" {
		t.Fatalf("expected a placeholder entry recording the missing-CRD failure, got ok=%v entry=%+v", ok, entry)
	}
}

func TestInitialSync_PopulatesRegistryForEnabledKinds(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	xrdCfg := renameRuleXRDConfig("xrd-cfg", "xfoos.example.org")
	crd := establishedCRD("foos.example.org")
	crdCfg := renameRuleCRDConfig("crd-cfg", "foos.example.org")
	server := &teraskyv1alpha1.ConversionWebhookServer{}
	server.Name = "srv"
	server.Spec.Default = true

	c := newFakeClient(xrd, xrdCfg, crd, crdCfg, server).Build()
	r := &Reconciler{Client: c, ServerName: "srv", Registry: NewRegistry(), EnableXRDSupport: true, EnableCRDSupport: true}

	if err := r.InitialSync(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := r.Registry.Get("xfoos.example.org"); !ok {
		t.Fatalf("expected the XRD's config to have been synced")
	}
	if _, ok := r.Registry.Get("foos.example.org"); !ok {
		t.Fatalf("expected the CRD's config to have been synced")
	}
}

func TestInitialSync_DisabledKindsAreSkipped(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	xrdCfg := renameRuleXRDConfig("xrd-cfg", "xfoos.example.org")
	server := &teraskyv1alpha1.ConversionWebhookServer{}
	server.Name = "srv"
	server.Spec.Default = true

	c := newFakeClient(xrd, xrdCfg, server).Build()
	r := &Reconciler{Client: c, ServerName: "srv", Registry: NewRegistry(), EnableXRDSupport: false, EnableCRDSupport: false}

	if err := r.InitialSync(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Registry.Len() != 0 {
		t.Fatalf("expected nothing to sync when both kinds are disabled, got len=%d", r.Registry.Len())
	}
}
