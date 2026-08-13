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
	"fmt"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func nLeafSchema(n int) extv1.JSONSchemaProps {
	props := make(map[string]extv1.JSONSchemaProps, n)
	for i := 0; i < n; i++ {
		props[fmt.Sprintf("f%d", i)] = strSchema()
	}
	return objSchema(props)
}

func nRenameRules(n int) []Rule {
	rules := make([]Rule, n)
	for i := 0; i < n; i++ {
		p := ParsePath(fmt.Sprintf("f%d", i))
		rules[i] = Rule{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: p, SpokePath: p}}
	}
	return rules
}

func nLeafObject(n int) map[string]any {
	obj := make(map[string]any, n)
	for i := 0; i < n; i++ {
		obj[fmt.Sprintf("f%d", i)] = fmt.Sprintf("v%d", i)
	}
	return obj
}

func volumeItemSchema() extv1.JSONSchemaProps {
	return objSchema(map[string]extv1.JSONSchemaProps{
		"name":   strSchema(),
		"sizeGB": intSchema(),
	})
}

func volumeItemSpokeSchema() extv1.JSONSchemaProps {
	return objSchema(map[string]extv1.JSONSchemaProps{
		"name": strSchema(),
		"size": intSchema(),
	})
}

func forEachVolumeRules() RuleSet {
	return RuleSet{
		HubVersion: "v2", SpokeVersion: "v1",
		Rules: []Rule{{
			Strategy: StrategyForEach,
			Params: ForEachParams{
				HubItemsPath: ParsePath("volumes"), SpokeItemsPath: ParsePath("volumes"),
				Rules: []Rule{
					{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("name"), SpokePath: ParsePath("name")}},
					{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("sizeGB"), SpokePath: ParsePath("size")}},
				},
			},
		}},
	}
}

func volumesObject(n int) map[string]any {
	items := make([]any, n)
	for i := 0; i < n; i++ {
		items[i] = map[string]any{"name": fmt.Sprintf("vol-%d", i), "sizeGB": i}
	}
	return map[string]any{"volumes": items}
}

func zoneItemSchema() extv1.JSONSchemaProps {
	return objSchema(map[string]extv1.JSONSchemaProps{
		"name": strSchema(),
		"cidr": strSchema(),
	})
}

func zonesArrayObject(n int) map[string]any {
	items := make([]any, n)
	for i := 0; i < n; i++ {
		items[i] = map[string]any{"name": fmt.Sprintf("z%d", i), "cidr": fmt.Sprintf("10.0.%d.0/24", i)}
	}
	return map[string]any{"zones": items}
}

func zonesMapObject(n int) map[string]any {
	m := make(map[string]any, n)
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("z%d", i)] = map[string]any{"cidr": fmt.Sprintf("10.0.%d.0/24", i)}
	}
	return map[string]any{"zones": m}
}

func mustCompilePlan(rs RuleSet, hub, spoke *extv1.JSONSchemaProps) *Plan {
	plan, _, err := Compile(rs, hub, spoke)
	if err != nil {
		panic(err)
	}
	return plan
}
