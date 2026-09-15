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

package cli

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// SchemaViolation is one way a converted object fails the destination
// version's own schema.
//
// This is the gap the rest of `convctl test` cannot see. Round-tripping a
// sample and diffing it proves the rules agree with each other; it says
// nothing about whether the result is an object the apiserver will accept.
// A conversion that drops a required field, produces a value outside an
// enum, or overflows a maxLength is reported as PASS today and rejected in
// production, with an error that names the object rather than the rule that
// produced it.
type SchemaViolation struct {
	// Path is the JSON path within the converted object, e.g.
	// "spec.region".
	Path string `json:"path"`
	// Constraint is the schema keyword that failed, e.g. "required",
	// "enum", "pattern", "maxLength", "type". Best-effort: it is derived
	// from the validator's own error type, and falls back to "schema".
	Constraint string `json:"constraint"`
	// Detail is the validator's message, verbatim.
	Detail string `json:"detail"`
	// Rule names the conversion rule whose output landed at Path, when
	// that can be attributed. Attribution is best-effort by design — an
	// unattributed violation is still reported, because a violation
	// nobody can explain is more important to surface, not less.
	Rule string `json:"rule,omitempty"`
}

func (v SchemaViolation) String() string {
	s := fmt.Sprintf("%s: %s (%s)", v.Path, v.Detail, v.Constraint)
	if v.Rule != "" {
		s += " [" + v.Rule + "]"
	}
	return s
}

// outputValidator validates converted objects against the destination
// version's authored schema, using the apiextensions structural-schema
// validator rather than a hand-rolled checker — so the semantics are the
// apiserver's, not an approximation of them.
type outputValidator struct {
	byVersion map[string]validation.SchemaValidator
	// injected are the paths the platform adds to the generated CRD but
	// which are absent from the authored schema. See strip.
	injected []engine.FieldPath
}

// newOutputValidator compiles one validator per version.
//
// injected is the platform's own field set (Crossplane's spec.crossplane,
// status.conditions, the LegacyCluster block, ...). Those fields are real on
// a live object and absent from the authored schema, so they have to be
// removed before validating or every Crossplane object reports violations
// for fields the author never wrote and cannot fix. Pass nil for a native
// CRD, whose authored schema is the whole schema.
func newOutputValidator(versions []engine.VersionSchema, injected []engine.FieldPath) (*outputValidator, error) {
	ov := &outputValidator{byVersion: map[string]validation.SchemaValidator{}, injected: injected}
	for _, v := range versions {
		if v.Schema == nil {
			// A version with no schema constrains nothing; skip it rather
			// than inventing an empty schema that would reject everything.
			continue
		}
		internal := &apiextensions.JSONSchemaProps{}
		if err := extv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(v.Schema, internal, nil); err != nil {
			return nil, fmt.Errorf("converting %s schema for validation: %w", v.Name, err)
		}
		sv, _, err := validation.NewSchemaValidator(internal)
		if err != nil {
			return nil, fmt.Errorf("building validator for %s: %w", v.Name, err)
		}
		ov.byVersion[v.Name] = sv
	}
	return ov, nil
}

// knows reports whether a version has a schema to validate against.
func (o *outputValidator) knows(version string) bool {
	if o == nil {
		return false
	}
	_, ok := o.byVersion[version]
	return ok
}

// validate checks obj against version's schema and returns every violation,
// sorted by path so the output is stable between runs.
func (o *outputValidator) validate(obj map[string]any, version string) []SchemaViolation {
	if o == nil {
		return nil
	}
	sv, ok := o.byVersion[version]
	if !ok {
		return nil
	}

	errs := validation.ValidateCustomResource(nil, o.strip(obj), sv)
	if len(errs) == 0 {
		return nil
	}
	out := make([]SchemaViolation, 0, len(errs))
	for _, e := range errs {
		out = append(out, SchemaViolation{
			Path:       normalizeFieldPath(e.Field),
			Constraint: constraintFor(e),
			Detail:     e.ErrorBody(),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Detail < out[j].Detail
	})
	return out
}

// strip removes the platform's injected fields from a copy of obj.
//
// This is the detail the whole feature stands or falls on. Crossplane merges
// its machinery (spec.crossplane and friends) into the CRD it generates, but
// those properties are not in the XRD's authored schema — which is the only
// schema this tool has. Validating a live object against the authored schema
// without removing them makes every object look invalid in a way its author
// cannot act on.
//
// Removal happens on a shallow-copied spine rather than in place: the caller
// owns the converted object and may still be diffing it.
func (o *outputValidator) strip(obj map[string]any) map[string]any {
	if len(o.injected) == 0 {
		return obj
	}
	out := obj
	copied := false
	for _, p := range o.injected {
		if !hasPath(out, p) {
			continue
		}
		if !copied {
			out = deepCopyMap(obj)
			copied = true
		}
		deletePath(out, p)
	}
	return out
}

func hasPath(obj map[string]any, path engine.FieldPath) bool {
	cur := obj
	for i, seg := range path {
		v, ok := cur[seg]
		if !ok {
			return false
		}
		if i == len(path)-1 {
			return true
		}
		next, ok := v.(map[string]any)
		if !ok {
			return false
		}
		cur = next
	}
	return false
}

func deletePath(obj map[string]any, path engine.FieldPath) {
	cur := obj
	for i, seg := range path {
		if i == len(path)-1 {
			delete(cur, seg)
			return
		}
		next, ok := cur[seg].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
}

func deepCopyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if m, ok := v.(map[string]any); ok {
			out[k] = deepCopyMap(m)
			continue
		}
		out[k] = v
	}
	return out
}

