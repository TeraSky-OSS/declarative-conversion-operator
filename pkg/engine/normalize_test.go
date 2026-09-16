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
