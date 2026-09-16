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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/enqueue"
)

func TestMapServerToAssignedXRDConfigs_FiltersAndBoundsFanout(t *testing.T) {
	srvA := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv-a"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: true},
	}
	srvB := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv-b"},
	}

	const assignedN = 200
	objs := []runtime.Object{srvA, srvB}
	for i := 0; i < assignedN; i++ {
		cfg := renameRuleXRDConfig(fmt.Sprintf("cfg-%03d", i), fmt.Sprintf("x%d.example.org", i))
		// Default assignment → srv-a
		objs = append(objs, cfg)
	}
	// 50 more configs explicitly pinned to srv-b — must not be enqueued for srv-a.
	for i := 0; i < 50; i++ {
		cfg := renameRuleXRDConfig(fmt.Sprintf("other-%03d", i), fmt.Sprintf("y%d.example.org", i))
		cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-b"}
		objs = append(objs, cfg)
	}

	c := newFakeClient(objs...).Build()
	reqs, err := mapServerToAssignedXRDConfigs(context.Background(), c, srvA)
	if err != nil {
		t.Fatalf("mapServerToAssignedXRDConfigs: %v", err)
	}
	if len(reqs) != assignedN {
		t.Fatalf("expected %d assigned configs enqueued for srv-a, got %d (unbounded all-configs fan-out would be %d)", assignedN, len(reqs), assignedN+50)
	}

	spread := enqueue.FanoutSpread(len(reqs), enqueue.CWSConfigEnqueueQPS)
	if spread == 0 {
		t.Fatalf("paced fan-out must spread %d requests over time at %.0f QPS", len(reqs), enqueue.CWSConfigEnqueueQPS)
	}
}

func TestMapServerTransition_LosesDefault_StillEnqueuesPriorAssignments(t *testing.T) {
	// Live list already reflects srv-a Default=false after the update;
	// without the old view, unpinned configs would not enqueue for srv-a.
	srvALive := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv-a"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: false},
	}
	srvB := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv-b"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: true},
	}
	srvAOld := srvALive.DeepCopy()
	srvAOld.Spec.Default = true

	cfg := renameRuleXRDConfig("cfg-default", "xfoos.example.org") // unpinned → follows default
	c := newFakeClient(srvALive, srvB, cfg).Build()

	reqs, err := mapServerTransitionToAssignedXRDConfigs(context.Background(), c, srvAOld, srvALive)
	if err != nil {
		t.Fatalf("transition map: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Name != "cfg-default" {
		t.Fatalf("expected cfg-default enqueued from prior default assignment, got %#v", reqs)
	}
}

func TestMapServerToAssigned_DeleteUsesDeletedObjectView(t *testing.T) {
	// Live list no longer contains srv-a; the delete event still carries it.
	srvB := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv-b"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: true},
	}
	deleted := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv-a"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: false},
	}
	cfg := renameRuleXRDConfig("cfg-explicit", "xfoos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-a"}

	c := newFakeClient(srvB, cfg).Build()
	reqs, err := mapServerToAssignedXRDConfigs(context.Background(), c, deleted)
	if err != nil {
		t.Fatalf("delete map: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Name != "cfg-explicit" {
		t.Fatalf("expected cfg-explicit enqueued for deleted server, got %#v", reqs)
	}
}

func TestMapServerToAssignedCRDConfigs_FiltersAssignment(t *testing.T) {
	srvA := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv-a"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: true},
	}
	srvB := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv-b"},
	}
	cfgA := renameRuleCRDConfig("cfg-a", "a.example.org")
	cfgB := renameRuleCRDConfig("cfg-b", "b.example.org")
	cfgB.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-b"}

	c := newFakeClient(srvA, srvB, cfgA, cfgB).Build()
	reqs, err := mapServerToAssignedCRDConfigs(context.Background(), c, srvA)
	if err != nil {
		t.Fatalf("mapServerToAssignedCRDConfigs: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Name != "cfg-a" {
		t.Fatalf("expected only cfg-a for srv-a, got %#v", reqs)
	}
}

