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
	"strings"
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func mustCompile(t *testing.T, rs RuleSet, hub, spoke *extv1.JSONSchemaProps) *Plan {
	t.Helper()
	plan, diags, err := Compile(rs, hub, spoke)
	if err != nil {
		t.Fatalf("unexpected compile error: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected compile errors: %v", errs)
	}
	return plan
}

// TestConstant_InjectsFixedValue covers the normal Constant case: a spoke-only
// bookkeeping field is forced to a fixed string on hub→spoke.
func TestConstant_InjectsFixedValue(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"schemaVersion": strSchema()})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyConstant, AcknowledgeLossy: true, Reason: "bookkeeping",
			Params: ConstantParams{Path: ParsePath("schemaVersion"), ExistsOn: SideSpoke, Value: "v2"}},
	}}
	plan := mustCompile(t, rs, &hub, &spoke)

	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: map[string]any{}})
	if err != nil {
		t.Fatalf("convert h2s: %v", err)
	}
	if out["schemaVersion"] != "v2" {
		t.Fatalf("expected schemaVersion=v2, got %#v", out["schemaVersion"])
	}
}

// TestConstant_NumericJSONTypePreserved is the Constant-specific edge around
// type handling: encoding/json decodes whole numbers as float64, and Constant
// injects that decoded value verbatim (it does not run TypeCoerce).
func TestConstant_NumericJSONTypePreserved(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"schemaVersion": intSchema()})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyConstant, AcknowledgeLossy: true, Reason: "bookkeeping",
			Params: ConstantParams{Path: ParsePath("schemaVersion"), ExistsOn: SideSpoke, Value: float64(2)}},
	}}
	plan := mustCompile(t, rs, &hub, &spoke)

	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: map[string]any{}})
	if err != nil {
		t.Fatalf("convert h2s: %v", err)
	}
	if _, ok := out["schemaVersion"].(float64); !ok {
		t.Fatalf("expected float64 constant (JSON number), got %T (%#v)", out["schemaVersion"], out["schemaVersion"])
	}
	if out["schemaVersion"] != float64(2) {
		t.Fatalf("expected schemaVersion=2, got %#v", out["schemaVersion"])
	}
}

// TestDefaultValue_InjectsWhenAbsent covers the normal DefaultValue case:
// converting toward the side that declares the field injects the default when
// the source side never had a real value.
func TestDefaultValue_InjectsWhenAbsent(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"computeUnits": intSchema()})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyDefaultValue, AcknowledgeLossy: true, Reason: "spoke-only",
			Params: DefaultValueParams{Path: ParsePath("computeUnits"), ExistsOn: SideSpoke, Default: float64(1)}},
	}}
	plan := mustCompile(t, rs, &hub, &spoke)

	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: map[string]any{}})
	if err != nil {
		t.Fatalf("convert h2s: %v", err)
	}
	if out["computeUnits"] != float64(1) {
		t.Fatalf("expected computeUnits=1, got %#v", out["computeUnits"])
	}
}

// TestDefaultValue_DropsWhenSourcePresent is the DefaultValue edge: converting
// away from the side that declares the field drops a real present value
// (acknowledged-lossy direction).
func TestDefaultValue_DropsWhenSourcePresent(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"computeUnits": intSchema()})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyDefaultValue, AcknowledgeLossy: true, Reason: "spoke-only",
			Params: DefaultValueParams{Path: ParsePath("computeUnits"), ExistsOn: SideSpoke, Default: float64(1)}},
	}}
	plan := mustCompile(t, rs, &hub, &spoke)

	out, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: map[string]any{"computeUnits": float64(9)}})
	if err != nil {
		t.Fatalf("convert s2h: %v", err)
	}
	if _, ok := out["computeUnits"]; ok {
		t.Fatalf("expected computeUnits to be dropped on spoke→hub, got %#v", out["computeUnits"])
	}
}

