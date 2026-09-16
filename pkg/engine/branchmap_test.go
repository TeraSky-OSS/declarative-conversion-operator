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
	"reflect"
	"strings"
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// unionSchema builds the shape structural_facts_test.go proves a CRD
// actually accepts: every branch is a declared, optional property, and the
// oneOf only says which of them may be set.
func unionSchema(discriminator string, branches map[string]extv1.JSONSchemaProps) extv1.JSONSchemaProps {
	props := map[string]extv1.JSONSchemaProps{}
	var oneOf []extv1.JSONSchemaProps
	for name, schema := range branches {
		props[name] = schema
		oneOf = append(oneOf, extv1.JSONSchemaProps{Required: []string{name}})
	}
	if discriminator != "" {
		props[discriminator] = strSchema()
	}
	s := objSchema(props)
	s.OneOf = oneOf
	return s
}

func hubUnion() extv1.JSONSchemaProps {
	return objSchema(map[string]extv1.JSONSchemaProps{
		"backup": unionSchema("backend", map[string]extv1.JSONSchemaProps{
			"s3":  objSchema(map[string]extv1.JSONSchemaProps{"bucket": strSchema()}),
			"gcs": objSchema(map[string]extv1.JSONSchemaProps{"bucket": strSchema()}),
		}),
	})
}

func spokeUnion() extv1.JSONSchemaProps {
	return objSchema(map[string]extv1.JSONSchemaProps{
		"backup": unionSchema("backend", map[string]extv1.JSONSchemaProps{
			"objectStore": objSchema(map[string]extv1.JSONSchemaProps{"name": strSchema()}),
			"googleStore": objSchema(map[string]extv1.JSONSchemaProps{"name": strSchema()}),
		}),
	})
}

func branchMapRuleSet() RuleSet {
	return RuleSet{HubVersion: "v2", SpokeVersion: "v1", Rules: []Rule{{
		Strategy: StrategyBranchMap,
		Params: BranchMapParams{
			HubPath: ParsePath("backup"), SpokePath: ParsePath("backup"),
			Discriminator: "backend",
			Branches: []BranchMapping{
				{
					HubBranch: "s3", SpokeBranch: "objectStore",
					Rules: []Rule{{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("bucket"), SpokePath: ParsePath("name")}}},
				},
				{
					HubBranch: "gcs", SpokeBranch: "googleStore",
					Rules: []Rule{{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("bucket"), SpokePath: ParsePath("name")}}},
				},
			},
		},
	}}}
}

// The headline: the branches of a union are ordinary fields now, so the
// leftover scan sees every leaf inside them — and branchMap has to claim
// them all, or a perfectly complete config would report as uncovered.
func TestBranchMap_CoverageClaimsEveryBranchLeaf(t *testing.T) {
	hub, spoke := hubUnion(), spokeUnion()
	_, diags, err := Compile(branchMapRuleSet(), &hub, &spoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("expected a complete branch mapping to compile cleanly, got %v", errs)
	}
}

func TestBranchMap_ConvertsBothDirections(t *testing.T) {
	hub, spoke := hubUnion(), spokeUnion()
	plan, diags, err := Compile(branchMapRuleSet(), &hub, &spoke)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("compile errors: %v", errs)
	}

	hubObj := map[string]any{"backup": map[string]any{"backend": "s3", "s3": map[string]any{"bucket": "logs"}}}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: hubObj})
	if err != nil {
		t.Fatalf("hub->spoke: %v", err)
	}
	want := map[string]any{"backend": "objectStore", "objectStore": map[string]any{"name": "logs"}}
	if got := out["backup"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("hub->spoke backup = %#v, want %#v", got, want)
	}

	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: map[string]any{"backup": want}})
	if err != nil {
		t.Fatalf("spoke->hub: %v", err)
	}
	wantBack := map[string]any{"backend": "s3", "s3": map[string]any{"bucket": "logs"}}
	if got := back["backup"]; !reflect.DeepEqual(got, wantBack) {
		t.Fatalf("round trip = %#v, want %#v", got, wantBack)
	}
}

