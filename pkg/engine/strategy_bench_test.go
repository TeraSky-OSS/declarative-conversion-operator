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

type strategyBench struct {
	name  Strategy
	hub   extv1.JSONSchemaProps
	spoke extv1.JSONSchemaProps
	rules []Rule
	obj   map[string]any
}

func perStrategyBenches() []strategyBench {
	item := objSchema(map[string]extv1.JSONSchemaProps{"name": strSchema(), "cidr": strSchema()})
	return []strategyBench{
		{
			name:  StrategyFieldRename,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"storageGB": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"storageSize": strSchema()}),
			rules: []Rule{{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("storageGB"), SpokePath: ParsePath("storageSize")}}},
			obj:   map[string]any{"storageGB": "10"},
		},
		{
			name:  StrategyScalarToObject,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"version": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"version": objSchema(map[string]extv1.JSONSchemaProps{"major": strSchema()})}),
			rules: []Rule{{Strategy: StrategyScalarToObject, Params: ScalarToObjectParams{HubPath: ParsePath("version"), SpokePath: ParsePath("version"), Key: "major"}}},
			obj:   map[string]any{"version": "1"},
		},
		{
			name:  StrategyObjectToScalar,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"version": objSchema(map[string]extv1.JSONSchemaProps{"major": strSchema()})}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"version": strSchema()}),
			rules: []Rule{{Strategy: StrategyObjectToScalar, Params: ObjectToScalarParams{HubPath: ParsePath("version"), SpokePath: ParsePath("version"), Key: "major"}}},
			obj:   map[string]any{"version": map[string]any{"major": "1"}},
		},
		{
			name:  StrategySingletonArrayToObject,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"zones": arrSchema(objSchema(map[string]extv1.JSONSchemaProps{"name": strSchema()}), i64(1))}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"zone": objSchema(map[string]extv1.JSONSchemaProps{"name": strSchema()})}),
			rules: []Rule{{Strategy: StrategySingletonArrayToObject, Params: SingletonArrayToObjectParams{HubPath: ParsePath("zones"), SpokePath: ParsePath("zone")}}},
			obj:   map[string]any{"zones": []any{map[string]any{"name": "a"}}},
		},
		{
			name:  StrategyObjectToSingletonArray,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"zone": objSchema(map[string]extv1.JSONSchemaProps{"name": strSchema()})}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"zones": arrSchema(objSchema(map[string]extv1.JSONSchemaProps{"name": strSchema()}), i64(1))}),
			rules: []Rule{{Strategy: StrategyObjectToSingletonArray, Params: ObjectToSingletonArrayParams{HubPath: ParsePath("zone"), SpokePath: ParsePath("zones")}}},
			obj:   map[string]any{"zone": map[string]any{"name": "a"}},
		},
		{
			name:  StrategyFieldsToMap,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"env": strSchema(), "team": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"tags": openMapSchema()}),
			rules: []Rule{{Strategy: StrategyFieldsToMap, Params: FieldsToMapParams{HubPaths: []FieldPath{ParsePath("env"), ParsePath("team")}, SpokeMapPath: ParsePath("tags"), KeyNames: map[string]string{"env": "env", "team": "team"}}}},
			obj:   map[string]any{"env": "dev", "team": "core"},
		},
		{
			name:  StrategyMapToFields,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"tags": openMapSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"env": strSchema(), "team": strSchema()}),
			rules: []Rule{{Strategy: StrategyMapToFields, Params: MapToFieldsParams{HubMapPath: ParsePath("tags"), SpokePaths: []FieldPath{ParsePath("env"), ParsePath("team")}, KeyNames: map[string]string{"env": "env", "team": "team"}}}},
			obj:   map[string]any{"tags": map[string]any{"env": "dev", "team": "core"}},
		},
		{
			name:  StrategyToAnnotation,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"description": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{}),
			rules: []Rule{{Strategy: StrategyToAnnotation, Params: ToMetadataParams{HubPath: ParsePath("description"), Key: "ex/desc", RestoreOnReverse: true}}},
			obj:   map[string]any{"description": "n", "metadata": map[string]any{"name": "x"}},
		},
		{
			name:  StrategyToLabel,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{}),
			rules: []Rule{{Strategy: StrategyToLabel, Params: ToMetadataParams{HubPath: ParsePath("tier"), Key: "tier", Serialization: "String", RestoreOnReverse: true}}},
			obj:   map[string]any{"tier": "gold", "metadata": map[string]any{"name": "x"}},
		},
		{
			name:  StrategyFromAnnotation,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"note": strSchema()}),
			rules: []Rule{{Strategy: StrategyFromAnnotation, Params: FromMetadataParams{SpokePath: ParsePath("note"), Key: "ex/note", Serialization: "String", StashOnReverse: true}}},
			obj:   map[string]any{"metadata": map[string]any{"name": "x", "annotations": map[string]any{"ex/note": "hi"}}},
		},
		{
			name:  StrategyFromLabel,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()}),
			rules: []Rule{{Strategy: StrategyFromLabel, Params: FromMetadataParams{SpokePath: ParsePath("tier"), Key: "tier", Serialization: "String", StashOnReverse: true}}},
			obj:   map[string]any{"metadata": map[string]any{"name": "x", "labels": map[string]any{"tier": "gold"}}},
		},
		{
			name:  StrategyEnumRemap,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()}),
			rules: []Rule{{Strategy: StrategyEnumRemap, Params: EnumRemapParams{Path: ParsePath("tier"), Mapping: []EnumValueMapping{{Hub: "Standard", Spoke: "std"}}}}},
			obj:   map[string]any{"tier": "Standard"},
		},
		{
			name:  StrategyDefaultValue,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"replicas": intSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{}),
			rules: []Rule{{Strategy: StrategyDefaultValue, AcknowledgeLossy: true, Params: DefaultValueParams{Path: ParsePath("replicas"), ExistsOn: SideHub, Default: float64(1)}}},
			obj:   map[string]any{},
		},
		{
			name:  StrategyConstant,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"kindFixed": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{}),
			rules: []Rule{{Strategy: StrategyConstant, AcknowledgeLossy: true, Params: ConstantParams{Path: ParsePath("kindFixed"), ExistsOn: SideHub, Value: "fixed"}}},
			obj:   map[string]any{},
		},
		{
			name:  StrategyDelete,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"debug": boolSchema()}),
			rules: []Rule{{Strategy: StrategyDelete, AcknowledgeLossy: true, Params: DeleteParams{Path: ParsePath("debug"), ExistsOn: SideSpoke}}},
			obj:   map[string]any{},
		},
		{
			name:  StrategyJSONPatch,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"flag": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"flag": strSchema()}),
			rules: []Rule{{Strategy: StrategyJSONPatch, AcknowledgeLossy: true, Params: JSONPatchParams{
				HubToSpoke: []JSONPatchOp{{Op: "replace", Path: "/flag", Value: "x"}},
				SpokeToHub: []JSONPatchOp{{Op: "replace", Path: "/flag", Value: "y"}},
			}}},
			obj: map[string]any{"flag": "y"},
		},
		{
			name:  StrategyForEach,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"volumes": arrSchema(volumeItemSchema(), nil)}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"volumes": arrSchema(volumeItemSpokeSchema(), nil)}),
			rules: forEachVolumeRules().Rules,
			obj:   volumesObject(8),
		},
		{
			name:  StrategyTypeCoerce,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"replicas": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"replicas": intSchema()}),
			rules: []Rule{{Strategy: StrategyTypeCoerce, Params: TypeCoerceParams{Path: ParsePath("replicas")}}},
			obj:   map[string]any{"replicas": "3"},
		},
		{
			name:  StrategyScalarToFields,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"size": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"sizeValue": strSchema(), "sizeUnit": strSchema()}),
			rules: []Rule{{Strategy: StrategyScalarToFields, AcknowledgeLossy: true, Params: ScalarToFieldsParams{
				HubPath: ParsePath("size"), Pattern: `^(?P<value>\d+)(?P<unit>[A-Za-z]+)$`,
				SpokeFields:  map[string]FieldPath{"value": ParsePath("sizeValue"), "unit": ParsePath("sizeUnit")},
				JoinTemplate: "{{.value}}{{.unit}}", LosslessOverride: true,
			}}},
			obj: map[string]any{"size": "10Gi"},
		},
		{
			name:  StrategyFieldsToScalar,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"sizeValue": strSchema(), "sizeUnit": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"size": strSchema()}),
			rules: []Rule{{Strategy: StrategyFieldsToScalar, AcknowledgeLossy: true, Params: FieldsToScalarParams{
				HubFields: map[string]FieldPath{"value": ParsePath("sizeValue"), "unit": ParsePath("sizeUnit")},
				SpokePath: ParsePath("size"), Pattern: `^(?P<value>\d+)(?P<unit>[A-Za-z]+)$`,
				JoinTemplate: "{{.value}}{{.unit}}", LosslessOverride: true,
			}}},
			obj: map[string]any{"sizeValue": "10", "sizeUnit": "Gi"},
		},
		{
			name:  StrategyArrayToMapByKey,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"zones": arrSchema(item, nil)}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"zones": openMapSchema()}),
			rules: []Rule{{Strategy: StrategyArrayToMapByKey, AcknowledgeLossy: true, Params: ArrayToMapByKeyParams{HubPath: ParsePath("zones"), SpokePath: ParsePath("zones"), KeyField: "name"}}},
			obj:   zonesArrayObject(8),
		},
		{
			name:  StrategyMapToArrayByKey,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"zones": openMapSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"zones": arrSchema(item, nil)}),
			rules: []Rule{{Strategy: StrategyMapToArrayByKey, AcknowledgeLossy: true, Params: MapToArrayByKeyParams{HubPath: ParsePath("zones"), SpokePath: ParsePath("zones"), KeyField: "name"}}},
			obj:   zonesMapObject(8),
		},
		{
			name:  StrategyNumericScale,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"bytes": numSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"kilobytes": numSchema()}),
			rules: []Rule{{Strategy: StrategyNumericScale, Params: NumericScaleParams{HubPath: ParsePath("bytes"), SpokePath: ParsePath("kilobytes"), Factor: 0.001}}},
			obj:   map[string]any{"bytes": float64(1000)},
		},
		{
			name:  StrategyListJoin,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"tags": arrSchema(strSchema(), nil)}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"tagsCSV": strSchema()}),
			rules: []Rule{{Strategy: StrategyListJoin, Params: ListJoinParams{HubPath: ParsePath("tags"), SpokePath: ParsePath("tagsCSV"), Separator: ","}}},
			obj:   map[string]any{"tags": []any{"a", "b"}},
		},
		{
			name:  StrategyListSplit,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"tagsCSV": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"tags": arrSchema(strSchema(), nil)}),
			rules: []Rule{{Strategy: StrategyListSplit, Params: ListSplitParams{HubPath: ParsePath("tagsCSV"), SpokePath: ParsePath("tags"), Separator: ","}}},
			obj:   map[string]any{"tagsCSV": "a,b"},
		},
		{
			name:  StrategyQuantity,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"cpuRequest": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"cpuMillis": intSchema()}),
			rules: []Rule{{Strategy: StrategyQuantity, AcknowledgeLossy: true, Params: QuantityParams{HubPath: ParsePath("cpuRequest"), SpokePath: ParsePath("cpuMillis")}}},
			obj:   map[string]any{"cpuRequest": "500m"},
		},
		{
			name:  StrategyDuration,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"timeout": strSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"timeoutSeconds": intSchema()}),
			rules: []Rule{{Strategy: StrategyDuration, AcknowledgeLossy: true, Params: DurationParams{HubPath: ParsePath("timeout"), SpokePath: ParsePath("timeoutSeconds")}}},
			obj:   map[string]any{"timeout": "5s"},
		},
		{
			name:  StrategyMapKeyRename,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"labels": openMapSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"labels": openMapSchema()}),
			rules: []Rule{{Strategy: StrategyMapKeyRename, Params: MapKeyRenameParams{HubPath: ParsePath("labels"), SpokePath: ParsePath("labels"), Renames: map[string]string{"app": "application"}}}},
			obj:   map[string]any{"labels": map[string]any{"app": "w", "keep": "yes"}},
		},
		{
			name:  StrategyCEL,
			hub:   objSchema(map[string]extv1.JSONSchemaProps{"packed": intSchema()}),
			spoke: objSchema(map[string]extv1.JSONSchemaProps{"bitHigh": intSchema(), "bitLow": intSchema()}),
			rules: []Rule{{Strategy: StrategyCEL, AcknowledgeLossy: true, Params: CELParams{
				HubPaths:   []FieldPath{ParsePath("packed")},
				SpokePaths: []FieldPath{ParsePath("bitHigh"), ParsePath("bitLow")},
				HubToSpoke: `{"bitHigh": int(object.packed) / 256, "bitLow": int(object.packed) % 256}`,
				SpokeToHub: `{"packed": int(object.bitHigh) * 256 + int(object.bitLow)}`,
			}}},
			obj: map[string]any{"packed": float64(1025)},
		},
	}
}

func BenchmarkConvert_PerStrategy(b *testing.B) {
	cases := perStrategyBenches()
	if len(cases) != 29 {
		b.Fatalf("perStrategyBenches has %d, want 29", len(cases))
	}
	for _, tc := range cases {
		tc := tc
		hub, spoke := tc.hub, tc.spoke
		plan, diags, err := Compile(RuleSet{HubVersion: "v2", SpokeVersion: "v1", Rules: tc.rules}, &hub, &spoke)
		if err != nil {
			b.Fatalf("%s compile: %v", tc.name, err)
		}
		if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
			b.Fatalf("%s compile errors: %v", tc.name, errs)
		}
		b.Run(string(tc.name), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: tc.obj}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
