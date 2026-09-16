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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

// applyOnce drives the config to Applied against its current assignment.
func applyOnce(t *testing.T, r *XRDConversionConfigReconciler) *teraskyv1alpha1.XRDConversionConfig {
	t.Helper()
	for i := 0; i < 3; i++ {
		if _, err := reconcileXRD(t, r, "cfg"); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	got := getXRDConfig(t, r, "cfg")
	if got.Status.Phase != teraskyv1alpha1.PhaseApplied {
		t.Fatalf("fixture did not reach Applied: phase %q (%s)", got.Status.Phase, got.Status.Message)
	}
	return got
}

// moveTo repoints the config at another server and reconciles once.
func moveTo(t *testing.T, r *XRDConversionConfigReconciler, server string) *teraskyv1alpha1.XRDConversionConfig {
	t.Helper()
	cfg := getXRDConfig(t, r, "cfg")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: server}
	if err := r.Update(context.Background(), cfg); err != nil {
		t.Fatalf("repointing the config: %v", err)
	}
	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("reconcile after the move: %v", err)
	}
	return getXRDConfig(t, r, "cfg")
}

// The move is held until the destination reports it can already serve the
// target. Until then the XRD still names the source, and the source keeps
// serving it — so nothing is unserved while the gate is closed.
func TestXRDHandover_WaitsUntilTheDestinationCanServe(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	srcServer, srcSecret := readyServer("srv-a")
	dstServer, dstSecret := readyServer("srv-b")
	dstServer.Spec.Default = false

	c := newFakeClient(xrd, cfg, srcServer, srcSecret, dstServer, dstSecret,
		// srv-a's replicas serve it; srv-b's have published nothing about
		// it yet, which is the state immediately after a reassignment.
		replicaLease("srv-a", "a-1", "operator-ns", []string{"xfoos.example.org"}),
		replicaLease("srv-a", "a-2", "operator-ns", []string{"xfoos.example.org"}),
		replicaLease("srv-b", "b-1", "operator-ns", nil),
		replicaLease("srv-b", "b-2", "operator-ns", nil),
	).Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	applyOnce(t, r)
	got := moveTo(t, r, "srv-b")

	if meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.ConditionHandoverReady) {
		t.Fatal("HandoverReady is True although srv-b reports it cannot serve the target yet")
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionHandoverReady)
	if cond == nil || cond.Reason != "HandoverPending" {
		t.Fatalf("HandoverReady condition = %+v, want reason HandoverPending", cond)
	}
	if !strings.Contains(got.Status.Message, "srv-b") {
		t.Errorf("status message does not name the destination: %q", got.Status.Message)
	}
	// And critically: the XRD has NOT been repointed.
	if got.Status.WebhookURL == "" || !strings.Contains(got.Status.WebhookURL, "srv-a") {
		t.Fatalf("webhook URL is %q; while the gate is closed the target must still point at srv-a", got.Status.WebhookURL)
	}
}

// The gate has to stay closed across repeated reconciles, which is where
// the obvious implementation gets it wrong: status.assignedWebhookServer is
// written as soon as the resolver answers, so reading it back on the next
// pass says the move already happened and the target is repointed
// unverified on reconcile two.
func TestXRDHandover_StaysClosedAcrossRepeatedReconciles(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	srcServer, srcSecret := readyServer("srv-a")
	dstServer, dstSecret := readyServer("srv-b")
	dstServer.Spec.Default = false

	c := newFakeClient(xrd, cfg, srcServer, srcSecret, dstServer, dstSecret,
		replicaLease("srv-b", "b-1", "operator-ns", nil),
		replicaLease("srv-b", "b-2", "operator-ns", nil),
	).Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	applyOnce(t, r)
	moveTo(t, r, "srv-b")

	for i := 0; i < 5; i++ {
		if _, err := reconcileXRD(t, r, "cfg"); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
		got := getXRDConfig(t, r, "cfg")
		if !strings.Contains(got.Status.WebhookURL, "srv-a") {
			t.Fatalf("after %d further reconciles the target was repointed at %q although srv-b still reports it cannot serve it",
				i+1, got.Status.WebhookURL)
		}
		if meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.ConditionHandoverReady) {
			t.Fatalf("HandoverReady went True on reconcile %d with nothing having changed", i+1)
		}
	}
}

