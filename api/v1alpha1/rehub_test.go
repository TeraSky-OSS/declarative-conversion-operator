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

package v1alpha1

import (
	"testing"
)

func TestInvertRule_FieldRename(t *testing.T) {
	t.Parallel()
	in := ConversionRule{
		Strategy:    StrategyFieldRename,
		FieldRename: &FieldRenameParams{HubPath: "spec.size", SpokePath: "spec.capacity"},
	}
	out, err := InvertRule(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Strategy != StrategyFieldRename || out.FieldRename == nil {
		t.Fatalf("unexpected: %+v", out)
	}
	if out.FieldRename.HubPath != "spec.capacity" || out.FieldRename.SpokePath != "spec.size" {
		t.Fatalf("paths not swapped: %+v", out.FieldRename)
	}
}

func TestInvertRule_ToAnnotation(t *testing.T) {
	t.Parallel()
	in := ConversionRule{
		Strategy: StrategyToAnnotation,
		ToAnnotation: &ToMetadataParams{
			HubPath: "spec.description", Key: "x/desc", Serialization: "JSON", RestoreOnReverse: true,
		},
	}
	out, err := InvertRule(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Strategy != StrategyFromAnnotation || out.FromAnnotation == nil {
		t.Fatalf("expected FromAnnotation, got %+v", out)
	}
	if out.FromAnnotation.SpokePath != "spec.description" || !out.FromAnnotation.StashOnReverse {
		t.Fatalf("unexpected params: %+v", out.FromAnnotation)
	}
	back, err := InvertRule(out)
	if err != nil {
		t.Fatal(err)
	}
	if back.Strategy != StrategyToAnnotation || back.ToAnnotation.HubPath != "spec.description" || !back.ToAnnotation.RestoreOnReverse {
		t.Fatalf("round-trip invert failed: %+v", back)
	}
}

func TestInvertRule_ScalarToObject(t *testing.T) {
	t.Parallel()
	in := ConversionRule{
		Strategy:       StrategyScalarToObject,
		ScalarToObject: &ScalarToObjectParams{HubPath: "spec.n", SpokePath: "spec.obj", Key: "count"},
	}
	out, err := InvertRule(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Strategy != StrategyObjectToScalar || out.ObjectToScalar == nil {
		t.Fatalf("expected ObjectToScalar, got %+v", out)
	}
	if out.ObjectToScalar.HubPath != "spec.obj" || out.ObjectToScalar.SpokePath != "spec.n" || out.ObjectToScalar.Key != "count" {
		t.Fatalf("unexpected: %+v", out.ObjectToScalar)
	}
}

func TestComposeSpokeRules_LiftsAutoCoveredRename(t *testing.T) {
	t.Parallel()
	// Stage 4→5: v1 had capacity↔size; widgetName was auto-covered vs hub v2.
	// Promote map: widgetName → name.
	pathMap := HubPathMap{"spec.widgetName": "spec.name"}
	rules := []ConversionRule{{
		Strategy:    StrategyFieldRename,
		FieldRename: &FieldRenameParams{HubPath: "spec.capacity", SpokePath: "spec.size"},
	}}
	spokeLeaves := map[string]bool{
		"spec.capacity":   true,
		"spec.size":       true,
		"spec.widgetName": true,
	}
	out, err := ComposeSpokeRules(rules, pathMap, spokeLeaves)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 rules (rewritten + lifted), got %d: %+v", len(out), out)
	}
	foundLift := false
	for _, r := range out {
		if r.Strategy == StrategyFieldRename && r.FieldRename != nil &&
			r.FieldRename.HubPath == "spec.name" && r.FieldRename.SpokePath == "spec.widgetName" {
			foundLift = true
		}
	}
	if !foundLift {
		t.Fatalf("expected lifted FieldRename name→widgetName, got %+v", out)
	}
}

func TestPathMapFromPromoteRules(t *testing.T) {
	t.Parallel()
	m, err := PathMapFromPromoteRules([]ConversionRule{{
		Strategy:    StrategyFieldRename,
		FieldRename: &FieldRenameParams{HubPath: "spec.widgetName", SpokePath: "spec.name"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if m["spec.widgetName"] != "spec.name" {
		t.Fatalf("unexpected map: %+v", m)
	}
}

func TestRehubSpokes_PromoteV2(t *testing.T) {
	t.Parallel()
	// Stage 02 → 03: hub v1, spoke v2 size→capacity; promote v2.
	hub, spokes, err := RehubSpokes("v1", "v2", []SpokeVersionRules{{
		Version: "v2",
		Rules: []ConversionRule{{
			Strategy:    StrategyFieldRename,
			FieldRename: &FieldRenameParams{HubPath: "spec.size", SpokePath: "spec.capacity"},
		}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hub != "v2" {
		t.Fatalf("hub=%s", hub)
	}
	if len(spokes) != 1 || spokes[0].Version != "v1" {
		t.Fatalf("spokes=%+v", spokes)
	}
	r := spokes[0].Rules[0]
	if r.FieldRename == nil || r.FieldRename.HubPath != "spec.capacity" || r.FieldRename.SpokePath != "spec.size" {
		t.Fatalf("expected capacity→size, got %+v", r)
	}
}

func TestRehubSpokes_PromoteV3ComposesV1(t *testing.T) {
	t.Parallel()
	leaves := map[string]map[string]bool{
		"v1": {"spec.widgetName": true, "spec.size": true},
	}

	hub, spokes, err := RehubSpokes("v2", "v3", []SpokeVersionRules{
		{
			Version: "v1",
			Rules: []ConversionRule{{
				Strategy:    StrategyFieldRename,
				FieldRename: &FieldRenameParams{HubPath: "spec.capacity", SpokePath: "spec.size"},
			}},
		},
		{
			Version: "v3",
			Rules: []ConversionRule{{
				Strategy:    StrategyFieldRename,
				FieldRename: &FieldRenameParams{HubPath: "spec.widgetName", SpokePath: "spec.name"},
			}},
		},
	}, leaves)
	if err != nil {
		t.Fatal(err)
	}
	if hub != "v3" {
		t.Fatalf("hub=%s", hub)
	}
	byVer := map[string]SpokeVersionRules{}
	for _, s := range spokes {
		byVer[s.Version] = s
	}
	v2 := byVer["v2"]
	if len(v2.Rules) != 1 || v2.Rules[0].FieldRename.HubPath != "spec.name" || v2.Rules[0].FieldRename.SpokePath != "spec.widgetName" {
		t.Fatalf("v2 invert: %+v", v2.Rules)
	}
	v1 := byVer["v1"]
	if len(v1.Rules) != 2 {
		t.Fatalf("v1 expected 2 rules, got %d: %+v", len(v1.Rules), v1.Rules)
	}
	foundName, foundCap := false, false
	for _, r := range v1.Rules {
		if r.FieldRename == nil {
			continue
		}
		if r.FieldRename.HubPath == "spec.name" && r.FieldRename.SpokePath == "spec.widgetName" {
			foundName = true
		}
		if r.FieldRename.HubPath == "spec.capacity" && r.FieldRename.SpokePath == "spec.size" {
			foundCap = true
		}
	}
	if !foundName || !foundCap {
		t.Fatalf("v1 compose missing expected renames: %+v", v1.Rules)
	}
}

func TestRehubSpokes_ToNotSpoke(t *testing.T) {
	t.Parallel()
	_, _, err := RehubSpokes("v1", "v9", []SpokeVersionRules{{
		Version: "v2",
		Rules:   []ConversionRule{{Strategy: StrategyFieldRename, FieldRename: &FieldRenameParams{HubPath: "a", SpokePath: "b"}}},
	}}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRehubSpokes_JSONPatchPromoteFails(t *testing.T) {
	t.Parallel()
	_, _, err := RehubSpokes("v1", "v2", []SpokeVersionRules{{
		Version: "v2",
		Rules: []ConversionRule{{
			Strategy:  StrategyJSONPatch,
			JSONPatch: &JSONPatchParams{HubToSpoke: []JSONPatchOp{{Op: "remove", Path: "/spec/x"}}},
		}},
	}}, nil)
	if err == nil {
		t.Fatal("expected JSONPatch promote to fail closed")
	}
}
