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
	"reflect"
	"testing"

	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/terasky-oss/declarative-conversion-operator/internal/servedtargets"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

func newTestPublisher(reg *Registry) *TargetPublisher {
	return &TargetPublisher{
		Client:     newFakeClient().Build(),
		Registry:   reg,
		ServerName: "srv",
		Namespace:  "dco-system",
		PodName:    "srv-webhook-server-abc123",
		PodUID:     types.UID("uid-1"),
	}
}

func readLease(t *testing.T, p *TargetPublisher) *coordinationv1.Lease {
	t.Helper()
	var l coordinationv1.Lease
	key := types.NamespacedName{Name: servedtargets.LeaseName(p.PodName), Namespace: p.Namespace}
	if err := p.Client.Get(context.Background(), key, &l); err != nil {
		t.Fatalf("getting the published lease: %v", err)
	}
	return &l
}

func TestPublish_CreatesThenUpdates(t *testing.T) {
	reg := NewRegistry()
	reg.Set("xfoos.example.org", &CompiledEntry{Router: &engine.Router{Hub: "v2"}})
	p := newTestPublisher(reg)

	if err := p.Publish(context.Background()); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	l := readLease(t, p)
	if l.Labels[servedtargets.WebhookServerLabel] != "srv" {
		t.Fatalf("lease labels = %v; without the server label the operator's informer never sees it", l.Labels)
	}
	if l.Labels[managedByLabel] != managedByValue {
		t.Fatalf("lease labels = %v; without the managed-by label the manager's scoped Lease cache never sees it", l.Labels)
	}
	if len(l.OwnerReferences) != 1 || l.OwnerReferences[0].UID != "uid-1" {
		t.Fatalf("owner references = %v; without one, a replaced replica leaves a Lease behind claiming to serve things", l.OwnerReferences)
	}
	if l.Spec.RenewTime == nil {
		t.Fatal("renewTime is unset, so readers cannot tell a wedged replica from a working one")
	}
	got, err := servedtargets.Decode(l.Annotations[servedtargets.TargetsAnnotation])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"xfoos.example.org"}) {
		t.Fatalf("published %v, want the one servable target", got)
	}

	// Second publish must update in place rather than fail on an
	// already-existing object.
	reg.Set("bars.example.org", &CompiledEntry{Router: &engine.Router{Hub: "v2"}})
	if err := p.Publish(context.Background()); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	got, _ = servedtargets.Decode(readLease(t, p).Annotations[servedtargets.TargetsAnnotation])
	if !reflect.DeepEqual(got, []string{"bars.example.org", "xfoos.example.org"}) {
		t.Fatalf("published %v after the second target compiled", got)
	}
}

// An error-only registry entry has no Router: the replica knows about the
// target and cannot serve it. Publishing it would tell the operator the
// handover is safe when it is not.
func TestPublish_ExcludesEntriesWithNoCompiledPlan(t *testing.T) {
	reg := NewRegistry()
	reg.Set("good.example.org", &CompiledEntry{Router: &engine.Router{Hub: "v2"}})
	reg.RecordError("broken.example.org", "analysis failed")
	p := newTestPublisher(reg)

	if err := p.Publish(context.Background()); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, _ := servedtargets.Decode(readLease(t, p).Annotations[servedtargets.TargetsAnnotation])
	if !reflect.DeepEqual(got, []string{"good.example.org"}) {
		t.Fatalf("published %v, want only the target with a compiled plan", got)
	}
}

func TestPublisher_DisabledWithoutAnIdentity(t *testing.T) {
	p := newTestPublisher(NewRegistry())
	p.PodName = ""
	if p.Enabled() {
		t.Fatal("a publisher with no pod name must be disabled: its Lease could not be attributed to a replica")
	}
	// And must be inert rather than panicking or writing something wrong.
	if err := p.Publish(context.Background()); err != nil {
		t.Fatalf("a disabled publisher must be a no-op, got %v", err)
	}
	var l coordinationv1.Lease
	err := p.Client.Get(context.Background(), types.NamespacedName{Name: servedtargets.LeaseName(""), Namespace: p.Namespace}, &l)
	if err == nil {
		t.Fatal("a disabled publisher wrote a Lease")
	}
}

func TestPublisher_NotifyIsNonBlockingAndCoalescing(t *testing.T) {
	p := newTestPublisher(NewRegistry())
	p.Init()
	// More notifications than the channel can hold. A blocking send here
	// would deadlock the reconcile loop that calls it.
	for i := 0; i < 100; i++ {
		p.Notify()
	}
	if got := len(p.notify); got != 1 {
		t.Fatalf("notify channel holds %d tokens, want 1 — a burst must coalesce", got)
	}
}

func TestPublisher_NilIsInert(t *testing.T) {
	var p *TargetPublisher
	p.Notify() // must not panic: the Reconciler calls this unconditionally.
	if p.Enabled() {
		t.Fatal("a nil publisher must not report itself enabled")
	}
}
