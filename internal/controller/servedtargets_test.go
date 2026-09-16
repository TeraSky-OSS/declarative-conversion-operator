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
	"reflect"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/servedtargets"
)

func replicaLease(server, pod, namespace string, targets []string) *coordinationv1.Lease {
	value, _ := servedtargets.Encode(targets)
	now := metav1.NewMicroTime(time.Now())
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      servedtargets.LeaseName(pod),
			Namespace: namespace,
			Labels: map[string]string{
				servedtargets.WebhookServerLabel: server,
				ManagedByLabel:                   ManagedByValue,
			},
			Annotations: map[string]string{servedtargets.TargetsAnnotation: value},
		},
		Spec: coordinationv1.LeaseSpec{RenewTime: &now},
	}
}

func TestReadServedTargets_IntersectsAcrossReplicas(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "srv"}}
	server.Spec.Namespace = "dco-system"

	c := newFakeClient(
		replicaLease("srv", "pod-1", "dco-system", []string{"a", "b"}),
		replicaLease("srv", "pod-2", "dco-system", []string{"a"}),
		// Another instance's replica, and one in another namespace.
		replicaLease("other", "pod-3", "dco-system", []string{"zz"}),
		replicaLease("srv", "pod-4", "elsewhere", []string{"zz"}),
	).Build()

	served, reporting, truncated, err := readServedTargets(context.Background(), c, server, "operator-ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if truncated {
		t.Fatal("nothing was truncated")
	}
	if reporting != 2 {
		t.Fatalf("reporting = %d, want 2 — leases for other instances or namespaces must not be counted", reporting)
	}
	if !reflect.DeepEqual(served, []string{"a"}) {
		t.Fatalf("served = %v, want the intersection [a]", served)
	}
}

// spec.namespace is optional; an instance that omits it lives in the
// operator's own namespace, and looking in the wrong one would report
// every instance as publishing nothing.
func TestReadServedTargets_FallsBackToTheOperatorNamespace(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "srv"}}
	c := newFakeClient(replicaLease("srv", "pod-1", "operator-ns", []string{"a"})).Build()

	served, reporting, _, err := readServedTargets(context.Background(), c, server, "operator-ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reporting != 1 || !reflect.DeepEqual(served, []string{"a"}) {
		t.Fatalf("served = %v, reporting = %d", served, reporting)
	}
}

func TestCanServeTarget(t *testing.T) {
	const target = "xfoos.example.org"
	cases := []struct {
		name          string
		served        []string
		reporting     int32
		readyReplicas int32
		truncated     bool
		wantOK        bool
		wantReason    string
	}{{
		// Nothing published at all: a fleet mid-upgrade, or an instance in
		// a namespace whose Lease Role was never created. Blocking every
		// move forever would be a regression against every previous
		// release, so the move proceeds and says it was not verified.
		name:      "nothing published proceeds unverified",
		reporting: 0, readyReplicas: 2,
		wantOK: true, wantReason: "HandoverUnverified",
	}, {
		name:   "all reporting replicas serve it",
		served: []string{"afoos.example.org", target}, reporting: 2, readyReplicas: 2,
		wantOK: true, wantReason: "HandoverReady",
	}, {
		name:   "not yet compiled anywhere",
		served: []string{"afoos.example.org"}, reporting: 2, readyReplicas: 2,
		wantOK: false, wantReason: "HandoverPending",
	}, {
		// The intersection says yes, but a ready replica has not spoken.
		// Its silence is not agreement.
		name:   "a ready replica has not reported",
		served: []string{target}, reporting: 1, readyReplicas: 3,
		wantOK: false, wantReason: "HandoverPending",
	}, {
		name:      "a replica could not state its set",
		truncated: true, reporting: 2, readyReplicas: 2,
		wantOK: false, wantReason: "HandoverUnknown",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := canServeTarget(tc.served, tc.reporting, tc.readyReplicas, tc.truncated, target, "srv-b")
			if v.OK != tc.wantOK || v.Reason != tc.wantReason {
				t.Fatalf("got OK=%v reason=%q, want OK=%v reason=%q (message: %s)", v.OK, v.Reason, tc.wantOK, tc.wantReason, v.Message)
			}
			if v.Message == "" {
				t.Error("every verdict needs a message: it is what lands on the config's status condition")
			}
		})
	}
}

// The ConversionWebhookServer health gate runs before this, so it should
// not be reachable — but "no ready replica" must never read as "nothing
// objects". Repointing a target at an instance with nothing running is the
// outage the whole sequence exists to avoid.
func TestCanServeTarget_NoReadyReplicasNeverApproves(t *testing.T) {
	v := canServeTarget(nil, 0, 0, false, "xfoos.example.org", "srv-b")
	if v.OK {
		t.Fatalf("approved a handover to an instance with no ready replicas: %+v", v)
	}
	if v.Reason != "HandoverPending" {
		t.Fatalf("reason = %q, want HandoverPending", v.Reason)
	}
}