// Fail closed, both ways. A union with nothing set, or with two branches
// set, is an object the destination's own oneOf would reject — but it
// would reject it with a message about the schema, long after the
// conversion that produced it.
func TestBranchMap_NoBranchAndMultipleBranchesAreRuntimeErrors(t *testing.T) {
	hub, spoke := hubUnion(), spokeUnion()
	plan, _, err := Compile(branchMapRuleSet(), &hub, &spoke)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	_, err = Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: map[string]any{
		"backup": map[string]any{"backend": "s3"},
	}})
	if err == nil || !strings.Contains(err.Error(), "no branch is set") {
		t.Fatalf("expected a no-branch error, got %v", err)
	}
	for _, want := range []string{"gcs", "s3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name the branches it expected; %q missing from %v", want, err)
		}
	}

	_, err = Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: map[string]any{
		"backup": map[string]any{"s3": map[string]any{"bucket": "a"}, "gcs": map[string]any{"bucket": "b"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "all set") {
		t.Fatalf("expected a multiple-branch error, got %v", err)
	}
}

// A union object can carry properties that are not branches. branchMap
// writes each branch at its own path rather than replacing the object, so
// a rule covering one of those cannot be clobbered — whichever order the
// two rules are declared in.
func TestBranchMap_DoesNotClobberSiblingsOfTheUnion(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"backup": withProp(unionSchema("", map[string]extv1.JSONSchemaProps{
			"s3": objSchema(map[string]extv1.JSONSchemaProps{"bucket": strSchema()}),
		}), "retentionDays", intSchema()),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"backup": withProp(unionSchema("", map[string]extv1.JSONSchemaProps{
			"objectStore": objSchema(map[string]extv1.JSONSchemaProps{"bucket": strSchema()}),
		}), "keepDays", intSchema()),
	})

	rename := Rule{Strategy: StrategyFieldRename, Params: FieldRenameParams{
		HubPath: ParsePath("backup.retentionDays"), SpokePath: ParsePath("backup.keepDays"),
	}}
	branch := Rule{Strategy: StrategyBranchMap, Params: BranchMapParams{
		HubPath: ParsePath("backup"), SpokePath: ParsePath("backup"),
		Branches: []BranchMapping{{HubBranch: "s3", SpokeBranch: "objectStore"}},
	}}

	obj := map[string]any{"backup": map[string]any{"retentionDays": int64(7), "s3": map[string]any{"bucket": "logs"}}}
	want := map[string]any{"keepDays": int64(7), "objectStore": map[string]any{"bucket": "logs"}}

	// Both orders, because "ordering can never matter" is a property this
	// engine claims and a wholesale object write would break.
	for name, rules := range map[string][]Rule{
		"branch first": {branch, rename},
		"rename first": {rename, branch},
	} {
		t.Run(name, func(t *testing.T) {
			plan, diags, err := Compile(RuleSet{Rules: rules}, &hub, &spoke)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
				t.Fatalf("compile errors: %v", errs)
			}
			out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: obj})
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			if got := out["backup"]; !reflect.DeepEqual(got, want) {
				t.Fatalf("backup = %#v, want %#v", got, want)
			}
		})
	}
}

// A hub branch the rule does not name is data the conversion would drop.
// It surfaces through the ordinary coverage machinery, as an error, which
// is stronger than a lossiness warning would be.
func TestBranchMap_UnmappedBranchIsUncovered(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"backup": unionSchema("", map[string]extv1.JSONSchemaProps{
			"s3":    objSchema(map[string]extv1.JSONSchemaProps{"bucket": strSchema()}),
			"azure": objSchema(map[string]extv1.JSONSchemaProps{"container": strSchema()}),
		}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"backup": unionSchema("", map[string]extv1.JSONSchemaProps{
			"objectStore": objSchema(map[string]extv1.JSONSchemaProps{"bucket": strSchema()}),
		}),
	})
	rs := RuleSet{Rules: []Rule{{Strategy: StrategyBranchMap, Params: BranchMapParams{
		HubPath: ParsePath("backup"), SpokePath: ParsePath("backup"),
		Branches: []BranchMapping{{HubBranch: "s3", SpokeBranch: "objectStore"}},
	}}}}

	_, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	errs := diagMessages(diags, SeverityError)
	var sawAzure bool
	for _, msg := range errs {
		if strings.Contains(msg, "backup.azure") {
			sawAzure = true
		}
	}
	if !sawAzure {
		t.Fatalf("expected the unmapped hub branch to be reported as uncovered, got %v", errs)
	}
}

