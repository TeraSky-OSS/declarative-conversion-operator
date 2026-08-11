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

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func reconcileCRD(t *testing.T, r *CRDConversionConfigReconciler, name string) (reconcile.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
}

func getCRDConfig(t *testing.T, r *CRDConversionConfigReconciler, name string) *teraskyv1alpha1.CRDConversionConfig {
	t.Helper()
	var cfg teraskyv1alpha1.CRDConversionConfig
	if err := r.Get(context.Background(), types.NamespacedName{Name: name}, &cfg); err != nil {
		t.Fatalf("getting CRDConversionConfig %q: %v", name, err)
	}
	return &cfg
}

func TestCRDReconcile_AddsFinalizerThenApplies(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	server, secret := readyServer("srv")

	c := newFakeClient(crd, cfg, server, secret).Build()
	r := &CRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	// A single Reconcile call both adds the finalizer and runs
	// reconcileNormal through to Applied (unlike some controllers, this one
	// doesn't split that across two events). Deliberately not
	// double-reconciling here: the fake client's typed Apply for
	// CustomResourceDefinition doesn't faithfully preserve fields the
	// applied config doesn't mention (spec.versions gets dropped), which a
	// real API server's server-side apply would not do — a fake-client
	// fidelity gap, not a reconciler bug, but one a second Apply in this
	// test would trip over.
	if _, err := reconcileCRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := getCRDConfig(t, r, "cfg")
	if !controllerutil.ContainsFinalizer(got, teraskyv1alpha1.CRDConversionConfigFinalizer) {
		t.Fatalf("expected the finalizer to be added")
	}
	if got.Status.Phase != teraskyv1alpha1.PhaseApplied {
		t.Fatalf("expected phase Applied, got %q (message: %s)", got.Status.Phase, got.Status.Message)
	}
	if got.Status.AssignedWebhookServer != "srv" {
		t.Fatalf("expected assigned server %q, got %q", "srv", got.Status.AssignedWebhookServer)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.ConditionApplied) {
		t.Fatalf("expected condition Applied=True")
	}

	var patched extv1.CustomResourceDefinition
	if err := r.Get(context.Background(), types.NamespacedName{Name: "foos.example.org"}, &patched); err != nil {
		t.Fatalf("getting patched CRD: %v", err)
	}
	if patched.Spec.Conversion == nil || patched.Spec.Conversion.Strategy != extv1.WebhookConverter {
		t.Fatalf("expected spec.conversion.strategy=Webhook on the target CRD, got %+v", patched.Spec.Conversion)
	}
}

func TestCRDReconcile_TargetCRDMissing_MarksInvalid(t *testing.T) {
	cfg := renameRuleCRDConfig("cfg", "missing.example.org")
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.CRDConversionConfigFinalizer)
	c := newFakeClient(cfg).Build()
	r := &CRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	if _, err := reconcileCRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := getCRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseInvalid {
		t.Fatalf("expected phase Invalid, got %q", got.Status.Phase)
	}
}

func TestCRDReconcile_CRDNotEstablished_Pending(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	crd.Status.Conditions = nil
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.CRDConversionConfigFinalizer)
	server, secret := readyServer("srv")

	c := newFakeClient(crd, cfg, server, secret).Build()
	r := &CRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	res, err := reconcileCRD(t, r, "cfg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a requeue while waiting on the CRD to become Established")
	}
	got := getCRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhasePending {
		t.Fatalf("expected phase Pending, got %q", got.Status.Phase)
	}
}

func TestCRDReconcile_Drift_FailClosed_RevertsAndFails(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	cfg.Spec.DriftPolicy = teraskyv1alpha1.DriftPolicyFailClosed
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.CRDConversionConfigFinalizer)
	server, secret := readyServer("srv")

	c := newFakeClient(crd, cfg, server, secret).Build()
	r := &CRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	live := getCRDConfig(t, r, "cfg")
	live.Spec.Spokes[0].Rules[0].FieldRename.HubPath = "spec.doesNotExist"
	if err := r.Update(context.Background(), live); err != nil {
		t.Fatalf("updating config: %v", err)
	}

	if _, err := reconcileCRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := getCRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseFailed {
		t.Fatalf("expected phase Failed under FailClosed drift, got %q (message: %s)", got.Status.Phase, got.Status.Message)
	}

	var patched extv1.CustomResourceDefinition
	if err := r.Get(context.Background(), types.NamespacedName{Name: "foos.example.org"}, &patched); err != nil {
		t.Fatalf("getting CRD: %v", err)
	}
	if patched.Spec.Conversion == nil || patched.Spec.Conversion.Strategy != extv1.NoneConverter {
		t.Fatalf("expected the CRD to be reverted to strategy=None, got %+v", patched.Spec.Conversion)
	}
}

func TestCRDReconcile_Delete_BlockedByMultipleServedVersions(t *testing.T) {
	crd := establishedCRD("foos.example.org") // v1 and v2, both served
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.CRDConversionConfigFinalizer)
	now := metav1.Now()
	cfg.DeletionTimestamp = &now

	c := newFakeClient(crd, cfg).Build()
	r := &CRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	res, err := reconcileCRD(t, r, "cfg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a requeue while deletion is blocked")
	}
	got := getCRDConfig(t, r, "cfg")
	if !controllerutil.ContainsFinalizer(got, teraskyv1alpha1.CRDConversionConfigFinalizer) {
		t.Fatalf("finalizer must not be removed while deletion is blocked")
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.ConditionDeletionBlocked) {
		t.Fatalf("expected condition DeletionBlocked=True")
	}
}

func TestCRDReconcile_Delete_UnsafeOverrideRemovesFinalizer(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	cfg.Status.Phase = teraskyv1alpha1.PhaseApplied
	cfg.Annotations = map[string]string{teraskyv1alpha1.AllowUnsafeDeleteAnnotation: "true"}
	controllerutil.AddFinalizer(cfg, teraskyv1alpha1.CRDConversionConfigFinalizer)
	now := metav1.Now()
	cfg.DeletionTimestamp = &now

	c := newFakeClient(crd, cfg).Build()
	r := &CRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	if _, err := reconcileCRD(t, r, "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got teraskyv1alpha1.CRDConversionConfig
	if err := r.Get(context.Background(), types.NamespacedName{Name: "cfg"}, &got); err == nil {
		t.Fatalf("expected the config to be gone once its finalizer is removed, got %+v", got)
	}
}

func TestCRDReconcile_NotFound_IsIgnored(t *testing.T) {
	c := newFakeClient().Build()
	r := &CRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}
	if _, err := reconcileCRD(t, r, "does-not-exist"); err != nil {
		t.Fatalf("expected a NotFound Get to be swallowed, got %v", err)
	}
}
