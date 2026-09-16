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
	"sort"
	"strings"
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func mustNormalize(t *testing.T, in extv1.JSONSchemaProps) *extv1.JSONSchemaProps {
	t.Helper()
	out, err := NormalizeSchema(&in)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return out
}

func leafPaths(schema *extv1.JSONSchemaProps) []string {
	var out []string
	for _, l := range flattenSchema(schema) {
		out = append(out, l.Path.String())
	}
	sort.Strings(out)
	return out
}

// The whole point, stated as one test: a node carrying an allOf used to be
// opaque, so every field declared right there in the same node was
// invisible to coverage analysis and unreachable by any rule.
func TestNormalize_AllOfNoLongerHidesDeclaredFields(t *testing.T) {
	minLen := int64(1)
	in := objSchema(map[string]extv1.JSONSchemaProps{
		"backup": {
			Type: "object",
			Properties: map[string]extv1.JSONSchemaProps{
				"bucket": strSchema(),
				"region": strSchema(),
			},
			AllOf: []extv1.JSONSchemaProps{{
				Required:   []string{"bucket"},
				Properties: map[string]extv1.JSONSchemaProps{"bucket": {MinLength: &minLen}},
			}},
		},
	})

	before := leafPaths(&in)
	if !reflect.DeepEqual(before, []string{"backup"}) {
		t.Fatalf("fixture is wrong: before normalisation the whole node should be one opaque leaf, got %v", before)
	}

	after := leafPaths(mustNormalize(t, in))
	if !reflect.DeepEqual(after, []string{"backup.bucket", "backup.region"}) {
		t.Fatalf("after normalisation = %v, want the two declared fields", after)
	}
}

func TestNormalize_MergesRequiredAndEnum(t *testing.T) {
	out := mustNormalize(t, objSchema(map[string]extv1.JSONSchemaProps{
		"tier": {Type: "string", Enum: []extv1.JSON{jsonRaw(`"a"`), jsonRaw(`"b"`), jsonRaw(`"c"`)}},
		"name": strSchema(),
	}, "name"))

	// Nothing to merge; the schema must survive unchanged.
	if len(out.Required) != 1 || out.Required[0] != "name" {
		t.Fatalf("required = %v, want [name]", out.Required)
	}

	withAllOf := objSchema(map[string]extv1.JSONSchemaProps{
		"tier": {Type: "string", Enum: []extv1.JSON{jsonRaw(`"a"`), jsonRaw(`"b"`), jsonRaw(`"c"`)}},
		"name": strSchema(),
	}, "name")
	withAllOf.AllOf = []extv1.JSONSchemaProps{{
		Required: []string{"tier"},
		// allOf means "and", so the permitted vocabulary is the
		// intersection, not the union.
		Properties: map[string]extv1.JSONSchemaProps{
			"tier": {Enum: []extv1.JSON{jsonRaw(`"b"`), jsonRaw(`"c"`), jsonRaw(`"d"`)}},
		},
	}}

	merged := mustNormalize(t, withAllOf)
	if got := merged.Required; !reflect.DeepEqual(got, []string{"name", "tier"}) {
		t.Fatalf("required = %v, want the union [name tier]", got)
	}
	tier := merged.Properties["tier"]
	var vals []string
	for _, e := range tier.Enum {
		vals = append(vals, string(e.Raw))
	}
	if !reflect.DeepEqual(vals, []string{`"b"`, `"c"`}) {
		t.Fatalf("enum = %v, want the intersection [\"b\" \"c\"]", vals)
	}
}

// A contradiction must be an error naming both sides, not a silent
// last-writer-wins: the apiserver rejects every object against such a
// schema, and guessing which half the author meant would make the engine's
// field set disagree with what the cluster accepts.
func TestNormalize_ConflictingConstraintsAreAnError(t *testing.T) {
	cases := map[string]struct {
		in   extv1.JSONSchemaProps
		want []string
	}{
		"type": {
			in: objSchema(map[string]extv1.JSONSchemaProps{
				"port": {Type: "integer", AllOf: []extv1.JSONSchemaProps{{Type: "string"}}},
			}),
			want: []string{"port", "integer", "string"},
		},
		"enum": {
			in: objSchema(map[string]extv1.JSONSchemaProps{
				"tier": {
					Type: "string", Enum: []extv1.JSON{jsonRaw(`"a"`)},
					AllOf: []extv1.JSONSchemaProps{{Enum: []extv1.JSON{jsonRaw(`"b"`)}}},
				},
			}),
			want: []string{"tier", "no value could ever satisfy both"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeSchema(&tc.in)
			if err == nil {
				t.Fatal("expected a conflict error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not mention %q: %v", want, err)
				}
			}
		})
	}
}

