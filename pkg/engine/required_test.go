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

// reqSchema builds a one-level spec schema with the named required fields.
func reqSchema(required []string, propNames ...string) *extv1.JSONSchemaProps {
	props := map[string]extv1.JSONSchemaProps{}
	for _, n := range propNames {
		props[n] = extv1.JSONSchemaProps{Type: "string"}
	}
	return &extv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"spec": {Type: "object", Required: required, Properties: props},
		},
	}
}

func codesOf(diags []Diagnostic) []string {
	var out []string
	for _, d := range diags {
		if d.Code != "" {
			out = append(out, d.Code)
		}
	}
	return out
}

func hasCode(diags []Diagnostic, code string) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestAnalyzeRequiredFields_VerdictPerClass(t *testing.T) {
	cases := []struct {
		name     string
		dest     *extv1.JSONSchemaProps
		src      *extv1.JSONSchemaProps
		rules    []Rule
		results  []RuleResult
		wantCode string // "" means no diagnostic
	}{
		{
			name:    "no rule writes it and no required source of the same name",
			dest:    reqSchema([]string{"region"}, "region", "size"),
			src:     reqSchema(nil, "size"),
			results: []RuleResult{{Index: 0, Strategy: StrategyFieldRename, HubPaths: []string{"spec.size"}, SpokePaths: []string{"spec.size"}}},
			// Nothing writes spec.region at all.
			wantCode: CodeRequiredFieldUnsatisfiable,
		},
		{
			name:     "written from a required source is always satisfied",
			dest:     reqSchema([]string{"region"}, "region"),
			src:      reqSchema([]string{"zone"}, "zone"),
			rules:    []Rule{{Strategy: StrategyFieldRename, SourceIndex: 0}},
			results:  []RuleResult{{Index: 0, Strategy: StrategyFieldRename, HubPaths: []string{"spec.zone"}, SpokePaths: []string{"spec.region"}}},
			wantCode: "",
		},
		{
			name:     "written from an optional source is conditional",
			dest:     reqSchema([]string{"region"}, "region"),
			src:      reqSchema(nil, "zone"),
			rules:    []Rule{{Strategy: StrategyFieldRename, SourceIndex: 0}},
			results:  []RuleResult{{Index: 0, Strategy: StrategyFieldRename, HubPaths: []string{"spec.zone"}, SpokePaths: []string{"spec.region"}}},
			wantCode: CodeRequiredFieldConditional,
		},
		{
			name:     "a when clause makes it conditional even from a required source",
			dest:     reqSchema([]string{"region"}, "region"),
			src:      reqSchema([]string{"zone"}, "zone"),
			rules:    []Rule{{Strategy: StrategyFieldRename, SourceIndex: 0, When: &RuleWhen{Path: ParsePath("spec.zone"), Equals: "eu"}}},
			results:  []RuleResult{{Index: 0, Strategy: StrategyFieldRename, HubPaths: []string{"spec.zone"}, SpokePaths: []string{"spec.region"}}},
			wantCode: CodeRequiredFieldConditional,
		},
		{
			name:     "Constant satisfies it out of nothing",
			dest:     reqSchema([]string{"region"}, "region"),
			src:      reqSchema(nil, "zone"),
			rules:    []Rule{{Strategy: StrategyConstant, SourceIndex: 0}},
			results:  []RuleResult{{Index: 0, Strategy: StrategyConstant, HubPaths: []string{"spec.zone"}, SpokePaths: []string{"spec.region"}}},
			wantCode: "",
		},
		{
			name:     "CEL cannot be proven either way",
			dest:     reqSchema([]string{"region"}, "region"),
			src:      reqSchema(nil, "zone"),
			rules:    []Rule{{Strategy: StrategyCEL, SourceIndex: 0}},
			results:  []RuleResult{{Index: 0, Strategy: StrategyCEL, HubPaths: []string{"spec.zone"}, SpokePaths: []string{"spec.region"}}},
			wantCode: CodeRequiredFieldUnprovable,
		},
		{
			name: "one unconditional rule beside a conditional one is enough",
			dest: reqSchema([]string{"region"}, "region"),
			src:  reqSchema([]string{"zone"}, "zone", "other"),
			rules: []Rule{
				{Strategy: StrategyFieldRename, SourceIndex: 0, When: &RuleWhen{Path: ParsePath("spec.zone"), Equals: "eu"}},
				{Strategy: StrategyFieldRename, SourceIndex: 1},
			},
			results: []RuleResult{
				{Index: 0, Strategy: StrategyFieldRename, HubPaths: []string{"spec.other"}, SpokePaths: []string{"spec.region"}},
				{Index: 1, Strategy: StrategyFieldRename, HubPaths: []string{"spec.zone"}, SpokePaths: []string{"spec.region"}},
			},
			wantCode: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := analyzeRequiredFields(tc.dest, tc.src, tc.rules, tc.results, "spoke")
			if tc.wantCode == "" {
				if len(got) != 0 {
					t.Fatalf("expected no diagnostic, got %v", codesOf(got))
				}
				return
			}
			if !hasCode(got, tc.wantCode) {
				t.Fatalf("expected %s, got %v", tc.wantCode, codesOf(got))
			}
			for _, d := range got {
				if d.Code != tc.wantCode {
					continue
				}
				if d.FieldPath == "" {
					t.Error("diagnostic carries no field path")
				}
				if !strings.Contains(d.Message, "spec.region") {
					t.Errorf("message does not name the field: %s", d.Message)
				}
			}
		})
	}
}

