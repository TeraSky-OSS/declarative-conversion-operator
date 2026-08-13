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
	"strings"
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// rawJSON builds an extv1.JSON from a literal, wrapping the bytes verbatim
// without validating them — some tests deliberately pass malformed JSON to
// exercise jsonToAny's error path.
func rawJSON(literal string) extv1.JSON {
	return extv1.JSON{Raw: []byte(literal)}
}

// wellFormedRules returns one syntactically valid ConversionRule per
// strategy, keyed by strategy — the fixture both the happy-path and the
// nil-params-error tables below mutate.
func wellFormedRules() map[Strategy]ConversionRule {
	return map[Strategy]ConversionRule{
		StrategyFieldRename: {
			Strategy:    StrategyFieldRename,
			FieldRename: &FieldRenameParams{HubPath: "spec.size", SpokePath: "spec.storageSize"},
		},
		StrategyScalarToObject: {
			Strategy: StrategyScalarToObject,
			ScalarToObject: &ScalarToObjectParams{
				HubPath: "spec.size", SpokePath: "spec.storage", Key: "value",
				DefaultsForOtherKeys: map[string]extv1.JSON{"unit": rawJSON(`"Gi"`)},
			},
		},
		StrategyObjectToScalar: {
			Strategy: StrategyObjectToScalar,
			ObjectToScalar: &ObjectToScalarParams{
				HubPath: "spec.storage", SpokePath: "spec.size", Key: "value",
				DefaultsForOtherKeys: map[string]extv1.JSON{"unit": rawJSON(`"Gi"`)},
			},
		},
		StrategySingletonArrayToObject: {
			Strategy:               StrategySingletonArrayToObject,
			SingletonArrayToObject: &SingletonArrayToObjectParams{HubPath: "spec.items", SpokePath: "spec.item"},
		},
		StrategyObjectToSingletonArray: {
			Strategy:               StrategyObjectToSingletonArray,
			ObjectToSingletonArray: &ObjectToSingletonArrayParams{HubPath: "spec.item", SpokePath: "spec.items"},
		},
		StrategyFieldsToMap: {
			Strategy: StrategyFieldsToMap,
			FieldsToMap: &FieldsToMapParams{
				HubPaths: []string{"spec.a", "spec.b"}, SpokeMapPath: "spec.m",
				KeyNames: map[string]string{"spec.a": "a", "spec.b": "b"},
			},
		},
		StrategyMapToFields: {
			Strategy: StrategyMapToFields,
			MapToFields: &MapToFieldsParams{
				HubMapPath: "spec.m", SpokePaths: []string{"spec.a", "spec.b"},
				KeyNames: map[string]string{"spec.a": "a", "spec.b": "b"},
			},
		},
		StrategyToAnnotation: {
			Strategy:     StrategyToAnnotation,
			ToAnnotation: &ToMetadataParams{HubPath: "spec.extra", Key: "example.org/extra"},
		},
		StrategyToLabel: {
			Strategy: StrategyToLabel,
			ToLabel:  &ToMetadataParams{HubPath: "spec.tier", Key: "example.org/tier"},
		},
		StrategyFromAnnotation: {
			Strategy:       StrategyFromAnnotation,
			FromAnnotation: &FromMetadataParams{SpokePath: "spec.operatorNote", Key: "example.org/operator-note"},
		},
		StrategyFromLabel: {
			Strategy:  StrategyFromLabel,
			FromLabel: &FromLabelParams{SpokePath: "spec.operatorTier", Key: "operator-tier", Serialization: "String"},
		},
		StrategyEnumRemap: {
			Strategy: StrategyEnumRemap,
			EnumRemap: &EnumRemapParams{
				Path:    "spec.tier",
				Mapping: []EnumValueMapping{{Hub: "Standard", Spoke: "std"}, {Hub: "Premium", Spoke: "prem"}},
			},
		},
		StrategyDefaultValue: {
			Strategy:     StrategyDefaultValue,
			DefaultValue: &DefaultValueParams{Path: "spec.tier", ExistsOn: SideSpoke, Default: rawJSON(`"Standard"`)},
		},
		StrategyConstant: {
			Strategy: StrategyConstant,
			Constant: &ConstantParams{Path: "spec.kind", ExistsOn: SideHub, Value: rawJSON(`"fixed"`)},
		},
		StrategyDelete: {
			Strategy: StrategyDelete,
			Delete:   &DeleteParams{Path: "spec.legacy", ExistsOn: SideHub},
		},
		StrategyJSONPatch: {
			Strategy: StrategyJSONPatch,
			JSONPatch: &JSONPatchParams{
				HubToSpoke: []JSONPatchOp{{Op: "add", Path: "/spec/flag", Value: ptrJSON(rawJSON("true"))}},
				SpokeToHub: []JSONPatchOp{{Op: "remove", Path: "/spec/flag"}},
			},
		},
		StrategyForEach: {
			Strategy: StrategyForEach,
			ForEach: &ForEachParams{
				HubItemsPath: "spec.items", SpokeItemsPath: "spec.items",
				Rules: []ConversionRule{{
					Strategy:    StrategyFieldRename,
					FieldRename: &FieldRenameParams{HubPath: "name", SpokePath: "id"},
				}},
			},
		},
		StrategyTypeCoerce: {
			Strategy:   StrategyTypeCoerce,
			TypeCoerce: &TypeCoerceParams{Path: "spec.count"},
		},
		StrategyScalarToFields: {
			Strategy: StrategyScalarToFields,
			ScalarToFields: &ScalarToFieldsParams{
				HubPath: "spec.size", Pattern: `^(?P<value>\d+)(?P<unit>[A-Za-z]+)$`,
				SpokeFields:  map[string]string{"value": "spec.sizeValue", "unit": "spec.sizeUnit"},
				JoinTemplate: "{{.value}}{{.unit}}",
			},
		},
		StrategyFieldsToScalar: {
			Strategy: StrategyFieldsToScalar,
			FieldsToScalar: &FieldsToScalarParams{
				HubFields: map[string]string{"value": "spec.sizeValue", "unit": "spec.sizeUnit"},
				Pattern:   `^(?P<value>\d+)(?P<unit>[A-Za-z]+)$`, SpokePath: "spec.size",
				JoinTemplate: "{{.value}}{{.unit}}",
			},
		},
		StrategyArrayToMapByKey: {
			Strategy:        StrategyArrayToMapByKey,
			ArrayToMapByKey: &ArrayToMapByKeyParams{HubPath: "spec.list", SpokePath: "spec.map", KeyField: "name"},
		},
		StrategyMapToArrayByKey: {
			Strategy:        StrategyMapToArrayByKey,
			MapToArrayByKey: &MapToArrayByKeyParams{HubPath: "spec.map", SpokePath: "spec.list", KeyField: "name"},
		},
		StrategyNumericScale: {
			Strategy:     StrategyNumericScale,
			NumericScale: &NumericScaleParams{HubPath: "spec.bytes", SpokePath: "spec.kilobytes", Factor: 0.001},
		},
		StrategyListJoin: {
			Strategy: StrategyListJoin,
			ListJoin: &ListJoinParams{HubPath: "spec.tags", SpokePath: "spec.tagsCSV", Separator: ","},
		},
		StrategyListSplit: {
			Strategy:  StrategyListSplit,
			ListSplit: &ListSplitParams{HubPath: "spec.tagsCSV", SpokePath: "spec.tags", Separator: ","},
		},
	}
}

