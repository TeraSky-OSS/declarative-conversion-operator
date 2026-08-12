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
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// Phase 2.1 failure-path coverage for FailClosed / KeepServingStale.
// These use the fake client + interceptors (no envtest) to exercise the
// branches the architecture safety table claims exist: mid-path revert
// failure, crash between compile and patch, and schema drift mid-lifecycle.

func TestXRDReconcile_CrashBeforePatch_RestartsAndApplies(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	server, secret := readyServer("srv")

	var applyCalls atomic.Int32
	c := newFakeClient(cfg, server, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
				if applyCalls.Add(1) == 1 {
					// Simulate a controller crash after analysis/compile
					// succeeded but before the XRD conversion patch landed.
					return fmt.Errorf("injected crash before XRD patch")
				}
				return c.Apply(ctx, obj, opts...)
			},
		}).
		Build()
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	// First reconcile adds the finalizer and continues into apply; the
	// injected Apply failure simulates a crash after compile/validation.
	_, err := reconcileXRD(t, r, "cfg")
	if err == nil {
		t.Fatalf("expected reconcile error when XRD patch crashes")
	}
	got := getXRDConfig(t, r, "cfg")
	if got.Status.Phase == teraskyv1alpha1.PhaseApplied {
		t.Fatalf("must not report Applied after a failed patch; phase=%q", got.Status.Phase)
	}
	liveXRD := &unstructured.Unstructured{}
	liveXRD.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(context.Background(), types.NamespacedName{Name: "xfoos.example.org"}, liveXRD); err != nil {
		t.Fatalf("getting XRD: %v", err)
	}
	if strategy, found := stringField(liveXRD.Object, "spec", "conversion", "strategy"); found && strategy == "Webhook" {
		t.Fatalf("XRD must remain unpatched after crash-before-patch")
	}

	// Restart: same reconciler, working Apply — completes to Applied.
	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("restart reconcile: %v", err)
	}
	got = getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseApplied {
		t.Fatalf("expected Applied after restart, got %q (%s)", got.Status.Phase, got.Status.Message)
	}
}

func TestXRDReconcile_CrashAfterPatchBeforeStatus_RestartsIdempotently(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	server, secret := readyServer("srv")

	var statusPatches atomic.Int32
	c := newFakeClient(cfg, server, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if subResourceName == "status" {
					if statusPatches.Add(1) == 1 {
						// Patch landed on the XRD; crash before status
						// ObservedGeneration/Phase=Applied is persisted.
						return fmt.Errorf("injected crash before status Applied")
					}
				}
				return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	// Finalizer + apply succeed; status persistence crashes — classic
	// "target patched, ObservedGeneration/Phase not yet Applied" window.
	_, err := reconcileXRD(t, r, "cfg")
	if err == nil {
		t.Fatalf("expected error when status patch crashes after XRD apply")
	}
	got := getXRDConfig(t, r, "cfg")
	if got.Status.Phase == teraskyv1alpha1.PhaseApplied {
		t.Fatalf("status must not show Applied after crashed status write; phase=%q", got.Status.Phase)
	}
	liveXRD := &unstructured.Unstructured{}
	liveXRD.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(context.Background(), types.NamespacedName{Name: "xfoos.example.org"}, liveXRD); err != nil {
		t.Fatalf("getting XRD: %v", err)
	}
	strategy, found := stringField(liveXRD.Object, "spec", "conversion", "strategy")
	if !found || strategy != "Webhook" {
		t.Fatalf("expected XRD already patched to Webhook before the status crash, found=%v strategy=%q", found, strategy)
	}

	// Restart reconciles idempotently: re-applies SSA and finally persists Applied.
	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("restart reconcile: %v", err)
	}
	got = getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseApplied {
		t.Fatalf("expected Applied after restart, got %q (%s)", got.Status.Phase, got.Status.Message)
	}
}

func TestXRDReconcile_SchemaDriftMidLifecycle_KeepServingStale(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.DriftPolicy = teraskyv1alpha1.DriftPolicyKeepServingStale
	server, secret := readyServer("srv")

	c := newFakeClient(cfg, server, secret).Build()
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("finalizer reconcile: %v", err)
	}
	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("apply reconcile: %v", err)
	}
	if got := getXRDConfig(t, r, "cfg"); got.Status.Phase != teraskyv1alpha1.PhaseApplied {
		t.Fatalf("precondition: expected Applied, got %q", got.Status.Phase)
	}

	// Drift the live XRD schema mid-lifecycle (hub field disappears) while
	// the config rules stay unchanged — the failure branch the drift
	// policies exist to handle.
	liveXRD := &unstructured.Unstructured{}
	liveXRD.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(context.Background(), types.NamespacedName{Name: "xfoos.example.org"}, liveXRD); err != nil {
		t.Fatalf("getting XRD: %v", err)
	}
	versions, _, _ := unstructured.NestedSlice(liveXRD.Object, "spec", "versions")
	hub := versions[0].(map[string]any)
	_ = unstructured.SetNestedMap(hub, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"spec": map[string]any{"type": "object", "properties": map[string]any{
				// hub no longer has spec.size — rename rule is now invalid
				"capacity": map[string]any{"type": "string"},
			}},
		},
	}, "schema", "openAPIV3Schema")
	versions[0] = hub
	if err := unstructured.SetNestedSlice(liveXRD.Object, versions, "spec", "versions"); err != nil {
		t.Fatalf("mutating XRD schema: %v", err)
	}
	liveXRD.SetGeneration(2)
	if err := r.Update(context.Background(), liveXRD); err != nil {
		t.Fatalf("updating drifted XRD: %v", err)
	}

	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("drift reconcile: %v", err)
	}
	got := getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseStale {
		t.Fatalf("expected Stale under KeepServingStale schema drift, got %q (%s)", got.Status.Phase, got.Status.Message)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.ConditionStale) {
		t.Fatalf("expected ConditionStale=True, got %+v", got.Status.Conditions)
	}
	patched := &unstructured.Unstructured{}
	patched.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(context.Background(), types.NamespacedName{Name: "xfoos.example.org"}, patched); err != nil {
		t.Fatalf("getting XRD after drift: %v", err)
	}
	strategy, found := stringField(patched.Object, "spec", "conversion", "strategy")
	if !found || strategy != "Webhook" {
		t.Fatalf("KeepServingStale must leave webhook conversion in place, found=%v strategy=%q", found, strategy)
	}
}

