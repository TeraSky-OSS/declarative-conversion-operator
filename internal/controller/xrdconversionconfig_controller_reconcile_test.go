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
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
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

	c := newFakeClient(cfg, server, secret).Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
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
	patchedXRD := &unstructured.Unstructured{}
	patchedXRD.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(context.Background(), types.NamespacedName{Name: "xfoos.example.org"}, patchedXRD); err != nil {
		t.Fatalf("getting patched XRD: %v", err)
	}
	strategy, found := stringField(patchedXRD.Object, "spec", "conversion", "strategy")
	if !found || strategy != "Webhook" {
		t.Fatalf("expected spec.conversion.strategy=Webhook on the target XRD, found=%v strategy=%q", found, strategy)
	}
}

func stringField(obj map[string]any, path ...string) (string, bool) {
	m := obj
	for i, p := range path {
		v, ok := m[p]
		if !ok {
			return "", false
		}
		if i == len(path)-1 {
			s, ok := v.(string)
			return s, ok
		}
		next, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		m = next
	}
	return "", false
}

func TestXRDReconcile_TargetXRDMissing_MarksInvalid(t *testing.T) {
	cfg := renameRuleXRDConfig("cfg", "missing.example.org")
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	c := newFakeClient(cfg).Build()
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

	c := newFakeClient(cfg, server, secret).Build()
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
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

	c := newFakeClient(cfg, server, secret).Build()
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
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
	c := newFakeClient(cfg).Build()
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
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

	c := newFakeClient(cfg, server, secret).Build()
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
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
	applied := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionApplied)
	if applied == nil || applied.Status != metav1.ConditionFalse || applied.Reason != teraskyv1alpha1.ReasonReverted {
		t.Fatalf("expected ConditionApplied=False Reason=Reverted after successful FailClosed revert, got %+v", applied)
	}

	patchedXRD := &unstructured.Unstructured{}
	patchedXRD.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(context.Background(), types.NamespacedName{Name: "xfoos.example.org"}, patchedXRD); err != nil {
		t.Fatalf("getting XRD: %v", err)
	}
	strategy, found := stringField(patchedXRD.Object, "spec", "conversion", "strategy")
	if !found || strategy != "None" {
		t.Fatalf("expected the XRD to be reverted to strategy=None, found=%v strategy=%q", found, strategy)
	}
}