// TestToLabel_RestoreOnReverse covers the normal ToLabel case: a hub string
// is stashed into a spoke label with String serialization and restored on
// the reverse direction.
func TestToLabel_RestoreOnReverse(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyToLabel, Params: ToMetadataParams{
			HubPath: ParsePath("tier"), Key: "tier", Serialization: "String", RestoreOnReverse: true,
		}},
	}}
	plan := mustCompile(t, rs, &hub, &spoke)

	in := map[string]any{"tier": "gold", "metadata": map[string]any{"name": "x"}}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
	if err != nil {
		t.Fatalf("convert h2s: %v", err)
	}
	md, _ := out["metadata"].(map[string]any)
	labels, _ := md["labels"].(map[string]any)
	if labels["tier"] != "gold" {
		t.Fatalf("expected labels.tier=gold, got metadata=%#v", md)
	}
	if _, ok := out["tier"]; ok {
		t.Fatalf("spoke object should not keep hub field tier")
	}

	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: out})
	if err != nil {
		t.Fatalf("convert s2h: %v", err)
	}
	if back["tier"] != "gold" {
		t.Fatalf("expected restored tier=gold, got %#v", back["tier"])
	}
	backMd, _ := back["metadata"].(map[string]any)
	if backLabels, ok := backMd["labels"].(map[string]any); ok {
		if _, exists := backLabels["tier"]; exists {
			t.Fatalf("bookkeeping label should have been removed on restore, got %#v", backMd)
		}
	}
}

// TestToLabel_JSONSerializationRejectedAtConvert verifies label writes fail
// closed when JSON serialization produces a quoted value that is not a valid
// Kubernetes label value. Prefer serialization=String for ToLabel/FromLabel.
func TestToLabel_JSONSerializationRejectedAtConvert(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyToLabel, Params: ToMetadataParams{
			HubPath: ParsePath("tier"), Key: "tier", RestoreOnReverse: true,
			// Serialization defaults to JSON.
		}},
	}}
	plan := mustCompile(t, rs, &hub, &spoke)

	in := map[string]any{"tier": "gold", "metadata": map[string]any{"name": "x"}}
	_, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
	if err == nil {
		t.Fatal("expected convert to reject JSON-quoted label value")
	}
	if !strings.Contains(err.Error(), "label value") {
		t.Fatalf("expected label-value error, got: %v", err)
	}
}

// TestMapToFields_RoundTrip covers the normal MapToFields case: a hub map
// expands into sibling spoke fields and collapses back.
func TestMapToFields_RoundTrip(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"tags": openMapSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"envTag": strSchema(), "teamTag": strSchema(),
	})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyMapToFields, Params: MapToFieldsParams{
			HubMapPath: ParsePath("tags"),
			SpokePaths: []FieldPath{ParsePath("envTag"), ParsePath("teamTag")},
			KeyNames:   map[string]string{"envTag": "env", "teamTag": "team"},
		}},
	}}
	plan := mustCompile(t, rs, &hub, &spoke)

	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: map[string]any{
		"tags": map[string]any{"env": "prod", "team": "platform"},
	}})
	if err != nil {
		t.Fatalf("convert h2s: %v", err)
	}
	if out["envTag"] != "prod" || out["teamTag"] != "platform" {
		t.Fatalf("unexpected spoke fields: %#v", out)
	}

	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: out})
	if err != nil {
		t.Fatalf("convert s2h: %v", err)
	}
	tags, ok := back["tags"].(map[string]any)
	if !ok || tags["env"] != "prod" || tags["team"] != "platform" {
		t.Fatalf("round trip mismatch: %#v", back["tags"])
	}
}

// TestMapToFields_EmptyMap is the MapToFields edge: an empty hub map expands
// to no spoke fields (and does not error).
func TestMapToFields_EmptyMap(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"tags": openMapSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"envTag": strSchema(), "teamTag": strSchema(),
	})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyMapToFields, Params: MapToFieldsParams{
			HubMapPath: ParsePath("tags"),
			SpokePaths: []FieldPath{ParsePath("envTag"), ParsePath("teamTag")},
			KeyNames:   map[string]string{"envTag": "env", "teamTag": "team"},
		}},
	}}
	plan := mustCompile(t, rs, &hub, &spoke)

	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: map[string]any{
		"tags": map[string]any{},
	}})
	if err != nil {
		t.Fatalf("convert h2s: %v", err)
	}
	if _, ok := out["envTag"]; ok {
		t.Fatalf("expected envTag absent for empty map, got %#v", out["envTag"])
	}
	if _, ok := out["teamTag"]; ok {
		t.Fatalf("expected teamTag absent for empty map, got %#v", out["teamTag"])
	}
	if _, ok := out["tags"]; ok {
		t.Fatalf("expected hub map not to leak through, got %#v", out["tags"])
	}
}
