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

// VersionSchema describes one version of a convertible resource.
type VersionSchema struct {
	// Name is the version name, e.g. "v1beta1".
	Name string
	// Schema is the OpenAPI v3 schema for this version, typically the
	// "spec"-rooted schema (callers decide exactly what subtree they hand
	// in; the engine treats it as the root of all paths it reasons about).
	Schema *extv1.JSONSchemaProps
	// Served indicates whether this version is currently served.
	Served bool
	// Storage indicates this is the hub/referenceable/storage version.
	Storage bool
}

// ResourceDescriptor identifies the resource a SchemaSource describes, used
// only for logging and report correlation.
type ResourceDescriptor struct {
	Kind       string // e.g. "XRD", "CRD"
	Name       string
	Generation int64
}

// SchemaSource is the seam that keeps the engine agnostic of where schemas
// come from. Today pkg/xrdadapter implements this by reading a Crossplane
// CompositeResourceDefinition; a future native-CRD adapter would implement
// the same interface without any change to this package.
type SchemaSource interface {
	Versions() ([]VersionSchema, error)
	Describe() ResourceDescriptor
}

// FieldKind classifies a leaf or opaque node found while flattening a
// schema, used for type-compatibility checks and lossiness reasoning.
type FieldKind string

const (
	FieldKindString  FieldKind = "string"
	FieldKindInteger FieldKind = "integer"
	FieldKindNumber  FieldKind = "number"
	FieldKindBoolean FieldKind = "boolean"
	FieldKindArray   FieldKind = "array"
	// FieldKindObject marks an object node that the engine did not descend
	// into further because it is opaque (see Opaque below) — e.g. an
	// additionalProperties:true map or a preserve-unknown-fields subtree.
	// Structured object nodes with known properties are not themselves
	// emitted as leaves; their properties are flattened instead.
	FieldKindObject  FieldKind = "object"
	FieldKindUnknown FieldKind = "unknown"
)

// LeafField is one addressable, schema-resolvable field discovered while
// flattening a schema for coverage analysis.
type LeafField struct {
	Path   FieldPath
	Kind   FieldKind
	Schema *extv1.JSONSchemaProps
	// Opaque is true for object/array subtrees the engine treats as a
	// single indivisible unit rather than descending further — namely
	// additionalProperties:true maps, x-kubernetes-preserve-unknown-fields
	// subtrees, and arrays/maps whose item/value schema is itself unknown.
	// A rule must claim an opaque field wholesale; partial-field reasoning
	// inside it is out of scope for v1.
	Opaque bool
	// Required mirrors the schema's required-ness of this field at its
	// immediate parent.
	Required bool
}

// flattenSchema walks schema and returns every leaf field it can address,
// recursing into structured objects and stopping (marking Opaque) at maps
// with no statically-known key set.
func flattenSchema(schema *extv1.JSONSchemaProps) []LeafField {
	if schema == nil {
		return nil
	}
	var out []LeafField
	flattenInto(schema, nil, nil, &out)
	return out
}

func flattenInto(schema *extv1.JSONSchemaProps, path FieldPath, requiredSet map[string]bool, out *[]LeafField) {
	if schema == nil {
		return
	}
	kind, opaque := classify(schema)

	switch {
	case opaque:
		*out = append(*out, LeafField{Path: path.Clone(), Kind: kind, Schema: schema, Opaque: true, Required: requiredSet[lastSegment(path)]})
	case kind == FieldKindObject && len(schema.Properties) == 0:
		// A non-opaque object with zero declared properties has no fields
		// to flatten — nothing to cover, nothing to claim.
	case kind == FieldKindObject && len(schema.Properties) > 0:
		req := map[string]bool{}
		for _, r := range schema.Required {
			req[r] = true
		}
		for name, propSchema := range schema.Properties {
			propSchema := propSchema
			flattenInto(&propSchema, append(path.Clone(), name), req, out)
		}
	case kind == FieldKindArray && schema.Items != nil && schema.Items.Schema != nil:
		// Arrays are addressed as a single field for top-level rule
		// resolution (e.g. ForEach's ItemsPath); we don't emit per-index
		// leaves since indices aren't stable field paths. The array field
		// itself is emitted as a leaf so coverage-checking still sees it.
		*out = append(*out, LeafField{Path: path.Clone(), Kind: kind, Schema: schema, Required: requiredSet[lastSegment(path)]})
	default:
		*out = append(*out, LeafField{Path: path.Clone(), Kind: kind, Schema: schema, Required: requiredSet[lastSegment(path)]})
	}
}

func lastSegment(p FieldPath) string {
	if len(p) == 0 {
		return ""
	}
	return p[len(p)-1]
}

// classify determines the FieldKind of a schema node and whether it should
// be treated as opaque (see LeafField.Opaque).
func classify(schema *extv1.JSONSchemaProps) (FieldKind, bool) {
	if schema.XPreserveUnknownFields != nil && *schema.XPreserveUnknownFields {
		return FieldKindObject, true
	}
	switch schema.Type {
	case "object":
		if len(schema.Properties) > 0 {
			return FieldKindObject, false
		}
		// additionalProperties:true (Allows=true, Schema=nil), or a typed
		// map (Schema set) — either way the key set isn't statically known.
		if schema.AdditionalProperties != nil {
			return FieldKindObject, true
		}
		// An object with no declared properties and no additionalProperties
		// is a structurally empty object (zero fields), not an opaque
		// blob — Kubernetes structural-schema rules require an object to
		// declare properties or additionalProperties to accept any fields
		// at all, so there is nothing ambiguous left to hide here.
		return FieldKindObject, false
	case "array":
		if schema.Items == nil || schema.Items.Schema == nil {
			return FieldKindArray, true
		}
		return FieldKindArray, false
	case "string":
		return FieldKindString, false
	case "integer":
		return FieldKindInteger, false
	case "number":
		return FieldKindNumber, false
	case "boolean":
		return FieldKindBoolean, false
	default:
		return FieldKindUnknown, true
	}
}

// lookupPath walks schema following path's segments and returns the schema
// node at that path, or an error if any segment doesn't resolve (missing
// property, or descending into a non-object/array).
func lookupPath(schema *extv1.JSONSchemaProps, path FieldPath) (*extv1.JSONSchemaProps, error) {
	cur := schema
	for i, seg := range path {
		if cur == nil {
			return nil, fmt.Errorf("path %q: no schema available at %q", path, FieldPath(path[:i]))
		}
		if cur.Properties == nil {
			return nil, fmt.Errorf("path %q: %q has no properties (found at %q)", path, seg, FieldPath(path[:i]))
		}
		next, ok := cur.Properties[seg]
		if !ok {
			return nil, fmt.Errorf("path %q: field %q not found under %q", path, seg, FieldPath(path[:i]))
		}
		cur = &next
	}
	return cur, nil
}
