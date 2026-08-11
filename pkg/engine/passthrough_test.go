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
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// TestCompile_PreservesFieldsAbsentFromSchemaEntirely is a regression test
// for a real bug caught by `convctl test --live` against an actual
// Crossplane cluster: Crossplane injects a standard status.conditions
// (and spec.crossplane) shape into every generated CRD version, but a
// real controller compiles its plan from the XRD's own authored
// openAPIV3Schema, which never declares those fields at all. Before the
// passthrough fix, such a field was invisible to flattenSchema, never
// claimed, never auto-covered, and never flagged -- Convert's output
// starts empty, so it was just silently dropped on every conversion, in
// both directions.
func TestCompile_PreservesFieldsAbsentFromSchemaEntirely(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"status": objSchema(map[string]extv1.JSONSchemaProps{"phase": strSchema()}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"status": objSchema(map[string]extv1.JSONSchemaProps{"state": strSchema()}),
	})
	rs := RuleSet{HubVersion: "v2", SpokeVersion: "v1", Rules: []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("status.phase"), SpokePath: ParsePath("status.state")}},
	}}
	plan, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	in := map[string]any{
		"status": map[string]any{
			"phase": "Ready",
			"conditions": []any{
				map[string]any{"type": "Synced", "status": "True"},
			},
		},
	}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
	if err != nil {
		t.Fatalf("convert h2s: %v", err)
	}
	outStatus, _ := out["status"].(map[string]any)
	if outStatus["state"] != "Ready" {
		t.Fatalf("expected the ruled field to still convert, got %v", out)
	}
	inConditions := in["status"].(map[string]any)["conditions"]
	if !reflect.DeepEqual(outStatus["conditions"], inConditions) {
		t.Fatalf("expected the schema-absent 'conditions' field to pass through unchanged, got %v", outStatus["conditions"])
	}

	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: out})
	if err != nil {
		t.Fatalf("convert s2h: %v", err)
	}
	backStatus, _ := back["status"].(map[string]any)
	if backStatus["phase"] != "Ready" {
		t.Fatalf("round trip mismatch on the ruled field: %v", back)
	}
	if !reflect.DeepEqual(backStatus["conditions"], inConditions) {
		t.Fatalf("expected 'conditions' to survive the round trip unchanged, got %v", backStatus["conditions"])
	}
}

// TestCompile_PassthroughDoesNotClobberKindOrMetadata guards against a
// naive top-level implementation of the same fix that would also try to
// "pass through" kind/apiVersion/metadata -- those are Convert's own
// responsibility (see TestConvert_PreservesKindAndMetadata) and must stay
// exactly as Convert already handles them, not be re-copied by this Op.
func TestCompile_PassthroughDoesNotClobberKindOrMetadata(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{"a": strSchema()})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{"a": strSchema()})
	rs := RuleSet{HubVersion: "v2", SpokeVersion: "v1"}
	plan, _, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	in := map[string]any{
		"kind":       "XWidget",
		"apiVersion": "example.org/v2",
		"metadata":   map[string]any{"name": "x"},
		"a":          "hello",
	}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if _, ok := out["apiVersion"]; ok {
		t.Fatalf("expected apiVersion to be left for the caller to set, got %v", out["apiVersion"])
	}
	if out["kind"] != "XWidget" {
		t.Fatalf("expected kind preserved via Convert's own passthrough, got %v", out["kind"])
	}
}

// TestCompile_UndeclaredNestedObjectPreservedWholesale mirrors the exact
// real-world shape: an object field (status) that IS declared on both
// sides gains a child (conditions) that is declared on NEITHER side.
func TestCompile_UndeclaredNestedObjectPreservedWholesale(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"status": objSchema(map[string]extv1.JSONSchemaProps{
			"phase": strSchema(),
		}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"status": objSchema(map[string]extv1.JSONSchemaProps{
			"phase": strSchema(),
		}),
	})
	rs := RuleSet{HubVersion: "v2", SpokeVersion: "v1"}
	plan, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	in := map[string]any{
		"status": map[string]any{
			"phase": "Ready",
			"conditions": []any{
				map[string]any{"type": "Synced", "status": "True"},
			},
		},
	}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	status, ok := out["status"].(map[string]any)
	if !ok {
		t.Fatalf("expected a status object in the output, got %v", out["status"])
	}
	if status["phase"] != "Ready" {
		t.Fatalf("expected phase auto-covered as before, got %v", status["phase"])
	}
	if !reflect.DeepEqual(status["conditions"], in["status"].(map[string]any)["conditions"]) {
		t.Fatalf("expected conditions preserved wholesale, got %v", status["conditions"])
	}
}