func TestNormalize_ResolvesLocalRef(t *testing.T) {
	ref := "#/definitions/Endpoint"
	in := objSchema(map[string]extv1.JSONSchemaProps{
		"primary": {Ref: &ref},
		"backup":  {Ref: &ref},
	})
	in.Definitions = extv1.JSONSchemaDefinitions{
		"Endpoint": objSchema(map[string]extv1.JSONSchemaProps{
			"host": strSchema(),
			"port": intSchema(),
		}),
	}

	got := leafPaths(mustNormalize(t, in))
	want := []string{"backup.host", "backup.port", "primary.host", "primary.port"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("leaves = %v, want %v", got, want)
	}
}

func TestNormalize_RejectsRemoteRef(t *testing.T) {
	ref := "https://example.org/schemas/endpoint.json"
	in := objSchema(map[string]extv1.JSONSchemaProps{"primary": {Ref: &ref}})
	_, err := NormalizeSchema(&in)
	if err == nil {
		t.Fatal("expected a remote $ref to be rejected: following one would make analysis depend on the network")
	}
	if !strings.Contains(err.Error(), "not local") {
		t.Fatalf("error should say why: %v", err)
	}
}

func TestNormalize_RejectsUnresolvableRef(t *testing.T) {
	ref := "#/definitions/Nope"
	in := objSchema(map[string]extv1.JSONSchemaProps{"primary": {Ref: &ref}})
	_, err := NormalizeSchema(&in)
	if err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("expected an error naming the missing definition, got %v", err)
	}
}

// Cycles have to be detected, not recursed into: the alternative is a
// stack overflow taking the process down, on input an author controls.
func TestNormalize_DetectsRefCycle(t *testing.T) {
	self := "#/definitions/Node"
	in := objSchema(map[string]extv1.JSONSchemaProps{"root": {Ref: &self}})
	in.Definitions = extv1.JSONSchemaDefinitions{
		"Node": objSchema(map[string]extv1.JSONSchemaProps{
			"name":  strSchema(),
			"child": {Ref: &self},
		}),
	}
	_, err := NormalizeSchema(&in)
	if err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("expected a cycle to be reported, got %v", err)
	}
}

func TestNormalize_RejectsRefWithSiblings(t *testing.T) {
	ref := "#/definitions/Endpoint"
	in := objSchema(map[string]extv1.JSONSchemaProps{
		// The type would be silently discarded by replacing the node.
		"primary": {Ref: &ref, Type: "object"},
	})
	in.Definitions = extv1.JSONSchemaDefinitions{"Endpoint": objSchema(map[string]extv1.JSONSchemaProps{"host": strSchema()})}
	_, err := NormalizeSchema(&in)
	if err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatalf("expected the discarded sibling to be named, got %v", err)
	}
}

// Normalisation must be a no-op for the overwhelming majority of schemas,
// which carry neither construct — otherwise every existing config would be
// analysed against a subtly different field set.
func TestNormalize_IsANoOpForAnOrdinarySchema(t *testing.T) {
	in := objSchema(map[string]extv1.JSONSchemaProps{
		"name":    strSchema(),
		"replica": intSchema(),
		"nested":  objSchema(map[string]extv1.JSONSchemaProps{"a": strSchema()}, "a"),
		"list":    arrSchema(objSchema(map[string]extv1.JSONSchemaProps{"b": strSchema()}), nil),
	}, "name")

	out := mustNormalize(t, in)
	if !reflect.DeepEqual(&in, out) {
		t.Fatalf("normalisation changed a schema with no $ref and no allOf:\n before: %+v\n after:  %+v", in, out)
	}
}