// Adding a sharded instance moves a share of the unpinned configs onto it.
// The fan-out on that create must enqueue exactly those, and reach them
// through the same paced handler as every other CWS-driven fan-out —
// rebalancing a fleet is the largest burst this watch ever produces, so it
// is the one that most needs the pacing.
func TestMapServerToAssignedXRDConfigs_ShardedFanoutIsBoundedAndPaced(t *testing.T) {
	poolA := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "srv-a"}}
	poolA.Spec.Default = true
	poolA.Spec.Sharding = &teraskyv1alpha1.ShardingSpec{}
	poolB := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "srv-b"}}
	poolB.Spec.Sharding = &teraskyv1alpha1.ShardingSpec{}

	const total = 400
	objs := []runtime.Object{poolA, poolB}
	for i := 0; i < total; i++ {
		objs = append(objs, renameRuleXRDConfig(fmt.Sprintf("cfg-%03d", i), fmt.Sprintf("x%d.example.org", i)))
	}
	// Pinned configs are never rebalanced, whatever the pool does.
	pinned := renameRuleXRDConfig("pinned", "pinned.example.org")
	pinned.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-a"}
	objs = append(objs, pinned)

	c := newFakeClient(objs...).Build()
	reqs, err := mapServerToAssignedXRDConfigs(context.Background(), c, poolB)
	if err != nil {
		t.Fatalf("mapServerToAssignedXRDConfigs: %v", err)
	}

	// Roughly half of the unpinned configs, and none of the pinned one.
	if len(reqs) == 0 || len(reqs) == total+1 {
		t.Fatalf("srv-b was enqueued %d of %d configs; a shard should take a share, not none or all", len(reqs), total+1)
	}
	if len(reqs) < total/4 || len(reqs) > (3*total)/4 {
		t.Errorf("srv-b was enqueued %d of %d unpinned configs, want roughly half", len(reqs), total)
	}
	for _, r := range reqs {
		if r.Name == "pinned" {
			t.Fatal("a config pinned to srv-a was enqueued as belonging to srv-b")
		}
	}

	if spread := enqueue.FanoutSpread(len(reqs), enqueue.CWSConfigEnqueueQPS); spread == 0 {
		t.Fatalf("a rebalance of %d configs must be paced, not dumped into the workqueue at once", len(reqs))
	}
}

// Removing a sharded instance has to re-reconcile the configs it used to
// hold, computed from the pre-delete view — after the object is gone there
// is nothing left to derive them from.
func TestMapServerTransition_ShardRemoval_EnqueuesItsFormerConfigs(t *testing.T) {
	poolA := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "srv-a"}}
	poolA.Spec.Default = true
	poolA.Spec.Sharding = &teraskyv1alpha1.ShardingSpec{}
	poolB := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "srv-b"}}
	poolB.Spec.Sharding = &teraskyv1alpha1.ShardingSpec{}

	const total = 200
	objs := []runtime.Object{poolA}
	var names []string
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("cfg-%03d", i)
		names = append(names, name)
		objs = append(objs, renameRuleXRDConfig(name, fmt.Sprintf("x%d.example.org", i)))
	}

	// The live list no longer contains srv-b; only the deleted object's own
	// view can say what it used to serve.
	c := newFakeClient(objs...).Build()
	reqs, err := mapXRDConfigsForServerViews(context.Background(), c, "srv-b", poolB)
	if err != nil {
		t.Fatalf("mapXRDConfigsForServerViews: %v", err)
	}
	if len(reqs) == 0 {
		t.Fatal("deleting a pool member enqueued nothing; the configs it held would keep pointing at a Service that no longer exists")
	}
	if len(reqs) >= len(names) {
		t.Fatalf("enqueued %d of %d configs for a removed shard, want only its own share", len(reqs), len(names))
	}
}
