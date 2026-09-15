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
	"strings"
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

func props(m map[string]extv1.JSONSchemaProps) *extv1.JSONSchemaProps {
	return &extv1.JSONSchemaProps{Type: "object", Properties: m}
}

// constraintSchema exercises one of each constraint class the issue names.
func constraintSchema() []engine.VersionSchema {
	return []engine.VersionSchema{{
		Name: "v1",
		Schema: props(map[string]extv1.JSONSchemaProps{
			"spec": {
				Type:     "object",
				Required: []string{"region"},
				Properties: map[string]extv1.JSONSchemaProps{
					"region":  {Type: "string"},
					"tier":    {Type: "string", Enum: []extv1.JSON{{Raw: []byte(`"gold"`)}, {Raw: []byte(`"silver"`)}}},
					"name":    {Type: "string", Pattern: "^[a-z]+$"},
					"short":   {Type: "string", MaxLength: ptrInt64(4)},
					"count":   {Type: "integer"},
					"ratio":   {Type: "number", Maximum: ptrFloat64(1)},
					"enabled": {Type: "boolean"},
				},
			},
		}),
	}}
}

func ptrInt64(v int64) *int64       { return &v }
func ptrFloat64(v float64) *float64 { return &v }

func TestOutputValidator_CatchesEachConstraintClass(t *testing.T) {
	ov, err := newOutputValidator(constraintSchema(), nil)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name           string
		spec           map[string]any
		wantPath       string
		wantConstraint string
	}{
		{
			name:           "missing required",
			spec:           map[string]any{"tier": "gold"},
			wantPath:       "spec.region",
			wantConstraint: "required",
		},
		{
			name:           "value outside the enum",
			spec:           map[string]any{"region": "eu", "tier": "bronze"},
			wantPath:       "spec.tier",
			wantConstraint: "enum",
		},
		{
			name:           "pattern mismatch",
			spec:           map[string]any{"region": "eu", "name": "NotLower"},
			wantPath:       "spec.name",
			wantConstraint: "pattern",
		},
		{
			name:           "maxLength overflow",
			spec:           map[string]any{"region": "eu", "short": "far too long"},
			wantPath:       "spec.short",
			wantConstraint: "maxlength",
		},
		{
			name:           "wrong type",
			spec:           map[string]any{"region": "eu", "count": "not a number"},
			wantPath:       "spec.count",
			wantConstraint: "type",
		},
		{
			name:           "maximum exceeded",
			spec:           map[string]any{"region": "eu", "ratio": float64(5)},
			wantPath:       "spec.ratio",
			wantConstraint: "maximum",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ov.validate(map[string]any{"spec": tc.spec}, "v1")
			if len(got) == 0 {
				t.Fatalf("expected a violation, got none")
			}
			var found *SchemaViolation
			for i := range got {
				if got[i].Path == tc.wantPath {
					found = &got[i]
				}
			}
			if found == nil {
				t.Fatalf("no violation at %s; got %v", tc.wantPath, got)
			}
			if found.Constraint != tc.wantConstraint {
				t.Errorf("constraint = %q, want %q (detail: %s)", found.Constraint, tc.wantConstraint, found.Detail)
			}
			if found.Detail == "" {
				t.Error("violation carries no detail")
			}
		})
	}
}

func TestOutputValidator_ValidObjectIsClean(t *testing.T) {
	ov, err := newOutputValidator(constraintSchema(), nil)
	if err != nil {
		t.Fatal(err)
	}
	obj := map[string]any{"spec": map[string]any{
		"region": "eu", "tier": "gold", "name": "abc", "short": "abcd",
		"count": int64(3), "ratio": float64(0.5), "enabled": true,
	}}
	if got := ov.validate(obj, "v1"); len(got) != 0 {
		t.Errorf("a valid object reported violations: %v", got)
	}
}