func ptrJSON(j extv1.JSON) *extv1.JSON { return &j }

// clearParams returns a copy of rule with every strategy-specific params
// pointer nilled out, simulating a rule whose declared strategy doesn't
// match any populated params field (the "requires X params" error path).
func clearParams(rule ConversionRule) ConversionRule {
	rule.FieldRename = nil
	rule.ScalarToObject = nil
	rule.ObjectToScalar = nil
	rule.SingletonArrayToObject = nil
	rule.ObjectToSingletonArray = nil
	rule.FieldsToMap = nil
	rule.MapToFields = nil
	rule.ToAnnotation = nil
	rule.ToLabel = nil
	rule.FromAnnotation = nil
	rule.FromLabel = nil
	rule.EnumRemap = nil
	rule.DefaultValue = nil
	rule.Constant = nil
	rule.Delete = nil
	rule.JSONPatch = nil
	rule.ForEach = nil
	rule.TypeCoerce = nil
	rule.ScalarToFields = nil
	rule.FieldsToScalar = nil
	rule.ArrayToMapByKey = nil
	rule.MapToArrayByKey = nil
	rule.NumericScale = nil
	rule.ListJoin = nil
	rule.ListSplit = nil
	return rule
}

func TestConvertRules_HappyPathPerStrategy(t *testing.T) {
	for strategy, rule := range wellFormedRules() {
		t.Run(string(strategy), func(t *testing.T) {
			out, err := convertRules([]ConversionRule{rule})
			if err != nil {
				t.Fatalf("unexpected error for a well-formed %s rule: %v", strategy, err)
			}
			if len(out) != 1 {
				t.Fatalf("expected exactly one converted rule, got %d", len(out))
			}
			if out[0].Strategy != engine.Strategy(strategy) {
				t.Fatalf("expected engine strategy %q, got %q", strategy, out[0].Strategy)
			}
			if out[0].Params == nil {
				t.Fatalf("expected non-nil params for %s", strategy)
			}
		})
	}
}

