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
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func reconcileXRD(t *testing.T, r *XRDConversionConfigReconciler, name string) (reconcile.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
}

func getXRDConfig(t *testing.T, r *XRDConversionConfigReconciler, name string) *teraskyv1alpha1.XRDConversionConfig {
	t.Helper()
	var cfg teraskyv1alpha1.XRDConversionConfig
	if err := r.Get(context.Background(), types.NamespacedName{Name: name}, &cfg); err != nil {
		t.Fatalf("getting XRDConversionConfig %q: %v", name, err)
	}
	return &cfg
}

func TestXRDReconcile_AddsFinalizerThenApplies(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	server, secret := readyServer("srv")

	c := newFakeClient(cfg, server, secret).build().Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	// First reconcile only adds the finalizer and returns (mirroring the
	// real controller-runtime flow: it Updates, then the caller's next
	// event drives reconcileNormal). Feeding it the live XRD only from the
	// second call on isn't required by the code, but keeping both present
	// throughout matches how a real cluster would behave and exercises the
	// same code path.
	c.Create(context.Background(), xrd)
	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getXRDConfig(t, r, "cfg")
	if !controllerutil.ContainsFinalizer(got, teraskyv1alpha1.XRDConversionConfigFinalizer) {
		t.Fatalf("expected the finalizer to be added")
	}

	// Second reconcile: finalizer already present, so this exercises
	// reconcileNormal's full happy path straight through to Applied.
	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error on second reconcile: %v", err)
	}
	got = getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseApplied {
		t.Fatalf("expected phase Applied, got %q (message: %s)", got.Status.Phase, got.Status.Message)
	}
	if got.Status.AssignedWebhookServer != "srv" {
		t.Fatalf("expected assigned server %q, got %q", "srv", got.Status.AssignedWebhookServer)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.ConditionApplied) {
		t.Fatalf("expected condition Applied=True, got %+v", got.Status.Conditions)
	}
	if got.Status.WebhookPath != "/convert/xfoos.example.org" {
		t.Fatalf("unexpected webhook path: %q", got.Status.WebhookPath)
	}

	// The XRD itself should now carry the patched conversion webhook config.
	var patchedXRD = establishedXRD("xfoos.example.org")
	if err := r.Get(context.Background(), types.NamespacedName{Name: "xfoos.example.org"}, patchedXRD); err != nil {
		t.Fatalf("getting patched XRD: %v", err)
	}
	strategy, found, _ := stringField(patchedXRD.Object, "spec", "conversion", "strategy")
	if !found || strategy != "Webhook" {
		t.Fatalf("expected spec.conversion.strategy=Webhook on the target XRD, found=%v strategy=%q", found, strategy)
	}
}

func stringField(obj map[string]any, path ...string) (string, bool, error) {
	m := obj
	for i, p := range path {
		v, ok := m[p]
		if !ok {
			return "", false, nil
		}
		if i == len(path)-1 {
			s, ok := v.(string)
			return s, ok, nil
		}
		next, ok := v.(map[string]any)
		if !ok {
			return "", false, nil
		}
		m = next
	}
	return "", false, nil
}

func TestXRDReconcile_TargetXRDMissing_MarksInvalid(t *testing.T) {
	cfg := renameRuleXRDConfig("cfg", "missing.example.org")
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	c := newFakeClient(cfg).build().Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseInvalid {
		t.Fatalf("expected phase Invalid, got %q", got.Status.Phase)
	}
	if meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.ConditionValidated) {
		t.Fatalf("expected condition Validated=False")
	}
}

func TestXRDReconcile_XRDNotEstablished_Pending(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	xrd.Object["status"] = map[string]any{} // strip the Established condition
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	server, secret := readyServer("srv")

	c := newFakeClient(cfg, server, secret).build().Build()
	c.Create(context.Background(), xrd)
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	res, err := reconcileXRD(t, r, "cfg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a requeue while waiting on the XRD to become Established")
	}
	got := getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhasePending {
		t.Fatalf("expected phase Pending, got %q", got.Status.Phase)
	}
}

func TestXRDReconcile_ServerNotReady_Pending(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	server, secret := readyServer("srv")
	server.Status.ReadyReplicas = 0 // not ready

	c := newFakeClient(cfg, server, secret).build().Build()
	c.Create(context.Background(), xrd)
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	res, err := reconcileXRD(t, r, "cfg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a requeue while waiting on the server to become ready")
	}
	got := getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhasePending {
		t.Fatalf("expected phase Pending, got %q", got.Status.Phase)
	}
}