// The detail the whole feature stands or falls on. Crossplane merges its
// machinery into the CRD it generates, but those properties are absent from
// the XRD's authored schema — the only schema this tool has. Without
// stripping them, every real Crossplane object reports violations its author
// did not cause and cannot fix.
func TestOutputValidator_InjectedFieldsProduceNoViolations(t *testing.T) {
	// additionalProperties:false is the shape that makes undeclared keys an
	// actual violation rather than merely untyped, so it is the honest test
	// of the stripping.
	closed := []engine.VersionSchema{{
		Name: "v1",
		Schema: &extv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]extv1.JSONSchemaProps{
				"spec": {
					Type:                 "object",
					Properties:           map[string]extv1.JSONSchemaProps{"size": {Type: "string"}},
					AdditionalProperties: &extv1.JSONSchemaPropsOrBool{Allows: false},
				},
				"status": {
					Type:                 "object",
					Properties:           map[string]extv1.JSONSchemaProps{"ready": {Type: "boolean"}},
					AdditionalProperties: &extv1.JSONSchemaPropsOrBool{Allows: false},
				},
			},
		},
	}}

	for _, scope := range []xrdadapter.Scope{xrdadapter.ScopeNamespaced, xrdadapter.ScopeCluster, xrdadapter.ScopeLegacyCluster} {
		t.Run(string(scope), func(t *testing.T) {
			injected := xrdadapter.InjectedPathsForScope(scope, true)
			ov, err := newOutputValidator(closed, injected)
			if err != nil {
				t.Fatal(err)
			}

			// A realistically-shaped object: the author's own fields plus
			// everything Crossplane injects for this scope.
			obj := map[string]any{
				"spec":   map[string]any{"size": "large"},
				"status": map[string]any{"ready": true},
			}
			for _, p := range injected {
				setPathForTest(obj, p, map[string]any{"injected": "value"})
			}

			if got := ov.validate(obj, "v1"); len(got) != 0 {
				t.Errorf("injected fields produced %d spurious violation(s): %v", len(got), got)
			}
		})
	}
}

// Stripping must not mutate the caller's object: the converted object is
// still being round-trip diffed after validation runs.
func TestOutputValidator_StripDoesNotMutateTheInput(t *testing.T) {
	injected := []engine.FieldPath{{"spec", "crossplane"}}
	ov, err := newOutputValidator(constraintSchema(), injected)
	if err != nil {
		t.Fatal(err)
	}
	obj := map[string]any{"spec": map[string]any{
		"region":     "eu",
		"crossplane": map[string]any{"compositionRef": map[string]any{"name": "x"}},
	}}
	_ = ov.validate(obj, "v1")

	spec, _ := obj["spec"].(map[string]any)
	if _, still := spec["crossplane"]; !still {
		t.Error("validate stripped spec.crossplane from the caller's object; the round-trip diff runs on it afterwards")
	}
}

