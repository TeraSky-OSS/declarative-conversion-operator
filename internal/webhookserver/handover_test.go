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
	"time"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

// pointXRDAt writes the spec.conversion an applied XRD would carry.
func pointXRDAt(xrd *unstructured.Unstructured, serverName string) {
	_ = unstructured.SetNestedMap(xrd.Object, map[string]any{
		"strategy": "Webhook",
		"webhook": map[string]any{
			"clientConfig": map[string]any{
				"service": map[string]any{
					"name":      teraskyv1alpha1.WebhookServerServiceName(serverName),
					"namespace": "dco-system",
					"path":      "/convert/" + xrd.GetName(),
				},
			},
		},
	}, "spec", "conversion")
}

func pointCRDAt(crd *extv1.CustomResourceDefinition, serverName string) {
	crd.Spec.Conversion = &extv1.CustomResourceConversion{
		Strategy: extv1.WebhookConverter,
		Webhook: &extv1.WebhookConversion{
			ClientConfig: &extv1.WebhookClientConfig{
				Service: &extv1.ServiceReference{
					Name:      teraskyv1alpha1.WebhookServerServiceName(serverName),
					Namespace: "dco-system",
				},
			},
		},
	}
}

func twoServers() []*teraskyv1alpha1.ConversionWebhookServer {
	a := &teraskyv1alpha1.ConversionWebhookServer{}
	a.Name = "srv-a"
	b := &teraskyv1alpha1.ConversionWebhookServer{}
	b.Name = "srv-b"
	return []*teraskyv1alpha1.ConversionWebhookServer{a, b}
}

// The losing half of a safe handover. The config has been reassigned to
// srv-b, but the XRD's conversion webhook still names srv-a's Service —
// so srv-a is still the endpoint the apiserver calls, and dropping the
// plan now would fail every read and write of the resource until the
// operator gets round to repointing it.
func TestReconcileOneXRD_KeepsServingWhileTheTargetStillPointsHere(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	pointXRDAt(xrd, "srv-a")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-b"}

	servers := twoServers()
	c := newFakeClient(xrd, cfg, servers[0], servers[1]).Build()
	r := &Reconciler{Client: c, ServerName: "srv-a", Registry: NewRegistry(), EnableXRDSupport: true}

	if _, err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := r.Registry.Get("xfoos.example.org")
	if !ok || entry.Router == nil {
		t.Fatal("srv-a dropped the plan while the XRD still pointed its conversion webhook at srv-a; every read of the resource fails until the operator repoints it")
	}
}

// Once the operator has repointed the XRD, the handover is over — but not
// instantly. The apiserver refreshes a CRD's conversion configuration
// asynchronously after the write, so for a short window it is still
// calling srv-a; dropping the plan the moment the object changes answers
// those calls with a 503. The plan goes only after the drain.
//
// This is not theoretical. Before the drain existed, hack/e2e-reassign.sh
// caught exactly one failed write in 9,456 across three reassignments,
// with the registry-miss message.
func TestReconcileOneXRD_DrainsBeforeDroppingAHandedOverTarget(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	pointXRDAt(xrd, "srv-b")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-b"}

	servers := twoServers()
	clock := newFakeClock()
	c := newFakeClient(xrd, cfg, servers[0], servers[1]).Build()
	r := &Reconciler{Client: c, ServerName: "srv-a", Registry: NewRegistry(), EnableXRDSupport: true, now: clock.Now}
	r.Registry.Set("xfoos.example.org", &CompiledEntry{})

	requeue, err := r.reconcileOneXRD(context.Background(), "cfg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requeue != DefaultTargetDrainPeriod {
		t.Fatalf("requeue = %s, want the drain period %s — without a requeue the plan would be held until the next watch event",
			requeue, DefaultTargetDrainPeriod)
	}
	if _, ok := r.Registry.Get("xfoos.example.org"); !ok {
		t.Fatal("srv-a dropped the plan immediately; the apiserver is still routing here for a moment after the repoint")
	}

	// Part way through: still held.
	clock.Advance(DefaultTargetDrainPeriod / 2)
	requeue, err = r.reconcileOneXRD(context.Background(), "cfg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requeue <= 0 {
		t.Fatalf("requeue = %s half way through the drain, want the remainder", requeue)
	}
	if _, ok := r.Registry.Get("xfoos.example.org"); !ok {
		t.Fatal("srv-a dropped the plan half way through the drain")
	}

	// Past it: dropped.
	clock.Advance(DefaultTargetDrainPeriod)
	requeue, err = r.reconcileOneXRD(context.Background(), "cfg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requeue != 0 {
		t.Fatalf("requeue = %s after the drain elapsed, want none", requeue)
	}
	if _, ok := r.Registry.Get("xfoos.example.org"); ok {
		t.Fatal("srv-a kept the plan after the drain elapsed; it would hold a plan for everything it has ever served")
	}
}