// Collapsing two hub branches onto one spoke branch is expressible and
// sometimes intended, but it cannot be undone: coming back, the engine
// cannot tell which one it started from.
func TestBranchMap_NonInjectiveIsLossyInTheCollapsingDirection(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"backup": unionSchema("", map[string]extv1.JSONSchemaProps{
			"s3":  objSchema(map[string]extv1.JSONSchemaProps{"bucket": strSchema()}),
			"gcs": objSchema(map[string]extv1.JSONSchemaProps{"bucket": strSchema()}),
		}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"backup": unionSchema("", map[string]extv1.JSONSchemaProps{
			"objectStore": objSchema(map[string]extv1.JSONSchemaProps{"bucket": strSchema()}),
		}),
	})
	rs := RuleSet{Rules: []Rule{{Strategy: StrategyBranchMap, Params: BranchMapParams{
		HubPath: ParsePath("backup"), SpokePath: ParsePath("backup"),
		Branches: []BranchMapping{
			{HubBranch: "s3", SpokeBranch: "objectStore"},
			{HubBranch: "gcs", SpokeBranch: "objectStore"},
		},
	}}}}

	_, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var sawLossy bool
	for _, msg := range diagMessages(diags, SeverityError) {
		if strings.Contains(msg, "spoke->hub conversion is lossy") {
			sawLossy = true
		}
	}
	if !sawLossy {
		t.Fatalf("expected the collapse to be reported as lossy spoke->hub, got %v", diagMessages(diags, SeverityError))
	}

	// "Expressible" has to mean it actually compiles once the loss is
	// acknowledged. The claim map records paths, not the rules holding
	// them, so claiming the shared spoke branch once per mapping would
	// have reported this rule as conflicting with itself — leaving no way
	// to write a collapse at all.
	rs.Rules[0].AcknowledgeLossy = true
	plan, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("an acknowledged collapse should compile cleanly, got %v", errs)
	}

	// Compiling is only half of "expressible". Both hub branches must
	// reach the shared spoke branch...
	for _, hubBranch := range []string{"s3", "gcs"} {
		hubObj := map[string]any{"backup": map[string]any{hubBranch: map[string]any{"bucket": "logs"}}}
		out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: hubObj})
		if err != nil {
			t.Fatalf("hub->spoke from %q: %v", hubBranch, err)
		}
		want := map[string]any{"objectStore": map[string]any{"bucket": "logs"}}
		if got := out["backup"]; !reflect.DeepEqual(got, want) {
			t.Errorf("hub->spoke from %q = %#v, want %#v", hubBranch, got, want)
		}
	}

	// ...and the way back must work at all. branchMapOp identifies the
	// active branch by counting entries whose srcBranch is present, so a
	// second compiled entry for the shared spoke branch made a valid
	// single-branch object look like two branches set at once and failed
	// every conversion back. The collapse compiled and could never convert.
	spokeObj := map[string]any{"backup": map[string]any{"objectStore": map[string]any{"bucket": "logs"}}}
	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: spokeObj})
	if err != nil {
		t.Fatalf("spoke->hub on a valid single-branch object: %v", err)
	}
	// The first mapping wins — the deterministic reading of "cannot tell
	// which hub branch it started from", and the reason this is lossy.
	want := map[string]any{"s3": map[string]any{"bucket": "logs"}}
	if got := back["backup"]; !reflect.DeepEqual(got, want) {
		t.Errorf("spoke->hub = %#v, want %#v (the first mapping wins)", got, want)
	}
}

func TestBranchMap_RejectsABranchThatIsNotADeclaredProperty(t *testing.T) {
	hub, spoke := hubUnion(), spokeUnion()
	rs := RuleSet{Rules: []Rule{{Strategy: StrategyBranchMap, Params: BranchMapParams{
		HubPath: ParsePath("backup"), SpokePath: ParsePath("backup"),
		Branches: []BranchMapping{{HubBranch: "azure", SpokeBranch: "objectStore"}},
	}}}}
	_, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var saw bool
	for _, msg := range diagMessages(diags, SeverityError) {
		if strings.Contains(msg, "azure") && strings.Contains(msg, "declared property") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected an undeclared branch to be rejected, got %v", diagMessages(diags, SeverityError))
	}
}

