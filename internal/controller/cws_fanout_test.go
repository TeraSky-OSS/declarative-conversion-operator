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