// constraintFor names the schema keyword behind a validation error.
//
// The validator reports a typed cause plus a human message. The type alone
// is too coarse — Invalid covers pattern, maximum and multipleOf alike — so
// the message is matched against the validator's own phrasings.
//
// Matching is on distinctive phrases, in order, rather than bare keywords.
// A bare-substring scan is wrong in a way that is easy to miss: "not"
// matches inside both "NotLower" (a value) and "may not be more than" (a
// maxLength message), so it silently mislabels unrelated violations. The
// fallback is "schema" rather than a guess, because a confidently wrong
// constraint name is worse than an unspecific one.
func constraintFor(e *field.Error) string {
	switch e.Type {
	case field.ErrorTypeRequired:
		return "required"
	case field.ErrorTypeTypeInvalid:
		return "type"
	case field.ErrorTypeNotSupported:
		return "enum"
	case field.ErrorTypeForbidden:
		return "forbidden"
	case field.ErrorTypeTooLong:
		return "maxlength"
	case field.ErrorTypeTooMany:
		return "maxitems"
	}

	body := strings.ToLower(e.ErrorBody())
	// Ordered: the first match wins, so more specific phrases come first.
	for _, m := range []struct{ phrase, constraint string }{
		{"should match", "pattern"},
		{"should be a multiple of", "multipleof"},
		{"should be less than or equal to", "maximum"},
		{"should be less than", "maximum"},
		{"should be greater than or equal to", "minimum"},
		{"should be greater than", "minimum"},
		{"should have at most", "maxitems"},
		{"should have at least", "minitems"},
		{"should be at most", "maxlength"},
		{"should be at least", "minlength"},
		{"must have unique items", "uniqueitems"},
		{"must have at most", "maxproperties"},
		{"must have at least", "minproperties"},
		{"must validate one and only one schema", "oneof"},
		{"must validate at least one schema", "anyof"},
		{"must validate all the schemas", "allof"},
		{"must not validate the schema", "not"},
		{"in body must be of type", "type"},
		{"must be of type", "type"},
	} {
		if strings.Contains(body, m.phrase) {
			return m.constraint
		}
	}
	return "schema"
}

// normalizeFieldPath turns the validator's field path into the dotted form
// used everywhere else in this tool's output. The validator roots paths at
// "<nil>" when given a nil parent, and uses bracket indexing for arrays.
func normalizeFieldPath(p string) string {
	p = strings.TrimPrefix(p, "<nil>.")
	p = strings.TrimPrefix(p, "<nil>")
	if p == "" {
		return "(root)"
	}
	return p
}

// attributeViolations fills in Rule for each violation whose path a rule
// claims as a destination.
//
// Matching is by declared destination path on the spoke's rule results, and
// is deliberately conservative: a violation is attributed only when exactly
// one rule claims the path. Two rules claiming it means the attribution
// would be a guess, and an unattributed violation is still perfectly
// actionable.
func attributeViolations(vs []SchemaViolation, report engine.AnalyzeReport, spoke, direction string) []SchemaViolation {
	if len(vs) == 0 {
		return vs
	}
	claims := map[string][]string{}
	for _, sr := range report.SpokeReports {
		if sr.Version != spoke {
			continue
		}
		for _, rr := range sr.RuleResults {
			paths := rr.SpokePaths
			if direction == "toHub" {
				paths = rr.HubPaths
			}
			for _, p := range paths {
				id := ruleID(sr.Version, rr)
				claims[p] = append(claims[p], id)
			}
		}
	}
	for i, v := range vs {
		owners := claims[v.Path]
		if len(owners) != 1 {
			continue
		}
		vs[i].Rule = owners[0]
	}
	return vs
}