// A move that reverts mid-drain must not leave the target scheduled for
// removal — otherwise the next reconcile after the revert would drop a
// plan this replica has just been given back.
func TestReconcileOneXRD_DrainIsCancelledIfTheTargetComesBack(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	pointXRDAt(xrd, "srv-b")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-b"}

	servers := twoServers()
	clock := newFakeClock()
	c := newFakeClient(xrd, cfg, servers[0], servers[1]).Build()
	r := &Reconciler{Client: c, ServerName: "srv-a", Registry: NewRegistry(), EnableXRDSupport: true, now: clock.Now}
	r.Registry.Set("xfoos.example.org", &CompiledEntry{})

	if _, err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("starting the drain: %v", err)
	}

	// The move is abandoned: the config points back at srv-a.
	live := getXRDConfigFromClient(t, r, "cfg")
	live.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-a"}
	if err := r.Update(context.Background(), live); err != nil {
		t.Fatalf("reverting the move: %v", err)
	}
	if _, err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("reconcile after the revert: %v", err)
	}

	// Long past the original deadline, and it must still be here.
	clock.Advance(DefaultTargetDrainPeriod * 10)
	if _, err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("reconcile after the drain would have elapsed: %v", err)
	}
	entry, ok := r.Registry.Get("xfoos.example.org")
	if !ok || entry.Router == nil {
		t.Fatal("a drain from an abandoned move fired anyway and dropped a target this replica owns")
	}
}

// An XRD that names nobody — never applied, or reverted — must not keep a
// plan alive on a server the resolver no longer assigns it to.
func TestReconcileOneXRD_DropsWhenTheTargetNamesNobody(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-b"}

	servers := twoServers()
	c := newFakeClient(xrd, cfg, servers[0], servers[1]).Build()
	r := &Reconciler{Client: c, ServerName: "srv-a", Registry: NewRegistry(), EnableXRDSupport: true, TargetDrainPeriod: -1}
	r.Registry.Set("xfoos.example.org", &CompiledEntry{})

	if _, err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := r.Registry.Get("xfoos.example.org"); ok {
		t.Fatal("kept a plan for a target that neither assigns to this server nor points at it")
	}
}

func TestReconcileOneCRD_KeepsServingWhileTheTargetStillPointsHere(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	pointCRDAt(crd, "srv-a")
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-b"}

	servers := twoServers()
	c := newFakeClient(crd, cfg, servers[0], servers[1]).Build()
	r := &Reconciler{Client: c, ServerName: "srv-a", Registry: NewRegistry(), EnableCRDSupport: true}

	if _, err := r.reconcileOneCRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := r.Registry.Get("foos.example.org")
	if !ok || entry.Router == nil {
		t.Fatal("srv-a dropped the plan while the CRD still pointed its conversion webhook at srv-a")
	}
}

func TestReconcileOneCRD_DrainsThenDropsAHandedOverTarget(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	pointCRDAt(crd, "srv-b")
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-b"}

	servers := twoServers()
	clock := newFakeClock()
	c := newFakeClient(crd, cfg, servers[0], servers[1]).Build()
	r := &Reconciler{Client: c, ServerName: "srv-a", Registry: NewRegistry(), EnableCRDSupport: true, now: clock.Now}
	r.Registry.Set("foos.example.org", &CompiledEntry{})

	requeue, err := r.reconcileOneCRD(context.Background(), "cfg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requeue != DefaultTargetDrainPeriod {
		t.Fatalf("requeue = %s, want the drain period %s", requeue, DefaultTargetDrainPeriod)
	}
	if _, ok := r.Registry.Get("foos.example.org"); !ok {
		t.Fatal("srv-a dropped the plan immediately after the CRD was repointed")
	}

	clock.Advance(DefaultTargetDrainPeriod + time.Second)
	if _, err := r.reconcileOneCRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := r.Registry.Get("foos.example.org"); ok {
		t.Fatal("srv-a kept the plan after the drain elapsed")
	}
}