func TestXRDReconcile_NoAssignableServer_Invalid(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	// No ConversionWebhookServer exists at all.
	c := newFakeClient(cfg).build().Build()
	c.Create(context.Background(), xrd)
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseInvalid {
		t.Fatalf("expected phase Invalid when no server can be resolved, got %q", got.Status.Phase)
	}
}

func TestXRDReconcile_Drift_FailClosed_RevertsAndFails(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.DriftPolicy = teraskyv1alpha1.DriftPolicyFailClosed
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied // simulate a previously-applied config
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	server, secret := readyServer("srv")

	c := newFakeClient(cfg, server, secret).build().Build()
	c.Create(context.Background(), xrd)
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	// Break the rule so analysis now errors (a rename targeting a
	// nonexistent hub field), simulating schema drift.
	live := getXRDConfig(t, r, "cfg")
	live.Spec.Spokes[0].Rules[0].FieldRename.HubPath = "spec.doesNotExist"
	if err := r.Update(context.Background(), live); err != nil {
		t.Fatalf("updating config: %v", err)
	}

	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseFailed {
		t.Fatalf("expected phase Failed under FailClosed drift, got %q (message: %s)", got.Status.Phase, got.Status.Message)
	}

	var patchedXRD = establishedXRD("xfoos.example.org")
	if err := r.Get(context.Background(), types.NamespacedName{Name: "xfoos.example.org"}, patchedXRD); err != nil {
		t.Fatalf("getting XRD: %v", err)
	}
	strategy, found, _ := stringField(patchedXRD.Object, "spec", "conversion", "strategy")
	if !found || strategy != "None" {
		t.Fatalf("expected the XRD to be reverted to strategy=None, found=%v strategy=%q", found, strategy)
	}
}

func TestXRDReconcile_Drift_KeepServingStale(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.DriftPolicy = teraskyv1alpha1.DriftPolicyKeepServingStale
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	server, secret := readyServer("srv")

	c := newFakeClient(cfg, server, secret).build().Build()
	c.Create(context.Background(), xrd)
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	live := getXRDConfig(t, r, "cfg")
	live.Spec.Spokes[0].Rules[0].FieldRename.HubPath = "spec.doesNotExist"
	if err := r.Update(context.Background(), live); err != nil {
		t.Fatalf("updating config: %v", err)
	}

	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseStale {
		t.Fatalf("expected phase Stale under KeepServingStale drift, got %q", got.Status.Phase)
	}
}

func TestXRDReconcile_Delete_BlockedByMultipleServedVersions(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org") // v1 and v2, both served
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	now := metav1.Now()
	cfg.DeletionTimestamp = &now

	c := newFakeClient(cfg).build().Build()
	c.Create(context.Background(), xrd)
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	res, err := reconcileXRD(t, r, "cfg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a requeue while deletion is blocked")
	}
	got := getXRDConfig(t, r, "cfg")
	if !controllerutil.ContainsFinalizer(got, teraskyv1alpha1.XRDConversionConfigFinalizer) {
		t.Fatalf("finalizer must not be removed while deletion is blocked")
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.ConditionDeletionBlocked) {
		t.Fatalf("expected condition DeletionBlocked=True")
	}
}

func TestXRDReconcile_Delete_UnsafeOverrideRemovesFinalizer(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	cfg.Annotations = map[string]string{teraskyv1alpha1.AllowUnsafeDeleteAnnotation: "true"}
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	now := metav1.Now()
	cfg.DeletionTimestamp = &now

	c := newFakeClient(cfg).build().Build()
	c.Create(context.Background(), xrd)
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got teraskyv1alpha1.XRDConversionConfig
	err := r.Get(context.Background(), types.NamespacedName{Name: "cfg"}, &got)
	if err == nil {
		t.Fatalf("expected the config to be gone once the fake client's finalizer-driven deletion completes, got %+v", got)
	}
}

func TestXRDReconcile_Delete_NeverAppliedSkipsRevert(t *testing.T) {
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	// Never applied: Phase is its zero value, not Applied/Stale.
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	now := metav1.Now()
	cfg.DeletionTimestamp = &now

	c := newFakeClient(cfg).build().Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got teraskyv1alpha1.XRDConversionConfig
	err := r.Get(context.Background(), types.NamespacedName{Name: "cfg"}, &got)
	if err == nil {
		t.Fatalf("expected the never-applied config to be removed immediately without touching any XRD")
	}
}

func TestXRDReconcile_NotFound_IsIgnored(t *testing.T) {
	c := newFakeClient().build().Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}
	if _, err := reconcileXRD(t, r, "does-not-exist"); err != nil {
		t.Fatalf("expected a NotFound Get to be swallowed, got %v", err)
	}
}
