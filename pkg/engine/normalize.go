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
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// NormalizeSchema rewrites a schema into the ordinary, junctor-free form
// the rest of this package reasons about: local `$ref` resolved, `allOf`
// merged into its parent. Its output is an ordinary JSONSchemaProps, so
// nothing downstream — flattenSchema, the resolvers, the leftover scan,
// the passthrough tree — needs to know it happened.
//
// **The result is an analysis artifact and is never written back to a
// cluster.** That is what makes the merge below tractable: it has to be
// faithful about the things the engine reads — the field set, its types
// and required-ness, enum vocabularies, and which subtrees are opaque —
// and it may drop the value validations nothing here ever looks at
// (patterns, bounds, formats), because the apiserver enforces those on its
// own and no conversion decision depends on them.
//
// What the two constructs actually mean in a CRD is pinned by
// structural_facts_test.go, against the apiserver's own validator:
//
//   - `$ref` is rejected outright in a CRD schema. Resolving it therefore
//     serves the offline path only — hand-written YAML handed to convctl —
//     where an unresolvable reference is an authoring mistake that deserves
//     a message rather than an opaque node.
//   - Every property named inside an `allOf` must also be declared outside
//     it. A junctor can only ever *constrain* fields the engine already
//     sees; it can never introduce one. Merging it is therefore never a
//     discovery, it is an un-hiding.
//
// A nil schema normalises to nil.
func NormalizeSchema(schema *extv1.JSONSchemaProps) (*extv1.JSONSchemaProps, error) {
	if schema == nil {
		return nil, nil //nolint:nilnil // a nil schema normalises to a nil schema; every caller already handles one, and a sentinel error would make "this version has no schema" an error condition it is not
	}
	n := &normalizer{root: schema, inFlight: map[string]bool{}}
	return n.node(schema, nil)
}

// NormalizedVersions reads a source's versions and normalises every
// schema, so callers that flatten schemas outside Analyze — convctl's
// suggester and rehub — see exactly the field set Analyze reported on.
// Without it, a schema carrying an allOf would flatten one way inside the
// report and another way in the tool reading it.
func NormalizedVersions(src SchemaSource) ([]VersionSchema, error) {
	versions, err := src.Versions()
	if err != nil {
		return nil, err
	}
	out := make([]VersionSchema, 0, len(versions))
	for _, v := range versions {
		normalized, err := NormalizeSchema(v.Schema)
		if err != nil {
			return nil, fmt.Errorf("version %q: %w", v.Name, err)
		}
		v.Schema = normalized
		out = append(out, v)
	}
	return out, nil
}

type normalizer struct {
	root *extv1.JSONSchemaProps
	// inFlight is the set of $ref pointers currently being resolved.
	// A reference that reappears while its own resolution is still on the
	// stack is a cycle, and the only alternative to detecting it is
	// recursing until the stack runs out.
	inFlight map[string]bool
}

func (n *normalizer) node(s *extv1.JSONSchemaProps, path FieldPath) (*extv1.JSONSchemaProps, error) {
	if s == nil {
		return nil, nil //nolint:nilnil // see NormalizeSchema
	}
	if s.Ref != nil && *s.Ref != "" {
		return n.resolveRef(s, path)
	}

	out := s.DeepCopy()

	// allOf first, so anything it contributes is then walked like the rest.
	if len(out.AllOf) > 0 {
		branches := out.AllOf
		out.AllOf = nil
		for i := range branches {
			branch, err := n.node(&branches[i], path)
			if err != nil {
				return nil, err
			}
			if err := mergeSchema(out, branch, path, i); err != nil {
				return nil, err
			}
		}
	}

	if len(out.Properties) > 0 {
		props := make(map[string]extv1.JSONSchemaProps, len(out.Properties))
		for name, prop := range out.Properties {
			normalized, err := n.node(&prop, append(path.Clone(), name))
			if err != nil {
				return nil, err
			}
			if normalized != nil {
				props[name] = *normalized
			}
		}
		out.Properties = props
	}
	if out.Items != nil && out.Items.Schema != nil {
		normalized, err := n.node(out.Items.Schema, append(path.Clone(), "[]"))
		if err != nil {
			return nil, err
		}
		out.Items = &extv1.JSONSchemaPropsOrArray{Schema: normalized, JSONSchemas: out.Items.JSONSchemas}
	}
	if out.AdditionalProperties != nil && out.AdditionalProperties.Schema != nil {
		normalized, err := n.node(out.AdditionalProperties.Schema, append(path.Clone(), "{}"))
		if err != nil {
			return nil, err
		}
		out.AdditionalProperties = &extv1.JSONSchemaPropsOrBool{Allows: out.AdditionalProperties.Allows, Schema: normalized}
	}
	return out, nil
}