// A deleted target cannot be pointing at anybody, so the grace this adds
// must not extend to one.
func TestReconcileOneXRD_DropsWhenTheTargetIsGone(t *testing.T) {
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-b"}

	servers := twoServers()
	c := newFakeClient(cfg, servers[0], servers[1]).Build()
	r := &Reconciler{Client: c, ServerName: "srv-a", Registry: NewRegistry(), EnableXRDSupport: true}
	r.Registry.Set("xfoos.example.org", &CompiledEntry{})

	if _, err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := r.Registry.Get("xfoos.example.org"); ok {
		t.Fatal("kept a plan for a target that no longer exists and is not assigned here")
	}
}

// Not-assigned-and-target-missing must drop rather than record a failure:
// "the XRD is gone" is not this replica's problem to report when the
// config does not belong to it.
func TestReconcileOneXRD_MissingTargetNotOursRecordsNoFailure(t *testing.T) {
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-b"}

	servers := twoServers()
	c := newFakeClient(cfg, servers[0], servers[1]).Build()
	r := &Reconciler{Client: c, ServerName: "srv-a", Registry: NewRegistry(), EnableXRDSupport: true}

	if _, err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry, ok := r.Registry.Get("xfoos.example.org"); ok {
		t.Fatalf("recorded %+v for a config this replica does not serve", entry)
	}
}

func TestPointsAtServer_Shapes(t *testing.T) {
	if xrdPointsAtServer(nil, "srv-a") {
		t.Error("a nil XRD points at nobody")
	}
	bare := establishedXRD("xfoos.example.org")
	if xrdPointsAtServer(bare, "srv-a") {
		t.Error("an XRD with no spec.conversion points at nobody")
	}
	if crdPointsAtServer(nil, "srv-a") || crdPointsAtServer(establishedCRD("foos.example.org"), "srv-a") {
		t.Error("a CRD with no spec.conversion points at nobody")
	}
	pointed := establishedXRD("xfoos.example.org")
	pointXRDAt(pointed, "srv-a")
	if !xrdPointsAtServer(pointed, "srv-a") {
		t.Error("an XRD pointed at srv-a should read as pointing at srv-a")
	}
	if xrdPointsAtServer(pointed, "srv-b") {
		t.Error("an XRD pointed at srv-a must not read as pointing at srv-b")
	}
}

// getXRDConfigFromClient reads the live config back so a test can mutate
// and re-apply it through the same fake client the reconciler reads from.
func getXRDConfigFromClient(t *testing.T, r *Reconciler, name string) *teraskyv1alpha1.XRDConversionConfig {
	t.Helper()
	var cfg teraskyv1alpha1.XRDConversionConfig
	if err := r.Get(context.Background(), types.NamespacedName{Name: name}, &cfg); err != nil {
		t.Fatalf("getting XRDConversionConfig %q: %v", name, err)
	}
	return &cfg
}

// A replica that holds no plan for the target has nothing to protect, and
// must not requeue itself every thirty seconds to remove something that is
// not there. On a replica serving none of a large fleet that would be one
// pointless timer per config.
func TestReconcileOneXRD_NoDrainWhenNothingIsHeld(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	pointXRDAt(xrd, "srv-b")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-b"}

	servers := twoServers()
	c := newFakeClient(xrd, cfg, servers[0], servers[1]).Build()
	r := &Reconciler{Client: c, ServerName: "srv-a", Registry: NewRegistry(), EnableXRDSupport: true}

	requeue, err := r.reconcileOneXRD(context.Background(), "cfg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requeue != 0 {
		t.Fatalf("requeue = %s for a target this replica never held, want none", requeue)
	}
}

// A config deleted mid-drain must not leave its deadline behind, or the
// map grows by one entry for every config that ever churned.
func TestForgetConfig_ClearsAPendingDrain(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	pointXRDAt(xrd, "srv-b")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-b"}

	servers := twoServers()
	c := newFakeClient(xrd, cfg, servers[0], servers[1]).Build()
	r := &Reconciler{Client: c, ServerName: "srv-a", Registry: NewRegistry(), EnableXRDSupport: true}
	r.Registry.Set("xfoos.example.org", &CompiledEntry{})

	if _, err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("starting the drain: %v", err)
	}
	r.mu.Lock()
	pending := len(r.drainUntil)
	r.mu.Unlock()
	if pending != 1 {
		t.Fatalf("expected one pending drain, got %d", pending)
	}

	if err := r.Delete(context.Background(), cfg); err != nil {
		t.Fatalf("deleting the config: %v", err)
	}
	if _, err := r.reconcileOneXRD(context.Background(), "cfg"); err != nil {
		t.Fatalf("reconcile after delete: %v", err)
	}
	r.mu.Lock()
	pending = len(r.drainUntil)
	r.mu.Unlock()
	if pending != 0 {
		t.Fatalf("a deleted config left %d drain deadline(s) behind", pending)
	}
}