// A schema default satisfies a required field: the apiserver fills it in.
func TestAnalyzeRequiredFields_SchemaDefaultSatisfies(t *testing.T) {
	dest := &extv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"spec": {
				Type:     "object",
				Required: []string{"region"},
				Properties: map[string]extv1.JSONSchemaProps{
					"region": {Type: "string", Default: &extv1.JSON{Raw: []byte(`"eu"`)}},
					"size":   {Type: "string"},
				},
			},
		},
	}
	src := reqSchema(nil, "size")
	results := []RuleResult{{Index: 0, Strategy: StrategyFieldRename, HubPaths: []string{"spec.size"}, SpokePaths: []string{"spec.size"}}}
	if got := analyzeRequiredFields(dest, src, nil, results, "spoke"); len(got) != 0 {
		t.Errorf("a required field with a schema default needs no rule: %v", codesOf(got))
	}
}

// Required inside an object that may itself be absent is not unconditionally
// required — the apiserver only enforces it when the parent is present. This
// is the shape that would otherwise produce false positives everywhere.
func TestAnalyzeRequiredFields_RequiredInsideOptionalParentIsNotReported(t *testing.T) {
	dest := &extv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"spec": {
				Type: "object",
				Properties: map[string]extv1.JSONSchemaProps{
					"network": {
						Type:       "object",
						Required:   []string{"cidr"},
						Properties: map[string]extv1.JSONSchemaProps{"cidr": {Type: "string"}},
					},
					"size": {Type: "string"},
				},
			},
		},
	}
	src := reqSchema(nil, "size")
	// Nothing writes into spec.network, so the object will not be present
	// and its required child does not apply.
	results := []RuleResult{{Index: 0, Strategy: StrategyFieldRename, HubPaths: []string{"spec.size"}, SpokePaths: []string{"spec.size"}}}
	if got := analyzeRequiredFields(dest, src, nil, results, "spoke"); len(got) != 0 {
		t.Errorf("required inside an absent optional parent must not be reported: %v", codesOf(got))
	}
}

// ...but once a rule writes into that parent, the object exists in the
// output and its required children do apply.
func TestAnalyzeRequiredFields_RequiredInsideAWrittenParentIsReported(t *testing.T) {
	dest := &extv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"spec": {
				Type: "object",
				Properties: map[string]extv1.JSONSchemaProps{
					"network": {
						Type:     "object",
						Required: []string{"cidr"},
						Properties: map[string]extv1.JSONSchemaProps{
							"cidr": {Type: "string"},
							"mtu":  {Type: "string"},
						},
					},
				},
			},
		},
	}
	src := reqSchema(nil, "mtu")
	results := []RuleResult{{Index: 0, Strategy: StrategyFieldRename, HubPaths: []string{"spec.mtu"}, SpokePaths: []string{"spec.network.mtu"}}}
	got := analyzeRequiredFields(dest, src, nil, results, "spoke")
	if !hasCode(got, CodeRequiredFieldUnsatisfiable) {
		t.Errorf("a rule writes spec.network.mtu, so spec.network exists and spec.network.cidr applies: %v", codesOf(got))
	}
}