func TestXRDReconcile_Drift_FailClosed_RevertFailure_HonestStatus(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	// Pretend the live XRD still has webhook conversion wired.
	if err := unstructured.SetNestedMap(xrd.Object, map[string]any{
		"strategy": "Webhook",
		"webhook":  map[string]any{"clientConfig": map[string]any{"url": "https://example.invalid/convert"}},
	}, "spec", "conversion"); err != nil {
		t.Fatalf("seeding webhook conversion on the XRD fixture: %v", err)
	}
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.DriftPolicy = teraskyv1alpha1.DriftPolicyFailClosed
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.ConditionApplied, Status: metav1.ConditionTrue, Reason: "Applied", Message: "previously applied",
	})
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	server, secret := readyServer("srv")

	c := newFakeClient(cfg, server, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
				return fmt.Errorf("injected apply failure")
			},
		}).
		Build()
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	live := getXRDConfig(t, r, "cfg")
	live.Spec.Spokes[0].Rules[0].FieldRename.HubPath = "spec.doesNotExist"
	if err := r.Update(context.Background(), live); err != nil {
		t.Fatalf("updating config: %v", err)
	}

	_, err := reconcileXRD(t, r, "cfg")
	if err == nil {
		t.Fatalf("expected reconcile to return an error so the work is requeued with backoff")
	}
	if !strings.Contains(err.Error(), "injected apply failure") {
		t.Fatalf("expected wrapped revert error, got: %v", err)
	}

	got := getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseFailed {
		t.Fatalf("expected phase Failed after revert failure, got %q", got.Status.Phase)
	}
	if strings.Contains(got.Status.Message, "reverted to strategy=None") {
		t.Fatalf("status must not claim a successful revert; message=%q", got.Status.Message)
	}
	if !strings.Contains(got.Status.Message, "failed to revert") {
		t.Fatalf("expected honest failed-to-revert message, got %q", got.Status.Message)
	}
	applied := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionApplied)
	if applied == nil || applied.Status != metav1.ConditionFalse || applied.Reason != teraskyv1alpha1.ReasonRevertFailed {
		t.Fatalf("expected ConditionApplied=False Reason=RevertFailed, got %+v", applied)
	}

	liveXRD := &unstructured.Unstructured{}
	liveXRD.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(context.Background(), types.NamespacedName{Name: "xfoos.example.org"}, liveXRD); err != nil {
		t.Fatalf("getting XRD: %v", err)
	}
	strategy, found := stringField(liveXRD.Object, "spec", "conversion", "strategy")
	if !found || strategy != "Webhook" {
		t.Fatalf("expected live XRD conversion to remain Webhook after failed revert, found=%v strategy=%q", found, strategy)
	}

	// A subsequent reconcile with the same injected failure must keep
	// retrying FailClosed revert (not treat Phase=Failed as terminal success).
	_, err = reconcileXRD(t, r, "cfg")
	if err == nil {
		t.Fatalf("expected second reconcile to keep returning revert errors for backoff requeue")
	}
	got = getXRDConfig(t, r, "cfg")
	applied = meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionApplied)
	if applied == nil || applied.Reason != teraskyv1alpha1.ReasonRevertFailed {
		t.Fatalf("expected RevertFailed to remain so FailClosed keeps retrying, got %+v", applied)
	}
}

func TestXRDReconcile_Drift_KeepServingStale(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.DriftPolicy = teraskyv1alpha1.DriftPolicyKeepServingStale
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	server, secret := readyServer("srv")

	c := newFakeClient(cfg, server, secret).Build()
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
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
	if !meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.ConditionStale) {
		t.Fatalf("expected condition Stale=True alongside phase Stale, got %+v", got.Status.Conditions)
	}

	// Reconcile again while drift remains: PhaseStale must keep wasApplied
	// semantics so we stay Stale instead of demoting to Invalid.
	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error on second drift reconcile: %v", err)
	}
	got = getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseStale {
		t.Fatalf("expected phase to remain Stale on second reconcile, got %q", got.Status.Phase)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.ConditionStale) {
		t.Fatalf("expected ConditionStale=True to remain on second reconcile, got %+v", got.Status.Conditions)
	}

	// Resolve the drift: restore a valid hub path and re-reconcile.
	live = getXRDConfig(t, r, "cfg")
	live.Spec.Spokes[0].Rules[0].FieldRename.HubPath = "spec.size"
	if err := r.Update(context.Background(), live); err != nil {
		t.Fatalf("restoring valid rule: %v", err)
	}
	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error after resolving drift: %v", err)
	}
	got = getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseApplied {
		t.Fatalf("expected phase Applied after resolving drift, got %q (message: %s)", got.Status.Phase, got.Status.Message)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionStale); cond != nil {
		t.Fatalf("expected ConditionStale to be cleared after drift resolves, got %+v", cond)
	}
}

func TestXRDReconcile_Delete_BlockedByMultipleServedVersions(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org") // v1 and v2, both served
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	now := metav1.Now()
	cfg.DeletionTimestamp = &now

	c := newFakeClient(cfg).Build()
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
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

	c := newFakeClient(cfg).Build()
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
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

	c := newFakeClient(cfg).Build()
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
	c := newFakeClient().Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}
	if _, err := reconcileXRD(t, r, "does-not-exist"); err != nil {
		t.Fatalf("expected a NotFound Get to be swallowed, got %v", err)
	}
}