// Normalising twice must give the same answer as normalising once, because
// Compile normalises its own inputs and Analyze has already normalised the
// schemas it hands around.
func TestNormalize_IsIdempotent(t *testing.T) {
	minLen := int64(2)
	ref := "#/definitions/Endpoint"
	in := objSchema(map[string]extv1.JSONSchemaProps{
		"primary": {Ref: &ref},
		"backup": {
			Type:       "object",
			Properties: map[string]extv1.JSONSchemaProps{"bucket": strSchema()},
			AllOf:      []extv1.JSONSchemaProps{{Required: []string{"bucket"}, Properties: map[string]extv1.JSONSchemaProps{"bucket": {MinLength: &minLen}}}},
		},
	})
	in.Definitions = extv1.JSONSchemaDefinitions{"Endpoint": objSchema(map[string]extv1.JSONSchemaProps{"host": strSchema()})}

	once := mustNormalize(t, in)
	twice, err := NormalizeSchema(once)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if !reflect.DeepEqual(once, twice) {
		t.Fatalf("normalisation is not idempotent:\n once:  %+v\n twice: %+v", once, twice)
	}
}

// The passthrough tree is built from the same schemas, so a field only
// visible after normalisation must be recognised as declared — otherwise
// passthroughUnknownOp would treat it as an undeclared field and copy it
// verbatim, overwriting whatever a rule had just written there.
func TestNormalize_PassthroughTreeSeesMergedFields(t *testing.T) {
	minLen := int64(1)
	hub := *mustNormalize(t, objSchema(map[string]extv1.JSONSchemaProps{
		"backup": {
			Type:       "object",
			Properties: map[string]extv1.JSONSchemaProps{"bucket": strSchema()},
			AllOf:      []extv1.JSONSchemaProps{{Properties: map[string]extv1.JSONSchemaProps{"bucket": {MinLength: &minLen}}}},
		},
	}))
	tree := buildKnownTree(&hub)
	backup, ok := tree.children["backup"]
	if !ok {
		t.Fatal("the known tree has no backup node")
	}
	if _, ok := backup.children["bucket"]; !ok {
		t.Fatal("bucket is not in the known tree, so passthrough would treat it as an undeclared field and clobber a rule's output")
	}
}

// A union inside an allOf branch survives the merge. The engine reads
// oneOf/anyOf — unionConstruct decides opacity, branchMap maps the
// branches — so dropping one during normalisation would hand every later
// stage a schema the author did not write, silently.
func TestNormalize_CarriesAUnionUpFromAnAllOfBranch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func() *extv1.JSONSchemaProps
		got   func(*extv1.JSONSchemaProps) []extv1.JSONSchemaProps
	}{
		{
			name: "oneOf",
			build: func() *extv1.JSONSchemaProps {
				s := unionParent()
				s.AllOf = []extv1.JSONSchemaProps{{OneOf: []extv1.JSONSchemaProps{
					{Required: []string{"s3"}}, {Required: []string{"gcs"}},
				}}}
				return s
			},
			got: func(s *extv1.JSONSchemaProps) []extv1.JSONSchemaProps { return s.OneOf },
		},
		{
			name: "anyOf",
			build: func() *extv1.JSONSchemaProps {
				s := unionParent()
				s.AllOf = []extv1.JSONSchemaProps{{AnyOf: []extv1.JSONSchemaProps{
					{Required: []string{"s3"}}, {Required: []string{"gcs"}},
				}}}
				return s
			},
			got: func(s *extv1.JSONSchemaProps) []extv1.JSONSchemaProps { return s.AnyOf },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := NormalizeSchema(tc.build())
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if len(tc.got(out)) != 2 {
				t.Fatalf("%s was discarded by the merge: got %d branches, want 2", tc.name, len(tc.got(out)))
			}
			if len(out.AllOf) != 0 {
				t.Errorf("the allOf itself should be gone, got %d branches", len(out.AllOf))
			}
			// The whole point: the union is still visible to the code that
			// reads it.
			if c := unionConstruct(out); c != tc.name {
				t.Errorf("unionConstruct = %q, want %q", c, tc.name)
			}
		})
	}
}