// Once the destination publishes the target, the move goes through.
func TestXRDHandover_ProceedsOnceTheDestinationReports(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	srcServer, srcSecret := readyServer("srv-a")
	dstServer, dstSecret := readyServer("srv-b")
	dstServer.Spec.Default = false

	c := newFakeClient(xrd, cfg, srcServer, srcSecret, dstServer, dstSecret,
		replicaLease("srv-b", "b-1", "operator-ns", []string{"xfoos.example.org"}),
		replicaLease("srv-b", "b-2", "operator-ns", []string{"xfoos.example.org"}),
	).Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	applyOnce(t, r)
	got := moveTo(t, r, "srv-b")

	if !meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.ConditionHandoverReady) {
		cond := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionHandoverReady)
		t.Fatalf("HandoverReady = %+v, want True once both destination replicas report the target", cond)
	}
	if got.Status.Phase != teraskyv1alpha1.PhaseApplied {
		t.Fatalf("phase = %q, want Applied (%s)", got.Status.Phase, got.Status.Message)
	}
	if !strings.Contains(got.Status.WebhookURL, "srv-b") {
		t.Fatalf("webhook URL is %q, want it repointed at srv-b", got.Status.WebhookURL)
	}
}

// The condition survives the reconciles that follow a completed move. It
// is the verdict on the last handover, and an operator whose replicas
// cannot publish their Leases only ever finds out through a
// HandoverUnverified that is still there when they look.
func TestXRDHandover_VerdictPersistsAfterTheMove(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	srcServer, srcSecret := readyServer("srv-a")
	dstServer, dstSecret := readyServer("srv-b")
	dstServer.Spec.Default = false

	c := newFakeClient(xrd, cfg, srcServer, srcSecret, dstServer, dstSecret,
		replicaLease("srv-b", "b-1", "operator-ns", []string{"xfoos.example.org"}),
		replicaLease("srv-b", "b-2", "operator-ns", []string{"xfoos.example.org"}),
	).Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	applyOnce(t, r)
	moveTo(t, r, "srv-b")
	for i := 0; i < 3; i++ {
		if _, err := reconcileXRD(t, r, "cfg"); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	cond := meta.FindStatusCondition(getXRDConfig(t, r, "cfg").Status.Conditions, teraskyv1alpha1.ConditionHandoverReady)
	if cond == nil {
		t.Fatal("the handover verdict was cleared once the move settled; nobody would ever see an unverified one")
	}
	if cond.Status != metav1.ConditionTrue || cond.Reason != "HandoverReady" {
		t.Fatalf("HandoverReady = %+v after the move settled", cond)
	}
}

// A fleet whose replicas publish nothing — mid-upgrade, or a namespace
// with no Lease Role — must keep working exactly as it did before the gate
// existed, and say that the handover was not verified.
func TestXRDHandover_ProceedsUnverifiedWhenNobodyPublishes(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	srcServer, srcSecret := readyServer("srv-a")
	dstServer, dstSecret := readyServer("srv-b")
	dstServer.Spec.Default = false

	c := newFakeClient(xrd, cfg, srcServer, srcSecret, dstServer, dstSecret).Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	applyOnce(t, r)
	got := moveTo(t, r, "srv-b")

	cond := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionHandoverReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "HandoverUnverified" {
		t.Fatalf("HandoverReady = %+v, want True/HandoverUnverified", cond)
	}
	if !strings.Contains(got.Status.WebhookURL, "srv-b") {
		t.Fatalf("webhook URL is %q; an unverifiable handover must still proceed, as it did before", got.Status.WebhookURL)
	}
}

// A first apply has no previous server still covering the target, so
// gating it would delay every new config for nothing.
func TestXRDHandover_FirstApplyIsNotGated(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	server, secret := readyServer("srv")

	c := newFakeClient(xrd, cfg, server, secret,
		// Replicas are up and publishing, but know nothing about this
		// target yet — exactly the state a brand-new config starts in.
		replicaLease("srv", "p-1", "operator-ns", nil),
		replicaLease("srv", "p-2", "operator-ns", nil),
	).Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}

	got := applyOnce(t, r)
	if meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionHandoverReady) != nil {
		t.Fatal("a first apply set a HandoverReady condition; there is nothing to hand over from")
	}
}
