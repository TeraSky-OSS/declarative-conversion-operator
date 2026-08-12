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

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

// Phase 2.1 failure-path coverage for CRDConversionConfig — mirrors the
// XRD scenarios in xrdconversionconfig_controller_failure_test.go.

func TestCRDReconcile_CrashBeforePatch_RestartsAndApplies(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	server, secret := readyServer("srv")

	var applyCalls atomic.Int32
	c := newFakeClient(crd, cfg, server, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
				if applyCalls.Add(1) == 1 {
					return fmt.Errorf("injected crash before CRD patch")
				}
				return c.Apply(ctx, obj, opts...)
			},
		}).
		Build()
	r := &CRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	_, err := reconcileCRD(t, r, "cfg")
	if err == nil {
		t.Fatalf("expected reconcile error when CRD patch crashes")
	}
	got := getCRDConfig(t, r, "cfg")
	if got.Status.Phase == teraskyv1alpha1.PhaseApplied {
		t.Fatalf("must not report Applied after a failed patch; phase=%q", got.Status.Phase)
	}
	var live extv1.CustomResourceDefinition
	if err := r.Get(context.Background(), types.NamespacedName{Name: "foos.example.org"}, &live); err != nil {
		t.Fatalf("getting CRD: %v", err)
	}
	if live.Spec.Conversion != nil && live.Spec.Conversion.Strategy == extv1.WebhookConverter {
		t.Fatalf("CRD must remain unpatched after crash-before-patch")
	}

	if _, err := reconcileCRD(t, r, "cfg"); err != nil {
		t.Fatalf("restart reconcile: %v", err)
	}
	got = getCRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseApplied {
		t.Fatalf("expected Applied after restart, got %q (%s)", got.Status.Phase, got.Status.Message)
	}
}

func TestCRDReconcile_CrashAfterPatchBeforeStatus_RestartsIdempotently(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	server, secret := readyServer("srv")

	var statusPatches atomic.Int32
	c := newFakeClient(crd, cfg, server, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if subResourceName == "status" {
					if statusPatches.Add(1) == 1 {
						return fmt.Errorf("injected crash before status Applied")
					}
				}
				return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()
	r := &CRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	_, err := reconcileCRD(t, r, "cfg")
	if err == nil {
		t.Fatalf("expected error when status patch crashes after CRD apply")
	}
	got := getCRDConfig(t, r, "cfg")
	if got.Status.Phase == teraskyv1alpha1.PhaseApplied {
		t.Fatalf("status must not show Applied after crashed status write; phase=%q", got.Status.Phase)
	}
	var live extv1.CustomResourceDefinition
	if err := r.Get(context.Background(), types.NamespacedName{Name: "foos.example.org"}, &live); err != nil {
		t.Fatalf("getting CRD: %v", err)
	}
	if live.Spec.Conversion == nil || live.Spec.Conversion.Strategy != extv1.WebhookConverter {
		t.Fatalf("expected CRD already patched to Webhook before the status crash, got %+v", live.Spec.Conversion)
	}
	// The fake client's typed SSA Apply can strip Spec.Versions on a
	// conversion-only patch. Re-seed them so the restart path exercises
	// idempotent status persistence rather than a schema-loss artifact.
	if len(live.Spec.Versions) == 0 {
		fresh := establishedCRD("foos.example.org")
		live.Spec.Versions = fresh.Spec.Versions
		live.Spec.Group = fresh.Spec.Group
		live.Spec.Names = fresh.Spec.Names
		live.Spec.Scope = fresh.Spec.Scope
		if err := r.Update(context.Background(), &live); err != nil {
			t.Fatalf("restoring CRD versions after fake-client Apply: %v", err)
		}
	}

	if _, err := reconcileCRD(t, r, "cfg"); err != nil {
		t.Fatalf("restart reconcile: %v", err)
	}
	got = getCRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseApplied {
		t.Fatalf("expected Applied after restart, got %q (%s)", got.Status.Phase, got.Status.Message)
	}
}

func TestCRDReconcile_SchemaDriftMidLifecycle_KeepServingStale(t *testing.T) {
	// Seed a previously-applied config with live webhook conversion. The
	// fake client's typed SSA Apply for CRDs can strip Spec.Versions, so
	// we avoid depending on a full apply round-trip here and focus on the
	// drift-policy branch under a mid-lifecycle schema change.
	crd := establishedCRD("foos.example.org")
	crd.Spec.Conversion = &extv1.CustomResourceConversion{
		Strategy: extv1.WebhookConverter,
		Webhook: &extv1.WebhookConversion{
			ClientConfig: &extv1.WebhookClientConfig{URL: strPtr("https://example.invalid/convert")},
		},
	}
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	cfg.Spec.DriftPolicy = teraskyv1alpha1.DriftPolicyKeepServingStale
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.CRDConversionConfigFinalizer)
	server, secret := readyServer("srv")

	c := newFakeClient(crd, cfg, server, secret).Build()
	r := &CRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	var live extv1.CustomResourceDefinition
	if err := r.Get(context.Background(), types.NamespacedName{Name: "foos.example.org"}, &live); err != nil {
		t.Fatalf("getting CRD: %v", err)
	}
	live.Spec.Versions[0].Schema = &extv1.CustomResourceValidation{OpenAPIV3Schema: &extv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"spec": {Type: "object", Properties: map[string]extv1.JSONSchemaProps{
				"capacity": {Type: "string"},
			}},
		},
	}}
	live.Generation = 2
	if err := r.Update(context.Background(), &live); err != nil {
		t.Fatalf("updating drifted CRD: %v", err)
	}

	if _, err := reconcileCRD(t, r, "cfg"); err != nil {
		t.Fatalf("drift reconcile: %v", err)
	}
	got := getCRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseStale {
		t.Fatalf("expected Stale under KeepServingStale schema drift, got %q (%s)", got.Status.Phase, got.Status.Message)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.ConditionStale) {
		t.Fatalf("expected ConditionStale=True, got %+v", got.Status.Conditions)
	}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "foos.example.org"}, &live); err != nil {
		t.Fatalf("getting CRD after drift: %v", err)
	}
	if live.Spec.Conversion == nil || live.Spec.Conversion.Strategy != extv1.WebhookConverter {
		t.Fatalf("KeepServingStale must leave webhook conversion in place, got %+v", live.Spec.Conversion)
	}
}

