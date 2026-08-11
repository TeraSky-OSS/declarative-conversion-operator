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

// Package engine implements a CRD-agnostic, declarative schema-conversion
// engine: given a "hub" schema, one or more "spoke" schemas, and a set of
// declarative rules describing how fields map between them, it can analyze
// whether the mapping is lossless, compile the rules into an efficient
// execution plan, and apply that plan to convert objects at runtime.
//
// This package intentionally has no knowledge of Crossplane or of the
// api/v1alpha1 CRD types. It depends only on the standard Kubernetes
// apiextensions JSONSchemaProps type as its schema currency. Adapters (see
// pkg/xrdadapter) bridge specific resource kinds into the SchemaSource
// interface this package consumes.
package engine

import "strings"

// FieldPath is a field path expressed as a sequence of map-key segments,
// e.g. FieldPath{"spec", "parameters", "storageGB"} for "spec.parameters.storageGB".
// Paths do not encode array indices; per-element access is handled by ForEach
// rules, whose nested rule paths are relative to a single array element.
type FieldPath []string

// ParsePath splits a dotted path string into a FieldPath.
func ParsePath(s string) FieldPath {
	if s == "" {
		return nil
	}
	return strings.Split(s, ".")
}

// FieldPathFromJSONPointer converts an RFC 6901 JSON Pointer (as used in
// RFC 6902 JSON Patch "path"/"from" fields, e.g. "/spec/legacyFlag") into a
// FieldPath, decoding the "~1" -> "/" and "~0" -> "~" escapes.
func FieldPathFromJSONPointer(ptr string) FieldPath {
	ptr = strings.TrimPrefix(ptr, "/")
	if ptr == "" {
		return nil
	}
	segs := strings.Split(ptr, "/")
	for i, s := range segs {
		s = strings.ReplaceAll(s, "~1", "/")
		s = strings.ReplaceAll(s, "~0", "~")
		segs[i] = s
	}
	return FieldPath(segs)
}

// String renders the path back into dotted form.
func (p FieldPath) String() string {
	return strings.Join(p, ".")
}

// Equal reports whether two paths have identical segments.
func (p FieldPath) Equal(o FieldPath) bool {
	if len(p) != len(o) {
		return false
	}
	for i := range p {
		if p[i] != o[i] {
			return false
		}
	}
	return true
}

// HasPrefix reports whether p starts with the segments of prefix.
func (p FieldPath) HasPrefix(prefix FieldPath) bool {
	if len(prefix) > len(p) {
		return false
	}
	for i := range prefix {
		if p[i] != prefix[i] {
			return false
		}
	}
	return true
}

// Clone returns a copy of the path so callers can safely mutate slices
// derived from a shared source.
func (p FieldPath) Clone() FieldPath {
	out := make(FieldPath, len(p))
	copy(out, p)
	return out
}

// getValue reads the value at path from an unstructured object tree. The
// second return value is false if any segment along the path is missing or
// the tree shape doesn't match (e.g. a non-object encountered mid-path).
func getValue(obj map[string]any, path FieldPath) (any, bool) {
	if len(path) == 0 {
		return obj, true
	}
	var cur any = obj
	for _, seg := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[seg]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// setValue writes val at path into an unstructured object tree, creating
// intermediate maps as needed. It merges into (rather than replacing)
// intermediate objects, so setting two sibling paths never clobbers one
// another.
func setValue(obj map[string]any, path FieldPath, val any) error {
	if len(path) == 0 {
		return errFieldPath("setValue: empty path")
	}
	cur := obj
	for _, seg := range path[:len(path)-1] {
		next, ok := cur[seg]
		if !ok {
			m := map[string]any{}
			cur[seg] = m
			cur = m
			continue
		}
		m, ok := next.(map[string]any)
		if !ok {
			return errFieldPath("setValue: cannot descend into non-object at " + seg)
		}
		cur = m
	}
	cur[path[len(path)-1]] = val
	return nil
}

// deleteValue removes the value at path, if present. Missing intermediate
// segments are treated as a no-op rather than an error, since "the field is
// already absent" and "delete the field" reach the same end state.
func deleteValue(obj map[string]any, path FieldPath) {
	if len(path) == 0 {
		return
	}
	cur := obj
	for _, seg := range path[:len(path)-1] {
		next, ok := cur[seg]
		if !ok {
			return
		}
		m, ok := next.(map[string]any)
		if !ok {
			return
		}
		cur = m
	}
	delete(cur, path[len(path)-1])
}

// deepCopyValue deep-copies a JSON-like value tree (maps, slices, and
// scalars) so that operations never let two paths in an output object alias
// the same underlying map/slice.
func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = deepCopyValue(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = deepCopyValue(vv)
		}
		return out
	default:
		return v
	}
}

// deleteValuePruningEmptyMaps behaves like deleteValue, but additionally
// removes any ancestor map that becomes empty as a result of the deletion
// (e.g. an annotations/labels map left with zero keys after its one stashed
// entry is restored elsewhere), so a stash-then-restore round trip produces
// an object indistinguishable from one where the stash never happened,
// rather than leaving behind a stray empty map the original never had.
func deleteValuePruningEmptyMaps(obj map[string]any, path FieldPath) {
	deleteValue(obj, path)
	for i := len(path) - 1; i > 0; i-- {
		parent := path[:i]
		v, ok := getValue(obj, parent)
		if !ok {
			return
		}
		m, ok := v.(map[string]any)
		if !ok || len(m) != 0 {
			return
		}
		deleteValue(obj, parent)
	}
}

type errFieldPath string

func (e errFieldPath) Error() string { return string(e) }