func TestConvertRules_MissingParamsErrorsPerStrategy(t *testing.T) {
	for strategy, rule := range wellFormedRules() {
		t.Run(string(strategy), func(t *testing.T) {
			bare := clearParams(rule)
			_, err := convertRules([]ConversionRule{bare})
			if err == nil {
				t.Fatalf("expected an error when %s's params field is nil", strategy)
			}
			if !strings.Contains(err.Error(), string(strategy)) {
				t.Fatalf("expected the error to name the strategy %q, got: %v", strategy, err)
			}
		})
	}
}

func TestConvertRules_UnknownStrategy(t *testing.T) {
	_, err := convertRules([]ConversionRule{{Strategy: "Bogus"}})
	if err == nil {
		t.Fatalf("expected an error for an unrecognized strategy")
	}
}

func TestConvertRules_IndexAndStrategyInErrorMessage(t *testing.T) {
	rules := []ConversionRule{
		{Strategy: StrategyFieldRename, FieldRename: &FieldRenameParams{HubPath: "a", SpokePath: "b"}},
		{Strategy: StrategyDelete}, // missing params, at index 1
	}
	_, err := convertRules(rules)
	if err == nil {
		t.Fatalf("expected an error")
	}
	if !strings.Contains(err.Error(), "rule 1") || !strings.Contains(err.Error(), string(StrategyDelete)) {
		t.Fatalf("expected the error to identify rule index 1 and strategy Delete, got: %v", err)
	}
}

func TestConvertRules_ForEachNesting(t *testing.T) {
	rule := wellFormedRules()[StrategyForEach]
	out, err := convertRules([]ConversionRule{rule})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	params, ok := out[0].Params.(engine.ForEachParams)
	if !ok {
		t.Fatalf("expected engine.ForEachParams, got %T", out[0].Params)
	}
	if len(params.Rules) != 1 || params.Rules[0].Strategy != engine.Strategy(StrategyFieldRename) {
		t.Fatalf("expected one nested FieldRename rule, got %+v", params.Rules)
	}
}

func TestConvertRules_ForEachNestedErrorIsWrapped(t *testing.T) {
	rule := ConversionRule{
		Strategy: StrategyForEach,
		ForEach: &ForEachParams{
			HubItemsPath: "spec.items", SpokeItemsPath: "spec.items",
			Rules: []ConversionRule{{Strategy: StrategyFieldRename}}, // missing params
		},
	}
	_, err := convertRules([]ConversionRule{rule})
	if err == nil {
		t.Fatalf("expected an error from the nested rule")
	}
	if !strings.Contains(err.Error(), "nested rules") {
		t.Fatalf("expected the error to be wrapped with \"nested rules\", got: %v", err)
	}
}

func TestConvertParams_InvalidJSONValue(t *testing.T) {
	cases := map[string]ConversionRule{
		"DefaultValue": {Strategy: StrategyDefaultValue, DefaultValue: &DefaultValueParams{Path: "p", ExistsOn: SideHub, Default: rawJSON(`{not json`)}},
		"Constant":     {Strategy: StrategyConstant, Constant: &ConstantParams{Path: "p", ExistsOn: SideHub, Value: rawJSON(`{not json`)}},
		"JSONPatchValue": {
			Strategy:  StrategyJSONPatch,
			JSONPatch: &JSONPatchParams{HubToSpoke: []JSONPatchOp{{Op: "add", Path: "/p", Value: ptrJSON(rawJSON(`{not json`))}}},
		},
		"ScalarToObjectDefaults": {
			Strategy: StrategyScalarToObject,
			ScalarToObject: &ScalarToObjectParams{
				HubPath: "a", SpokePath: "b", Key: "k",
				DefaultsForOtherKeys: map[string]extv1.JSON{"x": rawJSON(`{not json`)},
			},
		},
	}
	for name, rule := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := convertRules([]ConversionRule{rule}); err == nil {
				t.Fatalf("expected an error for malformed JSON in %s", name)
			}
		})
	}
}