func TestXRDReconcile_SchemaDriftMidLifecycle_FailClosedReverts(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.DriftPolicy = teraskyv1alpha1.DriftPolicyFailClosed
	server, secret := readyServer("srv")

	c := newFakeClient(cfg, server, secret).Build()
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("finalizer reconcile: %v", err)
	}
	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("apply reconcile: %v", err)
	}

	liveXRD := &unstructured.Unstructured{}
	liveXRD.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(context.Background(), types.NamespacedName{Name: "xfoos.example.org"}, liveXRD); err != nil {
		t.Fatalf("getting XRD: %v", err)
	}
	versions, _, _ := unstructured.NestedSlice(liveXRD.Object, "spec", "versions")
	hub := versions[0].(map[string]any)
	_ = unstructured.SetNestedMap(hub, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"spec": map[string]any{"type": "object", "properties": map[string]any{
				"capacity": map[string]any{"type": "string"},
			}},
		},
	}, "schema", "openAPIV3Schema")
	versions[0] = hub
	if err := unstructured.SetNestedSlice(liveXRD.Object, versions, "spec", "versions"); err != nil {
		t.Fatalf("mutating XRD schema: %v", err)
	}
	liveXRD.SetGeneration(2)
	if err := r.Update(context.Background(), liveXRD); err != nil {
		t.Fatalf("updating drifted XRD: %v", err)
	}

	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("drift reconcile: %v", err)
	}
	got := getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseFailed {
		t.Fatalf("expected Failed under FailClosed schema drift, got %q (%s)", got.Status.Phase, got.Status.Message)
	}
	applied := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionApplied)
	if applied == nil || applied.Reason != teraskyv1alpha1.ReasonReverted {
		t.Fatalf("expected Reason=Reverted, got %+v", applied)
	}
	patched := &unstructured.Unstructured{}
	patched.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(context.Background(), types.NamespacedName{Name: "xfoos.example.org"}, patched); err != nil {
		t.Fatalf("getting XRD after FailClosed: %v", err)
	}
	strategy, found := stringField(patched.Object, "spec", "conversion", "strategy")
	if !found || strategy != "None" {
		t.Fatalf("FailClosed must revert to strategy=None, found=%v strategy=%q", found, strategy)
	}
}

func TestXRDReconcile_FailClosed_PartwayRevertThenRecover(t *testing.T) {
	// Builds on Phase 0.5: first FailClosed revert fails mid-path; a later
	// reconcile with a working Apply finishes the tear-down honestly.
	xrd := establishedXRD("xfoos.example.org")
	if err := unstructured.SetNestedMap(xrd.Object, map[string]any{
		"strategy": "Webhook",
		"webhook":  map[string]any{"clientConfig": map[string]any{"url": "https://example.invalid/convert"}},
	}, "spec", "conversion"); err != nil {
		t.Fatalf("seeding webhook conversion: %v", err)
	}
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.DriftPolicy = teraskyv1alpha1.DriftPolicyFailClosed
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.ConditionApplied, Status: metav1.ConditionTrue, Reason: "Applied", Message: "previously applied",
	})
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.XRDConversionConfigFinalizer)
	server, secret := readyServer("srv")

	var applyCalls atomic.Int32
	c := newFakeClient(cfg, server, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
				if applyCalls.Add(1) == 1 {
					return fmt.Errorf("injected partway revert failure")
				}
				return c.Apply(ctx, obj, opts...)
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

	if _, err := reconcileXRD(t, r, "cfg"); err == nil {
		t.Fatalf("expected partway revert to return an error")
	}
	got := getXRDConfig(t, r, "cfg")
	applied := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionApplied)
	if applied == nil || applied.Reason != teraskyv1alpha1.ReasonRevertFailed {
		t.Fatalf("expected RevertFailed after partway failure, got %+v", applied)
	}

	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("recovery reconcile: %v", err)
	}
	got = getXRDConfig(t, r, "cfg")
	applied = meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionApplied)
	if applied == nil || applied.Reason != teraskyv1alpha1.ReasonReverted {
		t.Fatalf("expected Reason=Reverted after successful retry, got %+v", applied)
	}
	liveXRD := &unstructured.Unstructured{}
	liveXRD.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(context.Background(), types.NamespacedName{Name: "xfoos.example.org"}, liveXRD); err != nil {
		t.Fatalf("getting XRD: %v", err)
	}
	strategy, found := stringField(liveXRD.Object, "spec", "conversion", "strategy")
	if !found || strategy != "None" {
		t.Fatalf("expected strategy=None after recovered FailClosed revert, found=%v strategy=%q", found, strategy)
	}
}
