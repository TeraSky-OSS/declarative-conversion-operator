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
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// knownTree mirrors a schema node's declared child-key structure, just
// deep enough to tell two very different situations apart:
//
//   - A field the schema declares but no rule/identityOp claims. The
//     leftover-field scan in resolveAndBuildOps already catches this at
//     Compile time (flagged as an uncovered field, normally an error).
//   - A field the schema never mentions at all, at any depth. flattenSchema
//     never produces a LeafField for it, so it is invisible to every rule,
//     to identityOp's auto-coverage, and to the leftover-field scan alike —
//     nothing in Compile ever reasons about it. A real controller compiles
//     its schema from the XRD/CRD's own authored openAPIV3Schema, but the
//     platform (e.g. Crossplane injecting a standard status.conditions and
//     spec.crossplane shape into every generated CRD version) can still
//     populate fields the schema's author never declared. Convert's
//     output starts empty and is only ever populated by an Op, so without
//     this, such a field is silently dropped on every conversion.
//
// A nil/empty children map marks the second case's boundary: a scalar,
// array, or opaque map — exactly the node kinds flattenSchema stops
// recursing at (see classify).
type knownTree struct {
	children map[string]*knownTree
}

// buildKnownTree walks schema the same way flattenSchema does, but records
// the declared shape itself rather than a flat list of leaf paths, so
// passthroughUnknownOp can walk a runtime object in lock-step and tell
// "known leaf" apart from "not in the schema at all" at every depth.
func buildKnownTree(schema *extv1.JSONSchemaProps) *knownTree {
	if schema == nil {
		return &knownTree{}
	}
	kind, opaque := classify(schema)
	if opaque || kind != FieldKindObject || len(schema.Properties) == 0 {
		return &knownTree{}
	}
	t := &knownTree{children: make(map[string]*knownTree, len(schema.Properties))}
	for name, propSchema := range schema.Properties {
		propSchema := propSchema
		t.children[name] = buildKnownTree(&propSchema)
	}
	return t
}

// passthroughUnknownOp copies every subtree of the source object that tree
// never declares, verbatim, to the same path in the output. It never
// touches a path tree does recognize (whether or not a rule claims it —
// that distinction is Compile's job, not this Op's) and never introduces
// brand-new top-level keys (kind/apiVersion/metadata are Convert's own
// responsibility, and a wholly foreign top-level container is a different
// problem than the nested-nowhere-in-the-schema fields this exists for).
//
// Compile appends one of these, built from the direction's source schema,
// to the end of every ops list — after every rule-derived Op and every
// identityOp from the leftover-field scan — so it only ever fills in
// siblings those Ops left untouched; it can't clobber anything they wrote.
type passthroughUnknownOp struct {
	tree *knownTree
}

func (o passthroughUnknownOp) apply(ctx *execContext) error {
	collectUnknown(o.tree, ctx.input, nil, func(path FieldPath, v any) {
		_ = setValue(ctx.output, path, deepCopyValue(v))
	})
	return nil
}

// collectUnknown recurses through src in lock-step with t, invoking emit
// once per subtree rooted at a key t doesn't know about. It never
// descends into a subtree it just emitted (the whole thing is copied
// wholesale, whatever shape it turns out to have) and never recurses
// through a known leaf (t.children empty) — some other Op already owns
// that path, decided entirely by the schema, not by what a given sample
// happens to contain.
func collectUnknown(t *knownTree, srcNode any, prefix FieldPath, emit func(FieldPath, any)) {
	src, ok := srcNode.(map[string]any)
	if !ok {
		return
	}
	for k, v := range src {
		child, known := t.children[k]
		if !known {
			if len(prefix) == 0 {
				// Top-level keys outside the schema's own declared roots
				// (kind, apiVersion, metadata) are Convert's job.
				continue
			}
			emit(append(prefix.Clone(), k), v)
			continue
		}
		if child == nil || len(child.children) == 0 {
			continue
		}
		collectUnknown(child, v, append(prefix.Clone(), k), emit)
	}
}