func TestBranchMap_RejectsAnUndeclaredDiscriminator(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"backup": unionSchema("", map[string]extv1.JSONSchemaProps{
			"s3": objSchema(map[string]extv1.JSONSchemaProps{"bucket": strSchema()}),
		}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"backup": unionSchema("", map[string]extv1.JSONSchemaProps{
			"objectStore": objSchema(map[string]extv1.JSONSchemaProps{"bucket": strSchema()}),
		}),
	})
	rs := RuleSet{Rules: []Rule{{Strategy: StrategyBranchMap, Params: BranchMapParams{
		HubPath: ParsePath("backup"), SpokePath: ParsePath("backup"),
		Discriminator: "kind",
		Branches:      []BranchMapping{{HubBranch: "s3", SpokeBranch: "objectStore"}},
	}}}}
	_, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var saw bool
	for _, msg := range diagMessages(diags, SeverityError) {
		if strings.Contains(msg, "discriminator") && strings.Contains(msg, "kind") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected an undeclared discriminator to be rejected, got %v", diagMessages(diags, SeverityError))
	}
}

// Each side's diagnostic has to name that side's path. The two are
// routinely different — a union that was moved as well as reshaped — and
// an error about the spoke that quotes the hub path sends the reader to a
// schema where the field is not missing.
func TestBranchMap_DiscriminatorDiagnosticNamesTheFailingSidesPath(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"backup": unionSchema("backend", map[string]extv1.JSONSchemaProps{
			"s3": objSchema(map[string]extv1.JSONSchemaProps{"bucket": strSchema()}),
		}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"legacyBackup": unionSchema("", map[string]extv1.JSONSchemaProps{
			"objectStore": objSchema(map[string]extv1.JSONSchemaProps{"bucket": strSchema()}),
		}),
	})
	rs := RuleSet{Rules: []Rule{{Strategy: StrategyBranchMap, Params: BranchMapParams{
		HubPath: ParsePath("backup"), SpokePath: ParsePath("legacyBackup"),
		Discriminator: "backend",
		Branches:      []BranchMapping{{HubBranch: "s3", SpokeBranch: "objectStore"}},
	}}}}
	_, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var saw bool
	for _, msg := range diagMessages(diags, SeverityError) {
		if !strings.Contains(msg, "discriminator") {
			continue
		}
		saw = true
		if !strings.Contains(msg, "spoke union at \"legacyBackup\"") {
			t.Errorf("the spoke diagnostic must name the spoke path, got %q", msg)
		}
	}
	if !saw {
		t.Fatalf("expected the missing spoke discriminator to be reported, got %v", diagMessages(diags, SeverityError))
	}
}

// Before this strategy existed, a union node was one opaque leaf: every
// field inside it was invisible, and jsonPatch was the only way to touch
// one. This asserts the un-hiding directly.
func TestUnion_BranchesAreOrdinaryLeavesNow(t *testing.T) {
	hub := hubUnion()
	var paths []string
	for _, l := range flattenSchema(&hub) {
		paths = append(paths, l.Path.String())
	}
	want := map[string]bool{"backup.backend": true, "backup.s3.bucket": true, "backup.gcs.bucket": true}
	if len(paths) != len(want) {
		t.Fatalf("leaves = %v, want the three declared fields", paths)
	}
	for _, p := range paths {
		if !want[p] {
			t.Fatalf("unexpected leaf %q in %v", p, paths)
		}
	}
}

// The int-or-string shape has no type of its own, so the engine still
// cannot describe it and it stays one opaque leaf named after the
// construct. De-opaquing unions must not have de-opaqued this.
func TestUnion_UntypedStaysOpaque(t *testing.T) {
	yes := true
	node := extv1.JSONSchemaProps{
		XIntOrString: yes,
		AnyOf:        []extv1.JSONSchemaProps{{Type: "integer"}, {Type: "string"}},
	}
	schema := objSchema(map[string]extv1.JSONSchemaProps{"port": node})
	leaves := flattenSchema(&schema)
	if len(leaves) != 1 || leaves[0].Path.String() != "port" {
		t.Fatalf("leaves = %+v, want one leaf for port", leaves)
	}
	if !leaves[0].Opaque || leaves[0].Construct != "anyOf" {
		t.Fatalf("leaf = %+v, want an opaque leaf named after anyOf", leaves[0])
	}
}
