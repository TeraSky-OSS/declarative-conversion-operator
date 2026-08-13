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

package scalegen

import (
	"fmt"
	"math/rand"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	v1a "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func strProp() extv1.JSONSchemaProps  { return extv1.JSONSchemaProps{Type: "string"} }
func intProp() extv1.JSONSchemaProps  { return extv1.JSONSchemaProps{Type: "integer"} }
func numProp() extv1.JSONSchemaProps  { return extv1.JSONSchemaProps{Type: "number"} }
func boolProp() extv1.JSONSchemaProps { return extv1.JSONSchemaProps{Type: "boolean"} }
func objProp(p map[string]extv1.JSONSchemaProps) extv1.JSONSchemaProps {
	return extv1.JSONSchemaProps{Type: "object", Properties: p}
}
func arrProp(item extv1.JSONSchemaProps) extv1.JSONSchemaProps {
	return extv1.JSONSchemaProps{Type: "array", Items: &extv1.JSONSchemaPropsOrArray{Schema: &item}}
}
func arrPropMax1(item extv1.JSONSchemaProps) extv1.JSONSchemaProps {
	n := int64(1)
	p := arrProp(item)
	p.MaxItems = &n
	return p
}
func mapOf(item extv1.JSONSchemaProps) extv1.JSONSchemaProps {
	cp := item
	return extv1.JSONSchemaProps{Type: "object", AdditionalProperties: &extv1.JSONSchemaPropsOrBool{Allows: true, Schema: &cp}}
}
func strMapProp() extv1.JSONSchemaProps { return mapOf(strProp()) }

func pj(s string) *extv1.JSON { j := extv1.JSON{Raw: []byte(s)}; return &j }

// Slot is one strategy that can be composed into a generated CRD.
type Slot struct {
	Name       v1a.Strategy
	HubProps   map[string]extv1.JSONSchemaProps
	SpokeProps map[string]extv1.JSONSchemaProps
	Rule       v1a.ConversionRule
	SpokeSpec  map[string]any
}

func slots() []Slot {
	item := objProp(map[string]extv1.JSONSchemaProps{"name": strProp(), "cidr": strProp()})
	volHub := objProp(map[string]extv1.JSONSchemaProps{"name": strProp(), "sizeGB": intProp()})
	volSpoke := objProp(map[string]extv1.JSONSchemaProps{"name": strProp(), "size": intProp()})
	lossy := func(r v1a.ConversionRule) v1a.ConversionRule {
		r.AcknowledgeLossy = true
		r.Reason = "scale-gen synthetic fixture"
		return r
	}
	return []Slot{
		{Name: v1a.StrategyFieldRename,
			HubProps: map[string]extv1.JSONSchemaProps{"storageGB": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"storageSize": strProp()},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyFieldRename, FieldRename: &v1a.FieldRenameParams{HubPath: "spec.storageGB", SpokePath: "spec.storageSize"}},
			SpokeSpec: map[string]any{"storageSize": "10"}},
		{Name: v1a.StrategyTypeCoerce,
			HubProps: map[string]extv1.JSONSchemaProps{"replicas": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"replicas": intProp()},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyTypeCoerce, TypeCoerce: &v1a.TypeCoerceParams{Path: "spec.replicas"}},
			SpokeSpec: map[string]any{"replicas": 3}},
		{Name: v1a.StrategyEnumRemap,
			HubProps: map[string]extv1.JSONSchemaProps{"tier": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"tier": strProp()},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyEnumRemap, EnumRemap: &v1a.EnumRemapParams{Path: "spec.tier", Mapping: []v1a.EnumValueMapping{{Hub: extv1.JSON{Raw: []byte(`"Standard"`)}, Spoke: extv1.JSON{Raw: []byte(`"std"`)}}}}},
			SpokeSpec: map[string]any{"tier": "std"}},
		{Name: v1a.StrategyNumericScale,
			HubProps: map[string]extv1.JSONSchemaProps{"bytes": numProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"kilobytes": numProp()},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyNumericScale, NumericScale: &v1a.NumericScaleParams{HubPath: "spec.bytes", SpokePath: "spec.kilobytes", Factor: 0.001}},
			SpokeSpec: map[string]any{"kilobytes": 1}},
		{Name: v1a.StrategyListJoin,
			HubProps: map[string]extv1.JSONSchemaProps{"tags": arrProp(strProp())}, SpokeProps: map[string]extv1.JSONSchemaProps{"tagsCSV": strProp()},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyListJoin, ListJoin: &v1a.ListJoinParams{HubPath: "spec.tags", SpokePath: "spec.tagsCSV", Separator: ","}},
			SpokeSpec: map[string]any{"tagsCSV": "a,b"}},
		{Name: v1a.StrategyListSplit,
			HubProps: map[string]extv1.JSONSchemaProps{"csv": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"items": arrProp(strProp())},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyListSplit, ListSplit: &v1a.ListSplitParams{HubPath: "spec.csv", SpokePath: "spec.items", Separator: ","}},
			SpokeSpec: map[string]any{"items": []any{"a", "b"}}},
		{Name: v1a.StrategyQuantity,
			HubProps: map[string]extv1.JSONSchemaProps{"cpuRequest": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"cpuMillis": intProp()},
			Rule:      lossy(v1a.ConversionRule{Strategy: v1a.StrategyQuantity, Quantity: &v1a.QuantityParams{HubPath: "spec.cpuRequest", SpokePath: "spec.cpuMillis"}}),
			SpokeSpec: map[string]any{"cpuMillis": 500}},
		{Name: v1a.StrategyDuration,
			HubProps: map[string]extv1.JSONSchemaProps{"timeout": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"timeoutSeconds": intProp()},
			Rule:      lossy(v1a.ConversionRule{Strategy: v1a.StrategyDuration, Duration: &v1a.DurationParams{HubPath: "spec.timeout", SpokePath: "spec.timeoutSeconds"}}),
			SpokeSpec: map[string]any{"timeoutSeconds": 5}},
		{Name: v1a.StrategyMapKeyRename,
			HubProps: map[string]extv1.JSONSchemaProps{"extra": strMapProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"extra": strMapProp()},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyMapKeyRename, MapKeyRename: &v1a.MapKeyRenameParams{HubPath: "spec.extra", SpokePath: "spec.extra", Renames: map[string]string{"app": "application"}}},
			SpokeSpec: map[string]any{"extra": map[string]any{"application": "w", "keep": "yes"}}},
		{Name: v1a.StrategyArrayToMapByKey,
			HubProps: map[string]extv1.JSONSchemaProps{"zones": arrProp(item)}, SpokeProps: map[string]extv1.JSONSchemaProps{"zones": mapOf(objProp(map[string]extv1.JSONSchemaProps{"cidr": strProp()}))},
			Rule:      lossy(v1a.ConversionRule{Strategy: v1a.StrategyArrayToMapByKey, ArrayToMapByKey: &v1a.ArrayToMapByKeyParams{HubPath: "spec.zones", SpokePath: "spec.zones", KeyField: "name"}}),
			SpokeSpec: map[string]any{"zones": map[string]any{"a": map[string]any{"cidr": "10.0.0.0/24"}}}},
		{Name: v1a.StrategyMapToArrayByKey,
			HubProps: map[string]extv1.JSONSchemaProps{"limits": mapOf(objProp(map[string]extv1.JSONSchemaProps{"cidr": strProp()}))}, SpokeProps: map[string]extv1.JSONSchemaProps{"limits": arrProp(item)},
			Rule:      lossy(v1a.ConversionRule{Strategy: v1a.StrategyMapToArrayByKey, MapToArrayByKey: &v1a.MapToArrayByKeyParams{HubPath: "spec.limits", SpokePath: "spec.limits", KeyField: "name"}}),
			SpokeSpec: map[string]any{"limits": []any{map[string]any{"name": "a", "cidr": "10.0.0.0/24"}}}},
		{Name: v1a.StrategyForEach,
			HubProps: map[string]extv1.JSONSchemaProps{"volumes": arrProp(volHub)}, SpokeProps: map[string]extv1.JSONSchemaProps{"volumes": arrProp(volSpoke)},
			Rule: v1a.ConversionRule{Strategy: v1a.StrategyForEach, ForEach: &v1a.ForEachParams{HubItemsPath: "spec.volumes", SpokeItemsPath: "spec.volumes", Rules: []v1a.ConversionRule{
				{Strategy: v1a.StrategyFieldRename, FieldRename: &v1a.FieldRenameParams{HubPath: "name", SpokePath: "name"}},
				{Strategy: v1a.StrategyFieldRename, FieldRename: &v1a.FieldRenameParams{HubPath: "sizeGB", SpokePath: "size"}},
			}}},
			SpokeSpec: map[string]any{"volumes": []any{map[string]any{"name": "d0", "size": 10}}}},
		{Name: v1a.StrategyScalarToObject,
			HubProps: map[string]extv1.JSONSchemaProps{"ver": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"ver": objProp(map[string]extv1.JSONSchemaProps{"major": strProp()})},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyScalarToObject, ScalarToObject: &v1a.ScalarToObjectParams{HubPath: "spec.ver", SpokePath: "spec.ver", Key: "major"}},
			SpokeSpec: map[string]any{"ver": map[string]any{"major": "1"}}},
		{Name: v1a.StrategyObjectToScalar,
			HubProps: map[string]extv1.JSONSchemaProps{"rel": objProp(map[string]extv1.JSONSchemaProps{"major": strProp()})}, SpokeProps: map[string]extv1.JSONSchemaProps{"rel": strProp()},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyObjectToScalar, ObjectToScalar: &v1a.ObjectToScalarParams{HubPath: "spec.rel", SpokePath: "spec.rel", Key: "major"}},
			SpokeSpec: map[string]any{"rel": "1"}},
		{Name: v1a.StrategySingletonArrayToObject,
			HubProps: map[string]extv1.JSONSchemaProps{"zoneList": arrProp(objProp(map[string]extv1.JSONSchemaProps{"name": strProp()}))}, SpokeProps: map[string]extv1.JSONSchemaProps{"zone": objProp(map[string]extv1.JSONSchemaProps{"name": strProp()})},
			Rule:      lossy(v1a.ConversionRule{Strategy: v1a.StrategySingletonArrayToObject, SingletonArrayToObject: &v1a.SingletonArrayToObjectParams{HubPath: "spec.zoneList", SpokePath: "spec.zone"}}),
			SpokeSpec: map[string]any{"zone": map[string]any{"name": "a"}}},
		{Name: v1a.StrategyObjectToSingletonArray,
			HubProps: map[string]extv1.JSONSchemaProps{"primary": objProp(map[string]extv1.JSONSchemaProps{"name": strProp()})}, SpokeProps: map[string]extv1.JSONSchemaProps{"primaries": arrPropMax1(objProp(map[string]extv1.JSONSchemaProps{"name": strProp()}))},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyObjectToSingletonArray, ObjectToSingletonArray: &v1a.ObjectToSingletonArrayParams{HubPath: "spec.primary", SpokePath: "spec.primaries"}},
			SpokeSpec: map[string]any{"primaries": []any{map[string]any{"name": "a"}}}},
		{Name: v1a.StrategyFieldsToMap,
			HubProps: map[string]extv1.JSONSchemaProps{"env": strProp(), "team": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"labels": strMapProp()},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyFieldsToMap, FieldsToMap: &v1a.FieldsToMapParams{HubPaths: []string{"spec.env", "spec.team"}, SpokeMapPath: "spec.labels", KeyNames: map[string]string{"spec.env": "env", "spec.team": "team"}}},
			SpokeSpec: map[string]any{"labels": map[string]any{"env": "dev", "team": "core"}}},
		{Name: v1a.StrategyMapToFields,
			HubProps: map[string]extv1.JSONSchemaProps{"meta": strMapProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"owner": strProp(), "group": strProp()},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyMapToFields, MapToFields: &v1a.MapToFieldsParams{HubMapPath: "spec.meta", SpokePaths: []string{"spec.owner", "spec.group"}, KeyNames: map[string]string{"spec.owner": "owner", "spec.group": "group"}}},
			SpokeSpec: map[string]any{"owner": "a", "group": "b"}},
		{Name: v1a.StrategyToAnnotation,
			HubProps: map[string]extv1.JSONSchemaProps{"description": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyToAnnotation, ToAnnotation: &v1a.ToMetadataParams{HubPath: "spec.description", Key: "scale.dco.example.org/desc", RestoreOnReverse: true, Serialization: "String"}},
			SpokeSpec: map[string]any{}},
		{Name: v1a.StrategyToLabel,
			HubProps: map[string]extv1.JSONSchemaProps{"track": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{},
			Rule:      v1a.ConversionRule{Strategy: v1a.StrategyToLabel, ToLabel: &v1a.ToMetadataParams{HubPath: "spec.track", Key: "track", RestoreOnReverse: true, Serialization: "String"}},
			SpokeSpec: map[string]any{}},
		{Name: v1a.StrategyFromAnnotation,
			HubProps: map[string]extv1.JSONSchemaProps{}, SpokeProps: map[string]extv1.JSONSchemaProps{"note": strProp()},
			Rule:      lossy(v1a.ConversionRule{Strategy: v1a.StrategyFromAnnotation, FromAnnotation: &v1a.FromMetadataParams{SpokePath: "spec.note", Key: "scale.dco.example.org/note", Serialization: "String", StashOnReverse: true}}),
			SpokeSpec: map[string]any{"note": "hi"}},
		{Name: v1a.StrategyFromLabel,
			HubProps: map[string]extv1.JSONSchemaProps{}, SpokeProps: map[string]extv1.JSONSchemaProps{"lane": strProp()},
			Rule:      lossy(v1a.ConversionRule{Strategy: v1a.StrategyFromLabel, FromLabel: &v1a.FromLabelParams{SpokePath: "spec.lane", Key: "lane", Serialization: "String", StashOnReverse: true}}),
			SpokeSpec: map[string]any{"lane": "gold"}},
		{Name: v1a.StrategyDefaultValue,
			HubProps: map[string]extv1.JSONSchemaProps{"minReplicas": intProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{},
			Rule:      lossy(v1a.ConversionRule{Strategy: v1a.StrategyDefaultValue, DefaultValue: &v1a.DefaultValueParams{Path: "spec.minReplicas", ExistsOn: v1a.SideHub, Default: extv1.JSON{Raw: []byte(`1`)}}}),
			SpokeSpec: map[string]any{}},
		{Name: v1a.StrategyConstant,
			HubProps: map[string]extv1.JSONSchemaProps{"fixed": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{},
			Rule:      lossy(v1a.ConversionRule{Strategy: v1a.StrategyConstant, Constant: &v1a.ConstantParams{Path: "spec.fixed", ExistsOn: v1a.SideHub, Value: extv1.JSON{Raw: []byte(`"x"`)}}}),
			SpokeSpec: map[string]any{}},
		{Name: v1a.StrategyDelete,
			HubProps: map[string]extv1.JSONSchemaProps{}, SpokeProps: map[string]extv1.JSONSchemaProps{"debug": boolProp()},
			Rule:      lossy(v1a.ConversionRule{Strategy: v1a.StrategyDelete, Delete: &v1a.DeleteParams{Path: "spec.debug", ExistsOn: v1a.SideSpoke}}),
			SpokeSpec: map[string]any{"debug": true}},
		{Name: v1a.StrategyJSONPatch,
			HubProps: map[string]extv1.JSONSchemaProps{"flag": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"flag": strProp()},
			Rule: lossy(v1a.ConversionRule{Strategy: v1a.StrategyJSONPatch, JSONPatch: &v1a.JSONPatchParams{
				HubToSpoke: []v1a.JSONPatchOp{{Op: "replace", Path: "/spec/flag", Value: pj(`"spoke"`)}},
				SpokeToHub: []v1a.JSONPatchOp{{Op: "replace", Path: "/spec/flag", Value: pj(`"hub"`)}},
			}}),
			SpokeSpec: map[string]any{"flag": "spoke"}},
		{Name: v1a.StrategyScalarToFields,
			HubProps: map[string]extv1.JSONSchemaProps{"size": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"sizeValue": strProp(), "sizeUnit": strProp()},
			Rule: lossy(v1a.ConversionRule{Strategy: v1a.StrategyScalarToFields, ScalarToFields: &v1a.ScalarToFieldsParams{
				HubPath: "spec.size", Pattern: `^(?P<value>\d+)(?P<unit>[A-Za-z]+)$`,
				SpokeFields: map[string]string{"value": "spec.sizeValue", "unit": "spec.sizeUnit"}, JoinTemplate: "{{.value}}{{.unit}}", LosslessOverride: true,
			}}),
			SpokeSpec: map[string]any{"sizeValue": "10", "sizeUnit": "Gi"}},
		{Name: v1a.StrategyFieldsToScalar,
			HubProps: map[string]extv1.JSONSchemaProps{"capValue": strProp(), "capUnit": strProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"cap": strProp()},
			Rule: lossy(v1a.ConversionRule{Strategy: v1a.StrategyFieldsToScalar, FieldsToScalar: &v1a.FieldsToScalarParams{
				HubFields: map[string]string{"value": "spec.capValue", "unit": "spec.capUnit"}, SpokePath: "spec.cap",
				Pattern: `^(?P<value>\d+)(?P<unit>[A-Za-z]+)$`, JoinTemplate: "{{.value}}{{.unit}}", LosslessOverride: true,
			}}),
			SpokeSpec: map[string]any{"cap": "10Gi"}},
		{Name: v1a.StrategyCEL,
			HubProps: map[string]extv1.JSONSchemaProps{"packed": intProp()}, SpokeProps: map[string]extv1.JSONSchemaProps{"bitHigh": intProp(), "bitLow": intProp()},
			Rule: lossy(v1a.ConversionRule{Strategy: v1a.StrategyCEL, CEL: &v1a.CELParams{
				HubPaths: []string{"spec.packed"}, SpokePaths: []string{"spec.bitHigh", "spec.bitLow"},
				HubToSpoke: `{"spec.bitHigh": int(object.spec.packed) / 256, "spec.bitLow": int(object.spec.packed) % 256}`,
				SpokeToHub: `{"spec.packed": int(object.spec.bitHigh) * 256 + int(object.spec.bitLow)}`,
			}}),
			SpokeSpec: map[string]any{"bitHigh": 4, "bitLow": 1}},
	}
}

func mergeProps(dst map[string]extv1.JSONSchemaProps, src map[string]extv1.JSONSchemaProps) {
	for k, v := range src {
		dst[k] = v
	}
}

func mergeSpec(dst map[string]any, src map[string]any) {
	for k, v := range src {
		dst[k] = v
	}
}

// Assign picks minN–maxN slots per spoke so every catalog strategy appears
// at least once across the fleet. Requires 2*targets*maxN >= catalog size.
func Assign(targets, minN, maxN int, seed int64) ([][]Slot, [][]Slot, error) {
	all := slots()
	if minN < 1 || maxN < minN {
		return nil, nil, fmt.Errorf("strategies-min/max invalid")
	}
	if maxN > len(all) {
		maxN = len(all)
	}
	if minN > len(all) {
		minN = len(all)
	}
	if 2*targets*maxN < len(all) {
		return nil, nil, fmt.Errorf("cannot cover all %d strategies: need 2*targets*strategies-max >= %d (got %d); increase --targets or --strategies-max", len(all), len(all), 2*targets*maxN)
	}
	rng := rand.New(rand.NewSource(seed))
	v1, v2 := make([][]Slot, targets), make([][]Slot, targets)
	next := 0
	for i := 0; i < targets; i++ {
		n1 := minN + rng.Intn(maxN-minN+1)
		n2 := minN + rng.Intn(maxN-minN+1)
		v1[i] = pick(all, n1, &next, rng)
		v2[i] = pick(all, n2, &next, rng)
	}
	ensureCoverage(v1, v2, all, maxN)
	return v1, v2, nil
}

func ensureCoverage(v1, v2 [][]Slot, all []Slot, maxN int) {
	seen := map[v1a.Strategy]bool{}
	mark := func(rows [][]Slot) {
		for _, row := range rows {
			for _, s := range row {
				seen[s.Name] = true
			}
		}
	}
	mark(v1)
	mark(v2)
	has := func(row []Slot, name v1a.Strategy) bool {
		for _, s := range row {
			if s.Name == name {
				return true
			}
		}
		return false
	}
	tryAdd := func(row *[]Slot, s Slot) bool {
		if has(*row, s.Name) || len(*row) >= maxN {
			return false
		}
		*row = append(*row, s)
		return true
	}
	for _, s := range all {
		if seen[s.Name] {
			continue
		}
		placed := false
		for i := range v1 {
			if tryAdd(&v1[i], s) || tryAdd(&v2[i], s) {
				seen[s.Name] = true
				placed = true
				break
			}
		}
		if !placed {
			v1[len(v1)-1] = append(v1[len(v1)-1], s)
			seen[s.Name] = true
		}
	}
}

func pick(all []Slot, n int, next *int, rng *rand.Rand) []Slot {
	out := make([]Slot, 0, n)
	used := map[v1a.Strategy]bool{}
	for len(out) < n {
		s := all[*next%len(all)]
		*next++
		if used[s.Name] {
			if len(used) == len(all) {
				break
			}
			continue
		}
		used[s.Name] = true
		out = append(out, s)
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}