// Two unions over one node cannot become one list: `allOf: [{oneOf: A}]`
// on a parent that already has a oneOf means "satisfies both", which no
// single oneOf expresses. An error naming it, for the same reason two
// conflicting types are an error rather than last-writer-wins.
func TestNormalize_RejectsTwoUnionsOverTheSameNode(t *testing.T) {
	s := unionParent()
	s.OneOf = []extv1.JSONSchemaProps{{Required: []string{"s3"}}}
	s.AllOf = []extv1.JSONSchemaProps{{OneOf: []extv1.JSONSchemaProps{{Required: []string{"gcs"}}}}}
	_, err := NormalizeSchema(s)
	if err == nil {
		t.Fatal("expected two unions over one node to be rejected")
	}
	if !strings.Contains(err.Error(), "oneOf") {
		t.Errorf("the error should name the construct, got %v", err)
	}
}

func unionParent() *extv1.JSONSchemaProps {
	return &extv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"s3":  {Type: "object", Properties: map[string]extv1.JSONSchemaProps{"bucket": {Type: "string"}}},
			"gcs": {Type: "object", Properties: map[string]extv1.JSONSchemaProps{"bucket": {Type: "string"}}},
		},
	}
}

// Enum intersection is over JSON *values*, not over the bytes they were
// written as. JSON Schema treats `{"a":1,"b":2}` and `{ "b": 2, "a": 1 }`
// as the same enum member; comparing Raw made them different and turned an
// enum that intersects perfectly well into a hard compile error.
func TestNormalize_IntersectsEnumsByValueNotByBytes(t *testing.T) {
	for _, tc := range []struct{ name, parent, branch string }{
		{"object key order", `{"a":1,"b":2}`, `{ "b": 2, "a": 1 }`},
		{"whitespace", `["x","y"]`, `[ "x", "y" ]`},
		{"number formatting", `1`, `1.0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &extv1.JSONSchemaProps{
				Enum:  []extv1.JSON{{Raw: []byte(tc.parent)}},
				AllOf: []extv1.JSONSchemaProps{{Enum: []extv1.JSON{{Raw: []byte(tc.branch)}}}},
			}
			out, err := NormalizeSchema(s)
			if err != nil {
				t.Fatalf("two spellings of the same value must intersect: %v", err)
			}
			if len(out.Enum) != 1 {
				t.Fatalf("enum = %v, want the one shared value", out.Enum)
			}
			// The parent's spelling is kept, so the surviving entry is a
			// representative rather than a re-serialised approximation.
			if string(out.Enum[0].Raw) != tc.parent {
				t.Errorf("kept %q, want the parent's own entry %q", out.Enum[0].Raw, tc.parent)
			}
		})
	}
}

// ...and genuinely disjoint enums are still a contradiction.
func TestNormalize_StillRejectsDisjointEnums(t *testing.T) {
	s := &extv1.JSONSchemaProps{
		Enum:  []extv1.JSON{{Raw: []byte(`"a"`)}},
		AllOf: []extv1.JSONSchemaProps{{Enum: []extv1.JSON{{Raw: []byte(`"b"`)}}}},
	}
	if _, err := NormalizeSchema(s); err == nil {
		t.Fatal("expected disjoint enums to be rejected")
	}
}

// Nullability intersects. "Must be a string" and "may be null" is not a
// contradiction — it is a string, because a value has to satisfy every
// branch. Kubernetes rejects nullable on a junctor outright, so this is
// only reachable through the exported offline path, where the old
// behaviour was a hard error on a well-defined schema.
func TestNormalize_IntersectsNullability(t *testing.T) {
	for _, tc := range []struct {
		name           string
		parentNullable bool
		branch         extv1.JSONSchemaProps
		want           bool
	}{
		{"nullable branch cannot widen a non-nullable parent", false, extv1.JSONSchemaProps{Nullable: true}, false},
		{"a typed non-nullable branch narrows a nullable parent", true, extv1.JSONSchemaProps{Type: "string"}, false},
		{"both nullable stays nullable", true, extv1.JSONSchemaProps{Type: "string", Nullable: true}, true},
		{"an untyped branch says nothing about nullability", true, extv1.JSONSchemaProps{Enum: []extv1.JSON{{Raw: []byte(`"a"`)}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &extv1.JSONSchemaProps{
				Type: "string", Nullable: tc.parentNullable,
				AllOf: []extv1.JSONSchemaProps{tc.branch},
			}
			out, err := NormalizeSchema(s)
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if out.Nullable != tc.want {
				t.Errorf("Nullable = %v, want %v", out.Nullable, tc.want)
			}
		})
	}
}