// TestCompile_PassthroughDoesNotClobberRenamedField is a regression test
// for a real bug a CodeRabbit review caught: building passthroughUnknownOp
// from only the direction's source schema left it blind to a rule's
// destination field when that field is declared solely on the OTHER
// side. Here hub declares only "spec.old" and spoke declares only
// "spec.new"; a FieldRename maps old->new. From the hub schema alone,
// "spec.new" looks like just another undeclared field, so if the hub
// input happens to also contain a stale same-named "spec.new" of its own,
// the old (source-schema-only) implementation would let this Op run last
// and silently overwrite the rename's freshly-converted value with that
// stale input data.
func TestCompile_PassthroughDoesNotClobberRenamedField(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{"old": strSchema()}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{"new": strSchema()}),
	})
	rs := RuleSet{HubVersion: "v2", SpokeVersion: "v1", Rules: []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("spec.old"), SpokePath: ParsePath("spec.new")}},
	}}
	plan, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	in := map[string]any{
		"spec": map[string]any{
			"old": "renamed-value",
			// Not part of the hub schema at all -- e.g. leftover data from
			// a previous shape, or a field some other layer injected. Its
			// key happens to collide with the rename's spoke destination.
			"new": "stale-should-be-overwritten",
		},
	}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	spec, _ := out["spec"].(map[string]any)
	if spec["new"] != "renamed-value" {
		t.Fatalf("expected the rename's converted value to win over stale undeclared input data, got %v", spec["new"])
	}
}

func TestMergeKnownTrees(t *testing.T) {
	a := &knownTree{children: map[string]*knownTree{
		"onlyA": {},
		"both":  {children: map[string]*knownTree{"fromA": {}}},
	}}
	b := &knownTree{children: map[string]*knownTree{
		"onlyB": {},
		"both":  {children: map[string]*knownTree{"fromB": {}}},
	}}
	merged := mergeKnownTrees(a, b)

	for _, k := range []string{"onlyA", "onlyB", "both"} {
		if _, ok := merged.children[k]; !ok {
			t.Fatalf("expected %q to be present in the merged tree", k)
		}
	}
	both := merged.children["both"]
	if _, ok := both.children["fromA"]; !ok {
		t.Fatalf("expected merged 'both' to still know about 'fromA'")
	}
	if _, ok := both.children["fromB"]; !ok {
		t.Fatalf("expected merged 'both' to also know about 'fromB'")
	}
}

func TestMergeKnownTrees_BothEmpty(t *testing.T) {
	merged := mergeKnownTrees(&knownTree{}, &knownTree{})
	if len(merged.children) != 0 {
		t.Fatalf("expected an empty merge of two empty trees, got %#v", merged.children)
	}
}

func TestBuildKnownTree(t *testing.T) {
	schema := objSchema(map[string]extv1.JSONSchemaProps{
		"phase": strSchema(),
		"nested": objSchema(map[string]extv1.JSONSchemaProps{
			"inner": strSchema(),
		}),
		"blob": openMapSchema(),
	})
	tree := buildKnownTree(&schema)

	if _, ok := tree.children["phase"]; !ok {
		t.Fatalf("expected 'phase' to be a known child")
	}
	if len(tree.children["phase"].children) != 0 {
		t.Fatalf("expected 'phase' to be a known leaf with no children")
	}
	nested, ok := tree.children["nested"]
	if !ok || len(nested.children) == 0 {
		t.Fatalf("expected 'nested' to be a known object with its own children")
	}
	if _, ok := nested.children["inner"]; !ok {
		t.Fatalf("expected 'nested.inner' to be known")
	}
	if len(tree.children["blob"].children) != 0 {
		t.Fatalf("expected an opaque map to be treated as a known leaf, not recursed into")
	}
	if _, ok := tree.children["missing"]; ok {
		t.Fatalf("did not expect an undeclared key to appear in the tree")
	}
}

func TestBuildKnownTree_NilSchema(t *testing.T) {
	tree := buildKnownTree(nil)
	if len(tree.children) != 0 {
		t.Fatalf("expected an empty tree for a nil schema")
	}
}

// TestCollectUnknown_NonObjectAtKnownContainerPath guards against a panic
// or spurious emit when a declared-object field's runtime value isn't
// actually a map (e.g. explicit null, or a malformed sample) -- it should
// be skipped, not treated as a source of unknown children.
func TestCollectUnknown_NonObjectAtKnownContainerPath(t *testing.T) {
	schema := objSchema(map[string]extv1.JSONSchemaProps{
		"status": objSchema(map[string]extv1.JSONSchemaProps{"phase": strSchema()}),
	})
	tree := buildKnownTree(&schema)

	var emitted []FieldPath
	collectUnknown(tree, map[string]any{"status": nil}, nil, func(p FieldPath, v any) {
		emitted = append(emitted, p)
	})
	if len(emitted) != 0 {
		t.Fatalf("expected no emissions for a non-object value at a known container path, got %v", emitted)
	}
}