// The analysis runs per direction, and a rule set can be complete one way
// and not the other.
func TestAnalyzeRequiredFields_DirectionsAreIndependent(t *testing.T) {
	spoke := reqSchema([]string{"region"}, "region")
	hub := reqSchema(nil, "region")
	results := []RuleResult{{Index: 0, Strategy: StrategyFieldRename, HubPaths: []string{"spec.region"}, SpokePaths: []string{"spec.region"}}}
	rules := []Rule{{Strategy: StrategyFieldRename, SourceIndex: 0}}

	// hub -> spoke: the spoke requires it, the hub does not guarantee it.
	if got := analyzeRequiredFields(spoke, hub, rules, results, "spoke"); !hasCode(got, CodeRequiredFieldConditional) {
		t.Errorf("hub→spoke should be conditional: %v", codesOf(got))
	}
	// spoke -> hub: the hub requires nothing.
	if got := analyzeRequiredFields(hub, spoke, rules, results, "hub"); len(got) != 0 {
		t.Errorf("spoke→hub requires nothing and should be clean: %v", codesOf(got))
	}
}

// A rule whose destination IS the containing object sits between the two
// cases above. The object will exist, so its required child applies — but
// the rule may well be producing that key itself, so calling it
// unsatisfiable is a false alarm and dropping the leaf hides a real risk.
// The honest verdict is that it cannot be proven either way.
func TestAnalyzeRequiredFields_AWholesaleParentWriteIsUnprovable(t *testing.T) {
	dest := &extv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"spec": {
				Type: "object",
				Properties: map[string]extv1.JSONSchemaProps{
					"network": {
						Type:       "object",
						Required:   []string{"cidr"},
						Properties: map[string]extv1.JSONSchemaProps{"cidr": {Type: "string"}},
					},
				},
			},
		},
	}
	src := reqSchema(nil, "network")
	// The rule's destination is the object itself, not a field inside it.
	results := []RuleResult{{Index: 0, Strategy: StrategyScalarToObject, HubPaths: []string{"spec.network"}, SpokePaths: []string{"spec.network"}}}
	got := analyzeRequiredFields(dest, src, nil, results, "spoke")
	if hasCode(got, CodeRequiredFieldUnsatisfiable) {
		t.Errorf("a rule that produces the whole object may well supply the key; calling it unsatisfiable is a false alarm: %v", codesOf(got))
	}
	if !hasCode(got, CodeRequiredFieldUnprovable) {
		t.Errorf("a wholesale parent write must still be reported as unprovable, not skipped: %v", codesOf(got))
	}
}

// A `when` clause is knowable; the output of a CEL rule is not. A rule that
// is both must be reported for the thing that can be established — it may
// not fire at all, in which case nothing it would have produced matters.
func TestAnalyzeRequiredFields_ConditionalBeatsUnprovable(t *testing.T) {
	dest := reqSchema([]string{"region"}, "region")
	src := reqSchema(nil, "region")
	results := []RuleResult{{Index: 0, Strategy: StrategyCEL, HubPaths: []string{"spec.region"}, SpokePaths: []string{"spec.region"}}}
	when := &RuleWhen{}
	rules := []Rule{{Strategy: StrategyCEL, SourceIndex: 0, When: when}}

	got := analyzeRequiredFields(dest, src, rules, results, "spoke")
	if !hasCode(got, CodeRequiredFieldConditional) {
		t.Errorf("a conditional CEL rule must be reported as conditional, not downgraded to a warning about output that may never be produced: %v", codesOf(got))
	}

	// With a second, unconditional rule writing the same field, the
	// stronger guarantee wins and the verdict drops back to unprovable.
	results = append(results, RuleResult{Index: 1, Strategy: StrategyCEL, HubPaths: []string{"spec.region"}, SpokePaths: []string{"spec.region"}})
	rules = append(rules, Rule{Strategy: StrategyCEL, SourceIndex: 1})
	got = analyzeRequiredFields(dest, src, rules, results, "spoke")
	if hasCode(got, CodeRequiredFieldConditional) {
		t.Errorf("an unconditional rule also writes the field, so the conditional one is not the binding verdict: %v", codesOf(got))
	}
	if !hasCode(got, CodeRequiredFieldUnprovable) {
		t.Errorf("want the unprovable warning from the unconditional CEL rule: %v", codesOf(got))
	}
}