func TestConvertParams_ConstantOmittedValueIsNil(t *testing.T) {
	// An unset extv1.JSON.Raw (as opposed to an explicit JSON "null") must
	// convert to a nil Go value, not an error — jsonToAny's empty-Raw guard.
	rule := ConversionRule{Strategy: StrategyConstant, Constant: &ConstantParams{Path: "p", ExistsOn: SideHub}}
	out, err := convertRules([]ConversionRule{rule})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	params, ok := out[0].Params.(engine.ConstantParams)
	if !ok {
		t.Fatalf("expected engine.ConstantParams, got %T", out[0].Params)
	}
	if params.Value != nil {
		t.Fatalf("expected a nil Value for an omitted JSON literal, got %v", params.Value)
	}
}

func TestToRuleSets_XRDConversionConfig(t *testing.T) {
	cfg := &XRDConversionConfig{Spec: XRDConversionConfigSpec{
		HubVersion: "v2",
		Spokes: []SpokeVersionRules{
			{
				Version: "v1",
				Rules:   []ConversionRule{{Strategy: StrategyFieldRename, FieldRename: &FieldRenameParams{HubPath: "a", SpokePath: "b"}}},
			},
			{
				Version: "v1beta1",
				Rules:   []ConversionRule{{Strategy: StrategyDelete, Delete: &DeleteParams{Path: "c", ExistsOn: SideHub}}},
			},
		},
	}}

	ruleSets, err := cfg.ToRuleSets()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ruleSets) != 2 {
		t.Fatalf("expected 2 rule sets, got %d", len(ruleSets))
	}
	if ruleSets[0].HubVersion != "v2" || ruleSets[0].SpokeVersion != "v1" {
		t.Fatalf("unexpected first rule set: %+v", ruleSets[0])
	}
	if ruleSets[0].UnmappedFieldPolicy != engine.UnmappedFieldPolicy(UnmappedFieldPolicyError) {
		t.Fatalf("expected the default UnmappedFieldPolicy (Error) when unset, got %q", ruleSets[0].UnmappedFieldPolicy)
	}

	cfg.Spec.UnmappedFieldPolicy = UnmappedFieldPolicyWarn
	ruleSets, err = cfg.ToRuleSets()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, rs := range ruleSets {
		if rs.UnmappedFieldPolicy != engine.UnmappedFieldPolicy(UnmappedFieldPolicyWarn) {
			t.Fatalf("expected the explicitly set UnmappedFieldPolicy (Warn) to apply to every spoke, got %q for spoke %q", rs.UnmappedFieldPolicy, rs.SpokeVersion)
		}
	}
}

func TestToRuleSets_XRDConversionConfig_PropagatesSpokeError(t *testing.T) {
	cfg := &XRDConversionConfig{Spec: XRDConversionConfigSpec{
		HubVersion: "v2",
		Spokes: []SpokeVersionRules{
			{Version: "v1", Rules: []ConversionRule{{Strategy: StrategyFieldRename}}}, // missing params
		},
	}}
	_, err := cfg.ToRuleSets()
	if err == nil {
		t.Fatalf("expected an error")
	}
	if !strings.Contains(err.Error(), `spoke "v1"`) {
		t.Fatalf("expected the error to name the offending spoke version, got: %v", err)
	}
}

func TestToRuleSets_CRDConversionConfig(t *testing.T) {
	cfg := &CRDConversionConfig{Spec: CRDConversionConfigSpec{
		HubVersion: "v2",
		Spokes: []SpokeVersionRules{
			{Version: "v1", Rules: []ConversionRule{{Strategy: StrategyFieldRename, FieldRename: &FieldRenameParams{HubPath: "a", SpokePath: "b"}}}},
		},
	}}
	ruleSets, err := cfg.ToRuleSets()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ruleSets) != 1 || ruleSets[0].SpokeVersion != "v1" {
		t.Fatalf("unexpected rule sets: %+v", ruleSets)
	}
}