// resolveRef replaces a $ref node with the schema it points at.
//
// Only local references are resolvable, and that is not a simplification:
// Kubernetes structural schemas do not permit `$ref` at all, so anything
// this sees comes from a hand-written document, and following a remote one
// would mean fetching a URL during analysis — a correctness problem (the
// answer depends on the network) and a security one (a config could make
// the operator issue requests).
func (n *normalizer) resolveRef(s *extv1.JSONSchemaProps, path FieldPath) (*extv1.JSONSchemaProps, error) {
	ref := *s.Ref
	if !strings.HasPrefix(ref, "#") {
		return nil, fmt.Errorf("schema at %q: $ref %q is not local; only references within the same document (#/...) are supported, because resolving a remote one would make analysis depend on the network", pathOrRoot(path), ref)
	}
	if extra := refSiblings(s); len(extra) > 0 {
		return nil, fmt.Errorf("schema at %q: $ref %q also sets %s; a $ref node is replaced by what it points at, so those would be silently discarded — move them into the referenced schema", pathOrRoot(path), ref, strings.Join(extra, ", "))
	}
	if n.inFlight[ref] {
		return nil, fmt.Errorf("schema at %q: $ref %q is cyclic; it resolves, directly or indirectly, back to itself", pathOrRoot(path), ref)
	}

	target, err := resolvePointer(n.root, ref)
	if err != nil {
		return nil, fmt.Errorf("schema at %q: %w", pathOrRoot(path), err)
	}

	n.inFlight[ref] = true
	defer delete(n.inFlight, ref)
	return n.node(target, path)
}

