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

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apischema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// This file is the investigation 16.2 asks for, written as a test rather
// than as prose: what does apiextensions ACTUALLY accept in a CRD schema?
//
// It matters because the answer bounds the whole normalisation problem. It
// is asserted against the apiserver's own validator — the same
// k8s.io/apiextensions-apiserver code a real cluster runs — so the answer
// cannot drift away from reality without this failing. Designing against a
// reading of the documentation would have been guessing.
//
// The findings, and what the engine does with each:
//
//  1. `$ref` is rejected outright, anywhere in a CRD schema. So resolving
//     it is not about CRDs at all — it is about the hand-written YAML
//     convctl is handed offline, where a `$ref` is the author's mistake
//     and deserves a clear message rather than an opaque node.
//
//  2. A logical junctor (`allOf`, `anyOf`, `oneOf`, `not`) may not carry
//     structural fields: no `type`, no `additionalProperties`, no
//     `default`, no `nullable`.
//
//  3. Every property named inside a junctor must ALSO be declared in the
//     structural schema outside it. This is the decisive one: it means a
//     junctor can only ever add value validations to fields the engine can
//     already see. There is no such thing, in a legal CRD, as a field that
//     exists only inside an `allOf` or a `oneOf` branch.
//
// (3) is why the engine's treatment of these nodes was wrong rather than
// merely incomplete: `schemaConstruct` marked any node carrying one as
// opaque, hiding properties that were fully declared right there in the
// same node.

// validateAsCRDSchema runs a v1 JSONSchemaProps through the apiserver's own
// structural-schema checks and returns the errors, joined.
func validateAsCRDSchema(t *testing.T, in *extv1.JSONSchemaProps) string {
	t.Helper()
	var internal apiextensions.JSONSchemaProps
	if err := extv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(in, &internal, nil); err != nil {
		t.Fatalf("converting to the internal type: %v", err)
	}
	s, err := apischema.NewStructural(&internal)
	if err != nil {
		// NewStructural itself rejects the unsupported keywords, $ref
		// among them, before structurality is even considered.
		return err.Error()
	}
	var msgs []string
	for _, e := range apischema.ValidateStructural(field.NewPath("root"), s) {
		msgs = append(msgs, e.Error())
	}
	return strings.Join(msgs, "; ")
}

func TestApiextensions_RejectsRef(t *testing.T) {
	ref := "#/definitions/Thing"
	got := validateAsCRDSchema(t, &extv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"thing": {Ref: &ref},
		},
	})
	if !strings.Contains(got, "$ref") {
		t.Fatalf("expected $ref to be rejected, got %q", got)
	}
}

func TestApiextensions_AcceptsAllOfCarryingOnlyValueValidations(t *testing.T) {
	minLen := int64(1)
	got := validateAsCRDSchema(t, &extv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"name": {Type: "string"},
		},
		// A constraint on a property that IS declared outside.
		AllOf: []extv1.JSONSchemaProps{{
			Properties: map[string]extv1.JSONSchemaProps{"name": {MinLength: &minLen}},
		}},
	})
	if got != "" {
		t.Fatalf("expected an allOf of pure value validations to be accepted, got %q", got)
	}
}

// The decisive fact. A CRD cannot introduce a field inside a junctor, so
// flattening through one can never reveal a field the engine could not
// already see — which is why normalisation is about un-hiding what is
// already there, not about discovering anything new.
func TestApiextensions_RejectsAPropertyDeclaredOnlyInsideAllOf(t *testing.T) {
	minLen := int64(1)
	got := validateAsCRDSchema(t, &extv1.JSONSchemaProps{
		Type:       "object",
		Properties: map[string]extv1.JSONSchemaProps{"name": {Type: "string"}},
		AllOf: []extv1.JSONSchemaProps{{
			Properties: map[string]extv1.JSONSchemaProps{"undeclared": {MinLength: &minLen}},
		}},
	})
	if !strings.Contains(got, "undeclared") {
		t.Fatalf("expected a property declared only inside allOf to be rejected, got %q", got)
	}
}

func TestApiextensions_RejectsStructuralFieldsInsideAJunctor(t *testing.T) {
	for name, branch := range map[string]extv1.JSONSchemaProps{
		"type":                 {Type: "string"},
		"additionalProperties": {AdditionalProperties: &extv1.JSONSchemaPropsOrBool{Allows: true}},
		"nullable":             {Nullable: true},
	} {
		t.Run(name, func(t *testing.T) {
			got := validateAsCRDSchema(t, &extv1.JSONSchemaProps{
				Type:       "object",
				Properties: map[string]extv1.JSONSchemaProps{"name": {Type: "string"}},
				AllOf:      []extv1.JSONSchemaProps{branch},
			})
			if got == "" {
				t.Fatalf("expected %q inside allOf to be rejected as non-structural", name)
			}
		})
	}
}

// What a union actually looks like in a legal CRD, and the shape 16.1's
// branchMap is built against: every branch is an ordinary declared
// property, and the oneOf only says which of them may be present.
func TestApiextensions_AcceptsAPresenceDiscriminatedUnion(t *testing.T) {
	got := validateAsCRDSchema(t, &extv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"backend": {Type: "string", Enum: []extv1.JSON{{Raw: []byte(`"s3"`)}, {Raw: []byte(`"gcs"`)}}},
			"s3":      {Type: "object", Properties: map[string]extv1.JSONSchemaProps{"bucket": {Type: "string"}}},
			"gcs":     {Type: "object", Properties: map[string]extv1.JSONSchemaProps{"bucket": {Type: "string"}}},
		},
		OneOf: []extv1.JSONSchemaProps{
			{Required: []string{"s3"}},
			{Required: []string{"gcs"}},
		},
	})
	if got != "" {
		t.Fatalf("expected a presence-discriminated union to be accepted, got %q", got)
	}
}
