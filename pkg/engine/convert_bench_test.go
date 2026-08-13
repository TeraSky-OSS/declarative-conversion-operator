/*
Copyright 2026 The declarative-conversion-operator Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

    10|Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package engine

import (
	"fmt"
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func BenchmarkConvert_ForEach_ArrayLength(b *testing.B) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"volumes": arrSchema(volumeItemSchema(), nil)})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"volumes": arrSchema(volumeItemSpokeSchema(), nil)})
	plan := mustCompilePlan(forEachVolumeRules(), &hub, &spoke)
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			obj := volumesObject(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: obj}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkConvert_ArrayToMapByKey_ArrayLength(b *testing.B) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"zones": arrSchema(zoneItemSchema(), nil)})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"zones": openMapSchema()})
	rs := RuleSet{
		HubVersion: "v2", SpokeVersion: "v1",
		Rules: []Rule{{
			Strategy:         StrategyArrayToMapByKey,
			AcknowledgeLossy: true,
			Params:           ArrayToMapByKeyParams{HubPath: ParsePath("zones"), SpokePath: ParsePath("zones"), KeyField: "name"},
		}},
	}
	plan := mustCompilePlan(rs, &hub, &spoke)
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			obj := zonesArrayObject(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: obj}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkConvert_MapToArrayByKey_MapSize(b *testing.B) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"zones": openMapSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"zones": arrSchema(zoneItemSchema(), nil)})
	rs := RuleSet{
		HubVersion: "v2", SpokeVersion: "v1",
		Rules: []Rule{{
			Strategy:         StrategyMapToArrayByKey,
			AcknowledgeLossy: true,
			Params:           MapToArrayByKeyParams{HubPath: ParsePath("zones"), SpokePath: ParsePath("zones"), KeyField: "name"},
		}},
	}
	plan := mustCompilePlan(rs, &hub, &spoke)
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			obj := zonesMapObject(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: obj}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkConvert_JSONPatch(b *testing.B) {
	flag := strSchema()
	hub := objSchema(map[string]extv1.JSONSchemaProps{"a": flag, "b": flag, "c": flag})
	spoke := hub
	onePlan := mustCompilePlan(RuleSet{
		HubVersion: "v2", SpokeVersion: "v1",
		Rules: []Rule{jsonPatchReplaceRule("a", "x", "y")},
	}, &hub, &spoke)
	multiPlan := mustCompilePlan(RuleSet{
		HubVersion: "v2", SpokeVersion: "v1",
		Rules: []Rule{
			jsonPatchReplaceRule("a", "x", "y"),
			jsonPatchReplaceRule("b", "x", "y"),
			jsonPatchReplaceRule("c", "x", "y"),
		},
	}, &hub, &spoke)
	obj := map[string]any{"a": "0", "b": "1", "c": "2"}

	b.Run("ops=1", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := Convert(ConvertInput{Plan: onePlan, Direction: HubToSpoke, Object: obj}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ops=3", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := Convert(ConvertInput{Plan: multiPlan, Direction: HubToSpoke, Object: obj}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkRouter_SpokeToSpoke_vs_HubHop(b *testing.B) {
	hubSchema := objSchema(map[string]extv1.JSONSchemaProps{"volumes": arrSchema(volumeItemSchema(), nil)})
	v1Spoke := objSchema(map[string]extv1.JSONSchemaProps{"volumes": arrSchema(volumeItemSpokeSchema(), nil)})
	v2Spoke := objSchema(map[string]extv1.JSONSchemaProps{"volumes": arrSchema(objSchema(map[string]extv1.JSONSchemaProps{
		"name":   strSchema(),
		"sizeMi": intSchema(),
	}), nil)})
	v1 := mustCompilePlan(forEachVolumeRules(), &hubSchema, &v1Spoke)
	v2 := mustCompilePlan(RuleSet{
		HubVersion: "v3", SpokeVersion: "v2",
		Rules: []Rule{{
			Strategy: StrategyForEach,
			Params: ForEachParams{
				HubItemsPath: ParsePath("volumes"), SpokeItemsPath: ParsePath("volumes"),
				Rules: []Rule{
					{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("name"), SpokePath: ParsePath("name")}},
					{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("sizeGB"), SpokePath: ParsePath("sizeMi")}},
				},
			},
		}},
	}, &hubSchema, &v2Spoke)
	router := &Router{Hub: "v3", Plans: map[string]*Plan{"v1": v1, "v2": v2}}
	hubObj := volumesObject(1000)
	spokeObj, err := Convert(ConvertInput{Plan: v1, Direction: HubToSpoke, Object: hubObj})
	if err != nil {
		b.Fatal(err)
	}

	b.Run("hub_to_spoke", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := router.Convert(hubObj, "v3", "v1"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("spoke_to_spoke", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := router.Convert(spokeObj, "v1", "v2"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func jsonPatchReplaceRule(field, h2s, s2h string) Rule {
	return Rule{
		Strategy: StrategyJSONPatch, AcknowledgeLossy: true,
		Params: JSONPatchParams{
			HubToSpoke: []JSONPatchOp{{Op: "replace", Path: "/" + field, Value: h2s}},
			SpokeToHub: []JSONPatchOp{{Op: "replace", Path: "/" + field, Value: s2h}},
		},
	}
}