func TestCRDReconcile_SchemaDriftMidLifecycle_FailClosedReverts(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	crd.Spec.Conversion = &extv1.CustomResourceConversion{
		Strategy: extv1.WebhookConverter,
		Webhook: &extv1.WebhookConversion{
			ClientConfig: &extv1.WebhookClientConfig{URL: strPtr("https://example.invalid/convert")},
		},
	}
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	cfg.Spec.DriftPolicy = teraskyv1alpha1.DriftPolicyFailClosed
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.CRDConversionConfigFinalizer)
	server, secret := readyServer("srv")

	c := newFakeClient(crd, cfg, server, secret).Build()
	r := &CRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	var live extv1.CustomResourceDefinition
	if err := r.Get(context.Background(), types.NamespacedName{Name: "foos.example.org"}, &live); err != nil {
		t.Fatalf("getting CRD: %v", err)
	}
	live.Spec.Versions[0].Schema = &extv1.CustomResourceValidation{OpenAPIV3Schema: &extv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"spec": {Type: "object", Properties: map[string]extv1.JSONSchemaProps{
				"capacity": {Type: "string"},
			}},
		},
	}}
	live.Generation = 2
	if err := r.Update(context.Background(), &live); err != nil {
		t.Fatalf("updating drifted CRD: %v", err)
	}

	if _, err := reconcileCRD(t, r, "cfg"); err != nil {
		t.Fatalf("drift reconcile: %v", err)
	}
	got := getCRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseFailed {
		t.Fatalf("expected Failed under FailClosed schema drift, got %q (%s)", got.Status.Phase, got.Status.Message)
	}
	applied := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionApplied)
	if applied == nil || applied.Reason != teraskyv1alpha1.ReasonReverted {
		t.Fatalf("expected Reason=Reverted, got %+v", applied)
	}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "foos.example.org"}, &live); err != nil {
		t.Fatalf("getting CRD after FailClosed: %v", err)
	}
	if live.Spec.Conversion == nil || live.Spec.Conversion.Strategy != extv1.NoneConverter {
		t.Fatalf("FailClosed must revert to strategy=None, got %+v", live.Spec.Conversion)
	}
}

func TestCRDReconcile_FailClosed_PartwayRevertThenRecover(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	crd.Spec.Conversion = &extv1.CustomResourceConversion{
		Strategy: extv1.WebhookConverter,
		Webhook: &extv1.WebhookConversion{
			ClientConfig: &extv1.WebhookClientConfig{URL: strPtr("https://example.invalid/convert")},
		},
	}
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	cfg.Spec.DriftPolicy = teraskyv1alpha1.DriftPolicyFailClosed
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.ConditionApplied, Status: metav1.ConditionTrue, Reason: "Applied", Message: "previously applied",
	})
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.CRDConversionConfigFinalizer)
	server, secret := readyServer("srv")

	var applyCalls atomic.Int32
	c := newFakeClient(crd, cfg, server, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
				if applyCalls.Add(1) == 1 {
					return fmt.Errorf("injected partway revert failure")
				}
				return c.Apply(ctx, obj, opts...)
			},
		}).
		Build()
	r := &CRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	live := getCRDConfig(t, r, "cfg")
	live.Spec.Spokes[0].Rules[0].FieldRename.HubPath = "spec.doesNotExist"
	if err := r.Update(context.Background(), live); err != nil {
		t.Fatalf("updating config: %v", err)
	}

	if _, err := reconcileCRD(t, r, "cfg"); err == nil {
		t.Fatalf("expected partway revert to return an error")
	}
	got := getCRDConfig(t, r, "cfg")
	applied := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionApplied)
	if applied == nil || applied.Reason != teraskyv1alpha1.ReasonRevertFailed {
		t.Fatalf("expected RevertFailed after partway failure, got %+v", applied)
	}

	if _, err := reconcileCRD(t, r, "cfg"); err != nil {
		t.Fatalf("recovery reconcile: %v", err)
	}
	got = getCRDConfig(t, r, "cfg")
	applied = meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionApplied)
	if applied == nil || applied.Reason != teraskyv1alpha1.ReasonReverted {
		t.Fatalf("expected Reason=Reverted after successful retry, got %+v", applied)
	}
	var liveCRD extv1.CustomResourceDefinition
	if err := r.Get(context.Background(), types.NamespacedName{Name: "foos.example.org"}, &liveCRD); err != nil {
		t.Fatalf("getting CRD: %v", err)
	}
	if liveCRD.Spec.Conversion == nil || liveCRD.Spec.Conversion.Strategy != extv1.NoneConverter {
		t.Fatalf("expected strategy=None after recovered FailClosed revert, got %+v", liveCRD.Spec.Conversion)
	}
}
