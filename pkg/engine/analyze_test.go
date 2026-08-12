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

package engine

import (
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestAnalyze_UncoveredFieldsRoutedBySide(t *testing.T) {
	// "a" is identical on both sides (auto-covered, never uncovered).
	// "hubOnly" exists only on the hub -> hub-side uncovered.
	// "spokeOnly" exists only on the spoke -> spoke-side uncovered.
	// "mismatched" exists on both sides but with incompatible types, so
	// it's uncovered on BOTH sides simultaneously (one diagnostic from
	// each of resolveAndBuildOps' two leftover-field loops).
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"a":          strSchema(),
		"hubOnly":    strSchema(),
		"mismatched": strSchema(),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"a":          strSchema(),
		"spokeOnly":  strSchema(),
		"mismatched": intSchema(),
	})

	source := fakeSource{versions: []VersionSchema{
		{Name: "v2", Schema: &hub, Served: true, Storage: true},
		{Name: "v1", Schema: &spoke, Served: true},
	}}

	report, err := Analyze(AnalyzeInput{
		Source:     source,
		HubVersion: "v2",
		Spokes: []RuleSet{
			{SpokeVersion: "v1"}, // no rules at all
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.SpokeReports) != 1 {
		t.Fatalf("expected 1 spoke report, got %d", len(report.SpokeReports))
	}
	sr := report.SpokeReports[0]

	wantHub := map[string]bool{"hubOnly": true, "mismatched": true}
	if len(sr.Uncovered.UncoveredHub) != len(wantHub) {
		t.Fatalf("UncoveredHub = %v, want exactly %v", sr.Uncovered.UncoveredHub, wantHub)
	}
	for _, p := range sr.Uncovered.UncoveredHub {
		if !wantHub[p] {
			t.Errorf("unexpected path %q in UncoveredHub (%v)", p, sr.Uncovered.UncoveredHub)
		}
	}

	wantSpoke := map[string]bool{"spokeOnly": true, "mismatched": true}
	if len(sr.Uncovered.UncoveredSpoke) != len(wantSpoke) {
		t.Fatalf("UncoveredSpoke = %v, want exactly %v", sr.Uncovered.UncoveredSpoke, wantSpoke)
	}
	for _, p := range sr.Uncovered.UncoveredSpoke {
		if !wantSpoke[p] {
			t.Errorf("unexpected path %q in UncoveredSpoke (%v)", p, sr.Uncovered.UncoveredSpoke)
		}
	}

	// "a" must never appear in either list — identical shape is auto-covered.
	for _, p := range append(append([]string{}, sr.Uncovered.UncoveredHub...), sr.Uncovered.UncoveredSpoke...) {
		if p == "a" {
			t.Errorf("field %q should be auto-covered (identical shape) but was reported uncovered", p)
		}
	}
}
