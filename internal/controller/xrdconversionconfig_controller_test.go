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
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

func xrdWithServedVersions(served ...bool) *unstructured.Unstructured {
	versions := make([]any, 0, len(served))
	for i, s := range served {
		versions = append(versions, map[string]any{"name": "v" + string(rune('1'+i)), "served": s})
	}
	return &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"versions": versions}}}
}

func TestCountServedVersions(t *testing.T) {
	// This is the load-bearing check behind the "block deletion unless the
	// XRD serves at most one version" safety rule — getting it wrong in
	// either direction either strands a working config or lets an unsafe
	// revert through silently.
	if n := countServedVersions(xrdWithServedVersions(true, true, false)); n != 2 {
		t.Fatalf("expected 2 served versions, got %d", n)
	}
	if n := countServedVersions(xrdWithServedVersions(true)); n != 1 {
		t.Fatalf("expected 1 served version, got %d", n)
	}
	if n := countServedVersions(&unstructured.Unstructured{Object: map[string]any{}}); n != 0 {
		t.Fatalf("expected 0 served versions for an XRD with no spec.versions, got %d", n)
	}
}

func TestIsServerReady(t *testing.T) {
	notReady := &teraskyv1alpha1.ConversionWebhookServer{}
	if isServerReady(notReady) {
		t.Fatalf("a server with zero conditions and zero ready replicas must never be reported ready")
	}

	ready := &teraskyv1alpha1.ConversionWebhookServer{
		Status: teraskyv1alpha1.ConversionWebhookServerStatus{
			ReadyReplicas: 2,
			Conditions: []metav1.Condition{
				{Type: teraskyv1alpha1.CWSConditionAvailable, Status: metav1.ConditionTrue},
				{Type: teraskyv1alpha1.CWSConditionCertificateReady, Status: metav1.ConditionTrue},
				{Type: teraskyv1alpha1.CWSConditionServiceReady, Status: metav1.ConditionTrue},
			},
		},
	}
	if !isServerReady(ready) {
		t.Fatalf("expected a server with ReadyReplicas>0 and all three conditions True to be ready")
	}

	partiallyReady := ready.DeepCopy()
	partiallyReady.Status.Conditions[1].Status = metav1.ConditionFalse
	if isServerReady(partiallyReady) {
		t.Fatalf("a server missing CertificateReady must never be reported ready, even with ready pods")
	}
}

func TestPhasePending_ReportsStaleOnceApplied(t *testing.T) {
	if got := phasePending(false); got != teraskyv1alpha1.PhasePending {
		t.Fatalf("expected Pending for a never-applied config, got %s", got)
	}
	if got := phasePending(true); got != teraskyv1alpha1.PhaseStale {
		t.Fatalf("expected Stale once a config has been Applied before, got %s", got)
	}
}

func TestPopulateSpokeStatuses_CopiesUncoveredFields(t *testing.T) {
	cfg := &teraskyv1alpha1.XRDConversionConfig{}
	report := engine.AnalyzeReport{
		SpokeReports: []engine.SpokeReport{{
			Version:  "v1",
			Lossless: engine.LosslessVerdict{HubToSpoke: false, SpokeToHub: false},
			Uncovered: engine.FieldCoverage{
				UncoveredHub:   []string{"hubOnly"},
				UncoveredSpoke: []string{"spokeOnly"},
			},
			Errors: []engine.Diagnostic{{
				Severity: engine.SeverityError,
				Message:  `hub field "hubOnly" is not covered by any rule and has no identical counterpart in the spoke schema`,
			}},
		}},
	}
	populateSpokeStatuses(cfg, report)
	if len(cfg.Status.SpokeStatuses) != 1 {
		t.Fatalf("expected 1 spoke status, got %d", len(cfg.Status.SpokeStatuses))
	}
	s := cfg.Status.SpokeStatuses[0]
	if !reflect.DeepEqual(s.FieldsUncoveredHub, []string{"hubOnly"}) {
		t.Fatalf("FieldsUncoveredHub = %#v, want [hubOnly]", s.FieldsUncoveredHub)
	}
	if !reflect.DeepEqual(s.FieldsUncoveredSpoke, []string{"spokeOnly"}) {
		t.Fatalf("FieldsUncoveredSpoke = %#v, want [spokeOnly]", s.FieldsUncoveredSpoke)
	}
}