func TestOutputValidator_UnknownVersionIsNotAnError(t *testing.T) {
	ov, err := newOutputValidator(constraintSchema(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ov.knows("v9") {
		t.Error("claims to know a version it has no schema for")
	}
	if got := ov.validate(map[string]any{}, "v9"); got != nil {
		t.Errorf("validating against an unknown version should report nothing, got %v", got)
	}
}

// A version with no schema constrains nothing; inventing an empty schema
// would reject every object.
func TestOutputValidator_SchemalessVersionIsSkipped(t *testing.T) {
	ov, err := newOutputValidator([]engine.VersionSchema{{Name: "v1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ov.knows("v1") {
		t.Error("a version with no schema should not be validated against")
	}
}

func TestSchemaViolation_StringNamesTheRuleWhenAttributed(t *testing.T) {
	v := SchemaViolation{Path: "spec.region", Constraint: "required", Detail: "Required value", Rule: "v1:rule[2]:FieldRename"}
	got := v.String()
	for _, want := range []string{"spec.region", "required", "Required value", "v1:rule[2]:FieldRename"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %q", want, got)
		}
	}
}

func setPathForTest(obj map[string]any, path engine.FieldPath, val any) {
	cur := obj
	for i, seg := range path {
		if i == len(path)-1 {
			cur[seg] = val
			return
		}
		next, ok := cur[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
}

// The end-to-end demonstration of what the flag is for: a config that
// reports PASS today, and ERROR with the flag, on the same fixture.
func TestRunTest_ValidateOutputTurnsAFalsePassIntoAnError(t *testing.T) {
	base := TestOptions{
		XRDPath:    "testdata/validate-output/xrd.yaml",
		ConfigPath: "testdata/validate-output/config.yaml",
		SamplesDir: "testdata/validate-output/samples",
		Quiet:      true,
	}

	off, err := RunTest(base)
	if err != nil {
		t.Fatalf("without the flag: %v", err)
	}
	if off.Summary.Errors != 0 {
		t.Fatalf("fixture is supposed to pass without the flag, got %d error(s)", off.Summary.Errors)
	}

	on := base
	on.ValidateOutput = true
	rep, err := RunTest(on)
	if err != nil {
		t.Fatalf("with the flag: %v", err)
	}
	if rep.Summary.Errors == 0 {
		t.Fatal("expected schema violations to be counted as errors")
	}

	var paths, constraints, rules []string
	for _, s := range rep.Samples {
		for _, p := range s.Paths {
			for _, is := range p.Issues {
				if is.Type != "schema-violation" {
					continue
				}
				paths = append(paths, is.Field)
				constraints = append(constraints, is.Detail)
				rules = append(rules, is.Detail)
			}
		}
	}
	joinedPaths := strings.Join(paths, " ")
	// Both are value-domain violations, which is the class the compile-time
	// required-field analysis legitimately cannot reach: the hub constrains
	// neither, so only applying v1's schema to the converted object finds
	// them. The two checks are complementary, not overlapping.
	for _, want := range []string{"spec.name", "spec.tier"} {
		if !strings.Contains(joinedPaths, want) {
			t.Errorf("no violation reported at %s; got %v", want, paths)
		}
	}
	joined := strings.Join(constraints, " ")
	if !strings.Contains(joined, "pattern") {
		t.Errorf("the pattern violation was not identified as such: %v", constraints)
	}
	if !strings.Contains(joined, "enum") {
		t.Errorf("the out-of-enum value was not identified as such: %v", constraints)
	}
	// Attribution is best-effort, but on this fixture each path is claimed
	// by exactly one rule, so it must land.
	if !strings.Contains(strings.Join(rules, " "), "FieldRename") {
		t.Errorf("violations were not attributed to the rules that produced them: %v", rules)
	}
}

// A schema violation is an error rather than a loss, so it fails at the
// default --fail-on threshold rather than needing a stricter one.
func TestRunTest_ValidateOutputMarksThePathAsError(t *testing.T) {
	rep, err := RunTest(TestOptions{
		XRDPath:        "testdata/validate-output/xrd.yaml",
		ConfigPath:     "testdata/validate-output/config.yaml",
		SamplesDir:     "testdata/validate-output/samples",
		ValidateOutput: true,
		Quiet:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range rep.Samples {
		for _, p := range s.Paths {
			if p.From == "v2" && p.To == "v1" {
				found = true
				if p.Result != "error" {
					t.Errorf("v2→v1 result = %q, want error: an object the apiserver rejects is a failed conversion, not a lossy one", p.Result)
				}
			}
		}
	}
	if !found {
		t.Fatal("the v2→v1 path was not tested")
	}
}

// Every shipped fixture and example must stay clean under the flag, or
// turning it on later becomes a breaking change for this repo's own docs.
func TestRunTest_ValidateOutputIsCleanOnTheFullFixture(t *testing.T) {
	rep, err := RunTest(TestOptions{
		XRDPath:        "testdata/full/xrd.yaml",
		ConfigPath:     "testdata/full/config.yaml",
		SamplesDir:     "testdata/full/samples",
		ValidateOutput: true,
		Quiet:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rep.Samples {
		for _, p := range s.Paths {
			for _, is := range p.Issues {
				if is.Type == "schema-violation" {
					t.Errorf("spurious violation on the full fixture: %s %s→%s %s", is.Field, p.From, p.To, is.Detail)
				}
			}
		}
	}
}
