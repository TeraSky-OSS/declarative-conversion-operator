/*
Copyright 2026 The xrd-conversion-operator Authors.

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
	"reflect"
	"strings"
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestFieldRename_LosslessRoundTrip(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"storageGB": strSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"storageSize": strSchema()})
	rs := RuleSet{HubVersion: "v2", SpokeVersion: "v1", Rules: []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("storageGB"), SpokePath: ParsePath("storageSize")}},
	}}
	plan, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	in := map[string]any{"storageGB": "100"}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
	if err != nil {
		t.Fatalf("convert h2s: %v", err)
	}
	if out["storageSize"] != "100" {
		t.Fatalf("expected storageSize=100, got %v", out)
	}
	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: out})
	if err != nil {
		t.Fatalf("convert s2h: %v", err)
	}
	if back["storageGB"] != "100" {
		t.Fatalf("round trip mismatch: %v", back)
	}
}

func TestUncoveredField_FailsClosedByDefault(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"a": strSchema(), "b": strSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"a": strSchema()})
	rs := RuleSet{HubVersion: "v2", SpokeVersion: "v1"} // no rules at all; "b" is uncovered
	_, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	errs := diagMessages(diags, SeverityError)
	if len(errs) == 0 {
		t.Fatalf("expected an uncovered-field error, got none")
	}
	// "a" is identical on both sides and should NOT be flagged.
	for _, e := range errs {
		if strings.Contains(e, "\"a\"") {
			t.Fatalf("field 'a' should be auto-covered (identical shape), got error: %s", e)
		}
	}
}

func TestUncoveredField_WarnPolicyDowngrades(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"a": strSchema(), "b": strSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"a": strSchema()})
	rs := RuleSet{HubVersion: "v2", SpokeVersion: "v1", UnmappedFieldPolicy: UnmappedFieldPolicyWarn}
	_, diags, _ := Compile(rs, &hub, &spoke)
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("expected no errors under Warn policy, got: %v", errs)
	}
	if warns := diagMessages(diags, SeverityWarning); len(warns) == 0 {
		t.Fatalf("expected a warning under Warn policy, got none")
	}
}

func TestScalarToObject_LosslessAndLossy(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"version": strSchema()})

	// Lossless: spoke object has exactly the wrapped key, nothing else.
	spokeLossless := objSchema(map[string]extv1.JSONSchemaProps{
		"version": objSchema(map[string]extv1.JSONSchemaProps{"major": strSchema()}),
	})
	rsLossless := RuleSet{Rules: []Rule{
		{Strategy: StrategyScalarToObject, Params: ScalarToObjectParams{HubPath: ParsePath("version"), SpokePath: ParsePath("version"), Key: "major"}},
	}}
	_, diags, err := Compile(rsLossless, &hub, &spokeLossless)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors for lossless case: %v", errs)
	}

	// Lossy: spoke object has an extra key ("channel") that gets dropped
	// when converting spoke->hub, and acknowledgeLossy is not set.
	spokeLossy := objSchema(map[string]extv1.JSONSchemaProps{
		"version": objSchema(map[string]extv1.JSONSchemaProps{"major": strSchema(), "channel": strSchema()}),
	})
	rsLossy := RuleSet{Rules: []Rule{
		{Strategy: StrategyScalarToObject, Params: ScalarToObjectParams{HubPath: ParsePath("version"), SpokePath: ParsePath("version"), Key: "major"}},
	}}
	_, diags2, _ := Compile(rsLossy, &hub, &spokeLossy)
	if errs := diagMessages(diags2, SeverityError); len(errs) == 0 {
		t.Fatalf("expected a lossy-without-acknowledgement error")
	}

	// Same lossy case, but acknowledged: should compile clean and convert correctly.
	rsAck := RuleSet{Rules: []Rule{
		{Strategy: StrategyScalarToObject, AcknowledgeLossy: true, Reason: "channel not representable in hub",
			Params: ScalarToObjectParams{HubPath: ParsePath("version"), SpokePath: ParsePath("version"), Key: "major", DefaultsForOtherKeys: map[string]any{"channel": "stable"}}},
	}}
	plan, diags3, _ := Compile(rsAck, &hub, &spokeLossy)
	if errs := diagMessages(diags3, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors for acknowledged case: %v", errs)
	}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: map[string]any{"version": "15"}})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	versionObj, ok := out["version"].(map[string]any)
	if !ok || versionObj["major"] != "15" || versionObj["channel"] != "stable" {
		t.Fatalf("unexpected wrapped object: %#v", out["version"])
	}
	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: out})
	if err != nil {
		t.Fatalf("convert back: %v", err)
	}
	if back["version"] != "15" {
		t.Fatalf("expected unwrapped scalar 15, got %v", back["version"])
	}
}

func TestSingletonArrayToObject_LossyWithoutMaxItems(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"replicas": arrSchema(strSchema(), nil)})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"replica": strSchema()})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategySingletonArrayToObject, Params: SingletonArrayToObjectParams{HubPath: ParsePath("replicas"), SpokePath: ParsePath("replica")}},
	}}
	_, diags, _ := Compile(rs, &hub, &spoke)
	if errs := diagMessages(diags, SeverityError); len(errs) == 0 {
		t.Fatalf("expected lossy error when array has no MaxItems<=1 constraint")
	}

	hubBounded := objSchema(map[string]extv1.JSONSchemaProps{"replicas": arrSchema(strSchema(), i64(1))})
	rs2 := RuleSet{Rules: []Rule{
		{Strategy: StrategySingletonArrayToObject, Params: SingletonArrayToObjectParams{HubPath: ParsePath("replicas"), SpokePath: ParsePath("replica")}},
	}}
	plan, diags2, _ := Compile(rs2, &hubBounded, &spoke)
	if errs := diagMessages(diags2, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors with MaxItems<=1: %v", errs)
	}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: map[string]any{"replicas": []any{"r1"}}})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if out["replica"] != "r1" {
		t.Fatalf("expected replica=r1, got %v", out)
	}
	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: out})
	if err != nil {
		t.Fatalf("convert back: %v", err)
	}
	if !reflect.DeepEqual(back["replicas"], []any{"r1"}) {
		t.Fatalf("round trip mismatch: %v", back["replicas"])
	}
}

// TestObjectToSingletonArray_RoundTrip guards against a regression where the
// hub->spoke and spoke->hub ops (and their lossless verdicts) were swapped
// for this strategy, causing both conversion directions to silently read the
// wrong tree (and thus produce nothing) instead of erroring or converting.
func TestObjectToSingletonArray_RoundTrip(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"primary": strSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"replicas": arrSchema(strSchema(), i64(1))})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyObjectToSingletonArray, Params: ObjectToSingletonArrayParams{HubPath: ParsePath("primary"), SpokePath: ParsePath("replicas")}},
	}}
	plan, diags, _ := Compile(rs, &hub, &spoke)
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors with MaxItems<=1: %v", errs)
	}

	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: map[string]any{"primary": "r1"}})
	if err != nil {
		t.Fatalf("convert h2s: %v", err)
	}
	if !reflect.DeepEqual(out["replicas"], []any{"r1"}) {
		t.Fatalf("expected replicas=[r1], got %v", out)
	}

	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: out})
	if err != nil {
		t.Fatalf("convert s2h: %v", err)
	}
	if back["primary"] != "r1" {
		t.Fatalf("round trip mismatch: %v", back["primary"])
	}
}

func TestFieldsToMap_RoundTrip(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"cpuLimit": strSchema(), "memoryLimit": strSchema(),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"resources": openMapSchema()})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyFieldsToMap, Params: FieldsToMapParams{
			HubPaths:     []FieldPath{ParsePath("cpuLimit"), ParsePath("memoryLimit")},
			SpokeMapPath: ParsePath("resources"),
			KeyNames:     map[string]string{"cpuLimit": "cpu", "memoryLimit": "memory"},
		}},
	}}
	plan, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: map[string]any{"cpuLimit": "2", "memoryLimit": "4Gi"}})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	res, ok := out["resources"].(map[string]any)
	if !ok || res["cpu"] != "2" || res["memory"] != "4Gi" {
		t.Fatalf("unexpected map: %#v", out["resources"])
	}
	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: out})
	if err != nil {
		t.Fatalf("convert back: %v", err)
	}
	if back["cpuLimit"] != "2" || back["memoryLimit"] != "4Gi" {
		t.Fatalf("round trip mismatch: %v", back)
	}
}

func TestFieldsToMap_UnknownKeyDropIsLossy(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"cpuLimit": strSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"resources": openMapSchema()})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyFieldsToMap, Params: FieldsToMapParams{
			HubPaths: []FieldPath{ParsePath("cpuLimit")}, SpokeMapPath: ParsePath("resources"),
			OnUnknownSpokeKey: UnknownKeyDrop,
		}},
	}}
	_, diags, _ := Compile(rs, &hub, &spoke)
	if errs := diagMessages(diags, SeverityError); len(errs) == 0 {
		t.Fatalf("expected lossy-without-ack error for Drop policy")
	}
}

func TestToAnnotation_RestoreOnReverse(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"maintenanceWindow": objSchema(map[string]extv1.JSONSchemaProps{"day": strSchema()}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyToAnnotation, Params: ToMetadataParams{
			HubPath: ParsePath("maintenanceWindow"), Key: "conversion.terasky.com/maintenance-window", RestoreOnReverse: true,
		}},
	}}
	plan, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors (should be lossless with RestoreOnReverse): %v", errs)
	}
	in := map[string]any{"maintenanceWindow": map[string]any{"day": "Sun"}, "metadata": map[string]any{"name": "x"}}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	md := out["metadata"].(map[string]any)
	ann, ok := md["annotations"].(map[string]any)
	if !ok || ann["conversion.terasky.com/maintenance-window"] == nil {
		t.Fatalf("expected annotation to be stashed, got metadata: %#v", md)
	}
	if _, ok := out["maintenanceWindow"]; ok {
		t.Fatalf("spoke object should not have maintenanceWindow field")
	}
	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: out})
	if err != nil {
		t.Fatalf("convert back: %v", err)
	}
	mw, ok := back["maintenanceWindow"].(map[string]any)
	if !ok || mw["day"] != "Sun" {
		t.Fatalf("expected restored maintenanceWindow, got: %#v", back["maintenanceWindow"])
	}
	backMd := back["metadata"].(map[string]any)
	if backAnn, ok := backMd["annotations"].(map[string]any); ok {
		if _, exists := backAnn["conversion.terasky.com/maintenance-window"]; exists {
			t.Fatalf("bookkeeping annotation should have been removed on restore")
		}
	}
}

func TestToAnnotation_WithoutRestoreIsLossyOnReverse(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"debug": boolSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyToAnnotation, Params: ToMetadataParams{HubPath: ParsePath("debug"), Key: "x/debug", RestoreOnReverse: false}},
	}}
	_, diags, _ := Compile(rs, &hub, &spoke)
	if errs := diagMessages(diags, SeverityError); len(errs) == 0 {
		t.Fatalf("expected lossy-without-ack error when RestoreOnReverse=false")
	}
}

func TestToAnnotation_WithoutRestore_DoesNotLeakStashKeyBackToHub(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"debug": boolSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyToAnnotation, AcknowledgeLossy: true, Reason: "not needed on spoke",
			Params: ToMetadataParams{HubPath: ParsePath("debug"), Key: "x/debug", RestoreOnReverse: false}},
	}}
	plan, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	in := map[string]any{"debug": true, "metadata": map[string]any{"name": "x"}}
	spokeObj, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
	if err != nil {
		t.Fatalf("convert h2s: %v", err)
	}
	// Going back to hub without restoring the value must also not leave the
	// bookkeeping annotation dangling in the hub-side object's metadata —
	// it was never meaningful there, and the baseline metadata passthrough
	// would otherwise carry it forward unnoticed.
	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: spokeObj})
	if err != nil {
		t.Fatalf("convert s2h: %v", err)
	}
	md, _ := back["metadata"].(map[string]any)
	if ann, ok := md["annotations"].(map[string]any); ok {
		if _, exists := ann["x/debug"]; exists {
			t.Fatalf("expected stash annotation to be stripped on the non-restoring reverse direction, got metadata: %#v", md)
		}
	}
}

func TestEnumRemap_Bidirectional(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyEnumRemap, Params: EnumRemapParams{
			Path: ParsePath("tier"),
			Mapping: []EnumValueMapping{
				{Hub: "gp2", Spoke: "small"}, {Hub: "gp4", Spoke: "medium"}, {Hub: "gp8", Spoke: "large"},
			},
		}},
	}}
	plan, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: map[string]any{"tier": "gp4"}})
	if err != nil || out["tier"] != "medium" {
		t.Fatalf("expected tier=medium, got %v err=%v", out, err)
	}
	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: out})
	if err != nil || back["tier"] != "gp4" {
		t.Fatalf("expected tier=gp4, got %v err=%v", back, err)
	}
}

func TestEnumRemap_NonInjectiveIsLossy(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyEnumRemap, Params: EnumRemapParams{
			Path: ParsePath("tier"),
			Mapping: []EnumValueMapping{
				{Hub: "gp2", Spoke: "small"}, {Hub: "gp3", Spoke: "small"},
			},
		}},
	}}
	_, diags, _ := Compile(rs, &hub, &spoke)
	if errs := diagMessages(diags, SeverityError); len(errs) == 0 {
		t.Fatalf("expected lossy-without-ack error for non-injective mapping")
	}
}

func TestDelete_RequiresAcknowledgement(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"legacyDebugFlag": boolSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyDelete, Params: DeleteParams{Path: ParsePath("legacyDebugFlag"), ExistsOn: SideHub}},
	}}
	_, diags, _ := Compile(rs, &hub, &spoke)
	if errs := diagMessages(diags, SeverityError); len(errs) == 0 {
		t.Fatalf("expected lossy-without-ack error for Delete")
	}

	rsAck := RuleSet{Rules: []Rule{
		{Strategy: StrategyDelete, AcknowledgeLossy: true, Reason: "unused", Params: DeleteParams{Path: ParsePath("legacyDebugFlag"), ExistsOn: SideHub}},
	}}
	plan, diags2, _ := Compile(rsAck, &hub, &spoke)
	if errs := diagMessages(diags2, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: map[string]any{"legacyDebugFlag": true}})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if _, ok := out["legacyDebugFlag"]; ok {
		t.Fatalf("expected field to be dropped, got %v", out)
	}
}

func TestJSONPatch_LossyUnlessOverride(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"a": strSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"a": strSchema()})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyJSONPatch, Params: JSONPatchParams{
			HubToSpoke: []JSONPatchOp{{Op: "replace", Path: "/a", Value: "x"}},
			SpokeToHub: []JSONPatchOp{{Op: "replace", Path: "/a", Value: "y"}},
		}},
	}}
	_, diags, _ := Compile(rs, &hub, &spoke)
	if errs := diagMessages(diags, SeverityError); len(errs) == 0 {
		t.Fatalf("expected lossy-without-ack error for JSONPatch without LosslessOverride")
	}
}

// TestJSONPatch_MoveIsLosslessRoundTrip exercises a "move" op, which is
// only meaningful if the patch is applied against the pristine input (so
// the "from" source genuinely exists to move away from) rather than the
// partially-built output — and only compiles at all if the moved field is
// claimed for coverage purposes, since it otherwise has no counterpart on
// the other side. Both are real bugs this test guards against regressing.
func TestJSONPatch_MoveIsLosslessRoundTrip(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"legacyFlag": boolSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"legacyFlagV2": boolSchema()})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyJSONPatch, AcknowledgeLossy: false, Params: JSONPatchParams{
			HubToSpoke:       []JSONPatchOp{{Op: "move", From: "/legacyFlag", Path: "/legacyFlagV2"}},
			SpokeToHub:       []JSONPatchOp{{Op: "move", From: "/legacyFlagV2", Path: "/legacyFlag"}},
			LosslessOverride: true,
		}},
	}}
	plan, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	in := map[string]any{"legacyFlag": true}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
	if err != nil {
		t.Fatalf("convert h2s: %v", err)
	}
	if out["legacyFlagV2"] != true {
		t.Fatalf("expected legacyFlagV2=true, got %#v", out)
	}
	if _, ok := out["legacyFlag"]; ok {
		t.Fatalf("expected legacyFlag to be moved away, got %#v", out)
	}

	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: out})
	if err != nil {
		t.Fatalf("convert s2h: %v", err)
	}
	if back["legacyFlag"] != true {
		t.Fatalf("expected legacyFlag=true after round trip, got %#v", back)
	}
	if _, ok := back["legacyFlagV2"]; ok {
		t.Fatalf("expected legacyFlagV2 to be moved away on the return trip, got %#v", back)
	}
}

func TestForEach_NestedFieldRename(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"readReplicas": arrSchema(objSchema(map[string]extv1.JSONSchemaProps{"region": strSchema()}), nil),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"readReplicas": arrSchema(objSchema(map[string]extv1.JSONSchemaProps{"zone": strSchema()}), nil),
	})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyForEach, Params: ForEachParams{
			HubItemsPath: ParsePath("readReplicas"), SpokeItemsPath: ParsePath("readReplicas"),
			Rules: []Rule{
				{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("region"), SpokePath: ParsePath("zone")}},
			},
		}},
	}}
	plan, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	in := map[string]any{"readReplicas": []any{
		map[string]any{"region": "us-east-1"},
		map[string]any{"region": "us-west-2"},
	}}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	arr, ok := out["readReplicas"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("unexpected array: %#v", out["readReplicas"])
	}
	if arr[0].(map[string]any)["zone"] != "us-east-1" || arr[1].(map[string]any)["zone"] != "us-west-2" {
		t.Fatalf("unexpected zone values: %#v", arr)
	}
}

func TestDuplicateClaim_IsCompileError(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"a": strSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"b": strSchema(), "c": strSchema()})
	rs := RuleSet{Rules: []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("a"), SpokePath: ParsePath("b")}},
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("a"), SpokePath: ParsePath("c")}},
	}}
	_, diags, _ := Compile(rs, &hub, &spoke)
	if errs := diagMessages(diags, SeverityError); len(errs) == 0 {
		t.Fatalf("expected a duplicate-claim error when two rules target the same hub field")
	}
}

func TestAnalyze_KeepsLastGoodPlanShape(t *testing.T) {
	hubSchema := objSchema(map[string]extv1.JSONSchemaProps{"a": strSchema()})
	spokeSchema := objSchema(map[string]extv1.JSONSchemaProps{"a": strSchema()})
	src := fakeSource{gen: 3, versions: []VersionSchema{
		{Name: "v2", Schema: &hubSchema, Served: true, Storage: true},
		{Name: "v1", Schema: &spokeSchema, Served: true},
	}}
	report, err := Analyze(AnalyzeInput{Source: src, HubVersion: "v2", Spokes: []RuleSet{{SpokeVersion: "v1"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.OverallLossless() {
		t.Fatalf("expected overall lossless for identical schemas")
	}
	if report.SpokeReports[0].CompiledPlan == nil {
		t.Fatalf("expected a compiled plan for a clean analysis")
	}

	// Now break it: spoke gains an uncovered field.
	spokeSchema2 := objSchema(map[string]extv1.JSONSchemaProps{"a": strSchema(), "b": strSchema()})
	src2 := fakeSource{gen: 4, versions: []VersionSchema{
		{Name: "v2", Schema: &hubSchema, Served: true, Storage: true},
		{Name: "v1", Schema: &spokeSchema2, Served: true},
	}}
	report2, err := Analyze(AnalyzeInput{Source: src2, HubVersion: "v2", Spokes: []RuleSet{{SpokeVersion: "v1"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report2.SpokeReports[0].CompiledPlan != nil {
		t.Fatalf("expected no compiled plan once analysis has errors (fail-closed)")
	}
	if !report2.HasErrors() {
		t.Fatalf("expected HasErrors() to be true")
	}
}