// refSiblings names the fields set alongside a $ref that would be lost by
// replacing the node. Description and title are excluded: they carry no
// meaning the engine reads, and a documented reference is idiomatic.
func refSiblings(s *extv1.JSONSchemaProps) []string {
	stripped := s.DeepCopy()
	stripped.Ref = nil
	stripped.Description = ""
	stripped.Title = ""
	if stripped.Size() == 0 {
		return nil
	}
	// Marshal rather than reflect over sixty fields: the shape of the
	// remainder is exactly what a user needs to see, and JSON gives it for
	// free.
	raw, err := json.Marshal(stripped)
	if err != nil {
		return []string{"other fields"}
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		return []string{"other fields"}
	}
	names := make([]string, 0, len(asMap))
	for k := range asMap {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// resolvePointer walks an RFC 6901 JSON Pointer from the document root.
// Only the containers a JSONSchemaProps actually has are traversable:
// definitions, properties, items, and additionalProperties.
func resolvePointer(root *extv1.JSONSchemaProps, ref string) (*extv1.JSONSchemaProps, error) {
	ptr := strings.TrimPrefix(ref, "#")
	ptr = strings.TrimPrefix(ptr, "/")
	if ptr == "" {
		return root, nil
	}
	cur := root
	segs := strings.Split(ptr, "/")
	for i := 0; i < len(segs); i++ {
		seg := unescapePointerSegment(segs[i])
		switch seg {
		case "definitions", "properties":
			if i+1 >= len(segs) {
				return nil, fmt.Errorf("$ref %q ends at %q without naming a schema", ref, seg)
			}
			key := unescapePointerSegment(segs[i+1])
			i++
			var (
				next extv1.JSONSchemaProps
				ok   bool
			)
			if seg == "definitions" {
				next, ok = cur.Definitions[key]
			} else {
				next, ok = cur.Properties[key]
			}
			if !ok {
				return nil, fmt.Errorf("$ref %q: no %s named %q at that point in the document", ref, strings.TrimSuffix(seg, "s"), key)
			}
			cur = &next
		case "items":
			if cur.Items == nil || cur.Items.Schema == nil {
				return nil, fmt.Errorf("$ref %q: no items schema at that point in the document", ref)
			}
			cur = cur.Items.Schema
		case "additionalProperties":
			if cur.AdditionalProperties == nil || cur.AdditionalProperties.Schema == nil {
				return nil, fmt.Errorf("$ref %q: no additionalProperties schema at that point in the document", ref)
			}
			cur = cur.AdditionalProperties.Schema
		default:
			return nil, fmt.Errorf("$ref %q: %q is not a traversable part of a schema (expected definitions, properties, items or additionalProperties)", ref, seg)
		}
	}
	return cur, nil
}

func unescapePointerSegment(s string) string {
	s = strings.ReplaceAll(s, "~1", "/")
	return strings.ReplaceAll(s, "~0", "~")
}

func pathOrRoot(p FieldPath) string {
	if len(p) == 0 {
		return "(root)"
	}
	return p.String()
}

// mergeSchema folds one allOf branch into its parent.
//
// Conflicting constraints are a compile error naming both sides, never a
// last-writer-wins: the apiserver's own behaviour for a contradictory
// allOf is to reject every object, and guessing which half the author
// meant would make the engine's field set disagree with what the cluster
// actually accepts.
//
// Only the fields the engine reasons about are merged — see
// NormalizeSchema for why the rest may be dropped.
func mergeSchema(dst, src *extv1.JSONSchemaProps, path FieldPath, branch int) error {
	if src == nil {
		return nil
	}
	where := fmt.Sprintf("schema at %q, allOf[%d]", pathOrRoot(path), branch)

	if src.Type != "" {
		if dst.Type != "" && dst.Type != src.Type {
			return fmt.Errorf("%s: conflicting type %q against %q on the parent; an allOf cannot narrow a field to two different types", where, src.Type, dst.Type)
		}
		dst.Type = src.Type
	}
	// Nullability intersects rather than conflicting. "Must be a string"
	// and "may be null" is not a contradiction — it is a string, because a
	// value has to satisfy every branch. A branch that carries a type
	// fully specifies both, so its nullability constrains the parent's; a
	// branch that carries only `nullable: true` cannot widen a parent that
	// does not permit null.
	//
	// Kubernetes rejects `nullable` on a junctor outright, so none of this
	// is reachable through a CRD. It is reachable through NormalizeSchema,
	// which is exported and takes hand-written offline schemas, and there
	// the old behaviour was a hard error on a schema that has a perfectly
	// well-defined meaning.
	if src.Type != "" {
		dst.Nullable = dst.Nullable && src.Nullable
	}
	if src.XPreserveUnknownFields != nil {
		if dst.XPreserveUnknownFields != nil && *dst.XPreserveUnknownFields != *src.XPreserveUnknownFields {
			return fmt.Errorf("%s: conflicting x-kubernetes-preserve-unknown-fields against the parent", where)
		}
		dst.XPreserveUnknownFields = src.XPreserveUnknownFields
	}

	if len(src.Enum) > 0 {
		merged, err := intersectEnums(dst.Enum, src.Enum)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		dst.Enum = merged
	}

	if len(src.Required) > 0 {
		dst.Required = unionRequired(dst.Required, src.Required)
	}

	// A union inside an allOf branch is carried up, not dropped. The
	// engine reads oneOf/anyOf — unionConstruct decides whether a node is
	// opaque, and branchMap maps the branches — so discarding one here
	// would silently hand every later stage a different schema from the
	// one the author wrote.
	//
	// Two unions cannot be merged into one list: `allOf: [{oneOf: A},
	// {oneOf: B}]` means "satisfies A *and* satisfies B", which no single
	// oneOf expresses. That is an error naming both, for the same reason
	// two conflicting types are.
	for _, u := range []struct {
		name     string
		dst, src *[]extv1.JSONSchemaProps
	}{
		{"oneOf", &dst.OneOf, &src.OneOf},
		{"anyOf", &dst.AnyOf, &src.AnyOf},
	} {
		if len(*u.src) == 0 {
			continue
		}
		if len(*u.dst) > 0 {
			return fmt.Errorf("%s: declares %s while the parent already does; two unions over the same node cannot be merged into one, so express the combination as a single %s", where, u.name, u.name)
		}
		*u.dst = *u.src
	}

	if len(src.Properties) > 0 {
		if dst.Properties == nil {
			dst.Properties = map[string]extv1.JSONSchemaProps{}
		}
		names := make([]string, 0, len(src.Properties))
		for name := range src.Properties {
			names = append(names, name)
		}
		// Sorted so a conflict is reported against the same property on
		// every run, rather than whichever the map iterated to first.
		sort.Strings(names)
		for _, name := range names {
			srcProp := src.Properties[name]
			if dstProp, ok := dst.Properties[name]; ok {
				merged := dstProp.DeepCopy()
				if err := mergeSchema(merged, &srcProp, append(path.Clone(), name), branch); err != nil {
					return err
				}
				dst.Properties[name] = *merged
				continue
			}
			dst.Properties[name] = srcProp
		}
	}

	if src.Items != nil && src.Items.Schema != nil {
		if dst.Items == nil || dst.Items.Schema == nil {
			dst.Items = src.Items
		} else {
			merged := dst.Items.Schema.DeepCopy()
			if err := mergeSchema(merged, src.Items.Schema, append(path.Clone(), "[]"), branch); err != nil {
				return err
			}
			dst.Items = &extv1.JSONSchemaPropsOrArray{Schema: merged}
		}
	}

	if src.AdditionalProperties != nil {
		if dst.AdditionalProperties == nil {
			dst.AdditionalProperties = src.AdditionalProperties
		} else if src.AdditionalProperties.Schema != nil && dst.AdditionalProperties.Schema != nil {
			merged := dst.AdditionalProperties.Schema.DeepCopy()
			if err := mergeSchema(merged, src.AdditionalProperties.Schema, append(path.Clone(), "{}"), branch); err != nil {
				return err
			}
			dst.AdditionalProperties = &extv1.JSONSchemaPropsOrBool{Allows: dst.AdditionalProperties.Allows, Schema: merged}
		}
	}
	return nil
}

// intersectEnums is allOf's meaning for a value vocabulary: a value must
// satisfy every branch, so the permitted set is the intersection. An empty
// intersection permits nothing at all, which is a contradiction rather
// than a very strict field.
func intersectEnums(dst, src []extv1.JSON) ([]extv1.JSON, error) {
	if len(dst) == 0 {
		return src, nil
	}
	inSrc := make(map[string]bool, len(src))
	for _, v := range src {
		inSrc[canonicalJSON(v.Raw)] = true
	}
	var out []extv1.JSON
	for _, v := range dst {
		if inSrc[canonicalJSON(v.Raw)] {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("its enum shares no value with the parent's, so no value could ever satisfy both")
	}
	return out, nil
}

// canonicalJSON reduces an enum entry to a form two equal JSON *values*
// share, so the intersection is over values rather than over bytes. JSON
// Schema enum equality is value equality: `{"a":1,"b":2}` and `{ "b": 2,
// "a": 1 }` are the same member, and comparing Raw made them different —
// turning an enum that intersects perfectly well into "shares no value
// with the parent's", a hard compile error on a valid schema.
//
// encoding/json sorts object keys on the way out, which is what does the
// work here; it also normalises whitespace and number formatting. Input
// that does not parse is compared as its literal bytes, because the
// alternative is claiming two unparseable values are equal.
func canonicalJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func unionRequired(dst, src []string) []string {
	seen := make(map[string]bool, len(dst)+len(src))
	out := make([]string, 0, len(dst)+len(src))
	for _, list := range [][]string{dst, src} {
		for _, name := range list {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}
