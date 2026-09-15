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
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

func fuzzSchema() []engine.VersionSchema {
	maxLen := int64(6)
	minLen := int64(2)
	maxItems := int64(3)
	maxVal := float64(10)
	minVal := float64(2)
	return []engine.VersionSchema{{
		Name:    "v1",
		Storage: true,
		Served:  true,
		Schema: &extv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]extv1.JSONSchemaProps{
				"spec": {
					Type:     "object",
					Required: []string{"name"},
					Properties: map[string]extv1.JSONSchemaProps{
						"name":  {Type: "string", MinLength: &minLen, MaxLength: &maxLen},
						"count": {Type: "integer", Minimum: &minVal, Maximum: &maxVal},
						"tier":  {Type: "string", Enum: []extv1.JSON{{Raw: []byte(`"gold"`)}, {Raw: []byte(`"silver"`)}, {Raw: []byte(`"bronze"`)}}},
						"tags":  {Type: "array", MaxItems: &maxItems, Items: &extv1.JSONSchemaPropsOrArray{Schema: &extv1.JSONSchemaProps{Type: "string"}}},
						"opt":   {Type: "string"},
					},
				},
			},
		},
	}}
}

// The same seed must produce the same objects, or a CI failure cannot be
// reproduced locally and the feature is not usable as a gate.
func TestFuzz_SameSeedSameObjects(t *testing.T) {
	a, err := generateFuzzSamples(fuzzSchema(), "v1", "example.org", "Thing", 20, 1234, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := generateFuzzSamples(fuzzSchema(), "v1", "example.org", "Thing", 20, 1234, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range a {
		x, _ := json.Marshal(a[i].Object)
		y, _ := json.Marshal(b[i].Object)
		if string(x) != string(y) {
			t.Fatalf("object %d differs between two runs with the same seed:\n%s\n%s", i, x, y)
		}
	}

	c, err := generateFuzzSamples(fuzzSchema(), "v1", "example.org", "Thing", 20, 5678, nil)
	if err != nil {
		t.Fatal(err)
	}
	same := true
	for i := range a {
		x, _ := json.Marshal(a[i].Object)
		z, _ := json.Marshal(c[i].Object)
		if string(x) != string(z) {
			same = false
		}
	}
	if same {
		t.Error("a different seed produced identical objects; the seed is not being used")
	}
}

// The self-check is what keeps a generator bug from reading as a conversion
// bug, which is the failure mode that would make the feature untrustworthy.
func TestFuzz_GeneratedObjectsSatisfyTheSourceSchema(t *testing.T) {
	samples, err := generateFuzzSamples(fuzzSchema(), "v1", "example.org", "Thing", 200, 99, nil)
	if err != nil {
		t.Fatalf("the generator produced an object its own schema rejects: %v", err)
	}
	if len(samples) != 200 {
		t.Fatalf("expected 200 objects, got %d", len(samples))
	}
	for _, s := range samples {
		if s.Object["apiVersion"] != "example.org/v1" || s.Object["kind"] != "Thing" {
			t.Fatalf("generated object is not identifiable: %v", s.Object)
		}
	}
}

// Boundary bias is the whole point: a generator that only produced typical
// values would miss exactly the cases fixtures already cover.
func TestFuzz_HitsTheBoundaries(t *testing.T) {
	samples, err := generateFuzzSamples(fuzzSchema(), "v1", "example.org", "Thing", 300, 2024, nil)
	if err != nil {
		t.Fatal(err)
	}

	var (
		emptyArray, singleArray, maxArray bool
		minLenName, maxLenName            bool
		minCount, maxCount                bool
		firstEnum, lastEnum               bool
		absentOptional                    bool
	)
	for _, s := range samples {
		spec, _ := s.Object["spec"].(map[string]any)
		if spec == nil {
			continue
		}
		if tags, ok := spec["tags"].([]any); ok {
			switch len(tags) {
			case 0:
				emptyArray = true
			case 1:
				singleArray = true
			case 3:
				maxArray = true
			}
		}
		if n, ok := spec["name"].(string); ok {
			if len(n) == 2 {
				minLenName = true
			}
			if len(n) == 6 {
				maxLenName = true
			}
		}
		if c, ok := spec["count"].(int64); ok {
			if c == 2 {
				minCount = true
			}
			if c == 10 {
				maxCount = true
			}
		}
		switch spec["tier"] {
		case "gold":
			firstEnum = true
		case "bronze":
			lastEnum = true
		}
		if _, present := spec["opt"]; !present {
			absentOptional = true
		}
	}

	for _, c := range []struct {
		got  bool
		what string
	}{
		{emptyArray, "an empty array"},
		{singleArray, "a single-element array"},
		{maxArray, "an array at maxItems"},
		{minLenName, "a string at minLength"},
		{maxLenName, "a string at maxLength"},
		{minCount, "an integer at minimum"},
		{maxCount, "an integer at maximum"},
		{firstEnum, "the first enum member"},
		{lastEnum, "the last enum member"},
		{absentOptional, "an absent optional field"},
	} {
		if !c.got {
			t.Errorf("300 generated objects never produced %s; the boundary bias is not working", c.what)
		}
	}
}

// A schema can say `type: string` while a rule expects a Kubernetes
// quantity. Generating a random string there produces a schema-valid object
// the conversion cannot handle, which reads as a conversion bug and is not
// one.
func TestFuzz_HonoursLexicalShapesRulesRequire(t *testing.T) {
	shapes := map[string]lexicalShape{
		"spec.name":  shapeQuantity,
		"spec.opt":   shapeDuration,
		"spec.tier":  shapeFree,
		"spec.count": shapeFree,
	}
	// A schema with no length limits, so the shaped value is not truncated.
	versions := []engine.VersionSchema{{
		Name: "v1", Storage: true, Served: true,
		Schema: &extv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]extv1.JSONSchemaProps{
				"spec": {
					Type:     "object",
					Required: []string{"name", "opt"},
					Properties: map[string]extv1.JSONSchemaProps{
						"name": {Type: "string"},
						"opt":  {Type: "string"},
					},
				},
			},
		},
	}}
	samples, err := generateFuzzSamples(versions, "v1", "example.org", "Thing", 40, 11, shapes)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, s := range samples {
		spec, _ := s.Object["spec"].(map[string]any)
		if spec == nil {
			// spec itself is optional and is legitimately absent
			// sometimes; that is a boundary, not a failure.
			continue
		}
		checked++
		q, _ := spec["name"].(string)
		if q == "" || !strings.ContainsAny(q, "0123456789") {
			t.Fatalf("quantity-shaped field is not a quantity: %q", q)
		}
		d, _ := spec["opt"].(string)
		if d == "" || !strings.ContainsAny(d, "0123456789") {
			t.Fatalf("duration-shaped field is not a duration: %q", d)
		}
	}
	if checked == 0 {
		t.Fatal("every generated object omitted spec; the test proved nothing")
	}
}

// A path whose lexical form the generator cannot construct is left absent
// rather than filled with something the conversion will certainly reject.
func TestFuzz_UnknownShapeLeavesThePathAbsent(t *testing.T) {
	versions := []engine.VersionSchema{{
		Name: "v1", Storage: true, Served: true,
		Schema: &extv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]extv1.JSONSchemaProps{
				"spec": {
					Type:       "object",
					Properties: map[string]extv1.JSONSchemaProps{"packed": {Type: "string"}},
				},
			},
		},
	}}
	samples, err := generateFuzzSamples(versions, "v1", "example.org", "Thing", 30, 3, map[string]lexicalShape{"spec.packed": shapeUnknown})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range samples {
		spec, _ := s.Object["spec"].(map[string]any)
		if spec == nil {
			continue
		}
		if _, present := spec["packed"]; present {
			t.Fatalf("a path with an unconstructable lexical shape should be left absent, got %v", spec["packed"])
		}
	}
}

// A run large enough to hang CI is a footgun, not a feature.
func TestFuzz_BoundedRuntime(t *testing.T) {
	if _, err := generateFuzzSamples(fuzzSchema(), "v1", "example.org", "Thing", maxFuzzObjects+1, 1, nil); err == nil {
		t.Error("expected a refusal above the per-run cap")
	}
	if _, err := generateFuzzSamples(fuzzSchema(), "v1", "example.org", "Thing", 0, 1, nil); err == nil {
		t.Error("expected a refusal for a non-positive count")
	}
}

// An x-kubernetes-preserve-unknown-fields subtree has no schema to generate
// from; it must produce something plausible rather than panicking.
func TestFuzz_OpaqueSubtreeDoesNotPanic(t *testing.T) {
	yes := true
	versions := []engine.VersionSchema{{
		Name: "v1", Storage: true, Served: true,
		Schema: &extv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]extv1.JSONSchemaProps{
				"spec": {
					Type: "object",
					Properties: map[string]extv1.JSONSchemaProps{
						"blob": {XPreserveUnknownFields: &yes},
					},
				},
			},
		},
	}}
	if _, err := generateFuzzSamples(versions, "v1", "example.org", "Thing", 10, 5, nil); err != nil {
		t.Fatalf("an opaque subtree should generate something plausible: %v", err)
	}
}

// Promotion into the permanent fixture corpus is what makes fuzzing pay off
// over time rather than being a one-off.
func TestFuzz_RecordFailuresWritesOrdinarySamples(t *testing.T) {
	dir := t.TempDir()
	samples := []Sample{{File: "fuzz-0", Object: map[string]any{"apiVersion": "example.org/v1", "kind": "Thing"}}}
	results := []SampleResult{{File: "fuzz-0", Paths: []PathResult{{Result: "error"}}}}
	if err := recordFailingSamples(dir, samples, results); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dir + "/fuzz-0.yaml")
	if err != nil {
		t.Fatalf("the failing object was not promoted: %v", err)
	}
	if !strings.Contains(string(data), "kind: Thing") {
		t.Errorf("promoted file is not an ordinary sample: %s", data)
	}
}

func TestFuzz_PassingSamplesAreNotRecorded(t *testing.T) {
	dir := t.TempDir()
	samples := []Sample{{File: "fuzz-0", Object: map[string]any{"kind": "Thing"}}}
	results := []SampleResult{{File: "fuzz-0", Paths: []PathResult{{Result: "pass"}}}}
	if err := recordFailingSamples(dir, samples, results); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a passing sample should not be promoted, got %d file(s)", len(entries))
	}
}

// A rule's lexical shape and the schema's own constraints can disagree: a
// field the schema types as a string with a pattern, that a quantity rule
// also reads. The shaped value has to lose, or every generated object fails
// the self-check and the run aborts claiming a generator bug.
func TestFuzz_ShapedValuesStillSatisfyTheSchema(t *testing.T) {
	versions := []engine.VersionSchema{{
		Name: "v1", Storage: true, Served: true,
		Schema: &extv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]extv1.JSONSchemaProps{
				"spec": {
					Type:     "object",
					Required: []string{"size"},
					Properties: map[string]extv1.JSONSchemaProps{
						// A quantity rule reads it, but the schema only
						// admits these two spellings.
						"size": {Type: "string", Pattern: "^(small|large)$", Enum: []extv1.JSON{{Raw: []byte(`"small"`)}, {Raw: []byte(`"large"`)}}},
					},
				},
			},
		},
	}}
	samples, err := generateFuzzSamples(versions, "v1", "example.org", "Thing", 20, 3,
		map[string]lexicalShape{"spec.size": shapeQuantity})
	if err != nil {
		t.Fatalf("generation failed instead of honouring the schema: %v", err)
	}
	checked := 0
	for _, s := range samples {
		spec, _ := s.Object["spec"].(map[string]any)
		if spec == nil {
			continue
		}
		checked++
		if v, _ := spec["size"].(string); v != "small" && v != "large" {
			t.Fatalf("generated %q, which the schema's pattern rejects", v)
		}
	}
	if checked == 0 {
		t.Fatal("every object omitted spec; the test proved nothing")
	}
}

// A required field with a pattern the generator cannot invert, and neither a
// default nor an enum to fall back on, cannot be generated at all. That is a
// fact about the schema, and saying so beats aborting with "generator bug"
// on an object the generator was never able to produce.
func TestFuzz_RequiredFieldItCannotConstructIsReportedAsSuch(t *testing.T) {
	versions := []engine.VersionSchema{{
		Name: "v1", Storage: true, Served: true,
		Schema: &extv1.JSONSchemaProps{
			Type:     "object",
			Required: []string{"spec"},
			Properties: map[string]extv1.JSONSchemaProps{
				"spec": {
					Type:     "object",
					Required: []string{"serial"},
					Properties: map[string]extv1.JSONSchemaProps{
						"serial": {Type: "string", Pattern: `^SN-[0-9]{6}-[A-Z]{3}$`},
					},
				},
			},
		},
	}}
	_, err := generateFuzzSamples(versions, "v1", "example.org", "Thing", 5, 1, nil)
	if err == nil {
		t.Fatal("want an error naming the field that cannot be generated")
	}
	if !strings.Contains(err.Error(), "spec.serial") {
		t.Errorf("error should name the field: %v", err)
	}
	if strings.Contains(err.Error(), "generator bug") {
		t.Errorf("this is a fact about the schema, not a generator bug: %v", err)
	}
}

// minItems and maxLength arrive from a CRD as int64 and are validated
// against nothing. A schema asking for a million-element array is not a fuzz
// case worth allocating.
func TestFuzz_SchemaBoundsCannotDriveUnboundedAllocation(t *testing.T) {
	big := int64(50_000_000)
	versions := []engine.VersionSchema{{
		Name: "v1", Storage: true, Served: true,
		Schema: &extv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]extv1.JSONSchemaProps{
				"spec": {
					Type: "object",
					Properties: map[string]extv1.JSONSchemaProps{
						"tags":  {Type: "array", MaxItems: &big, Items: &extv1.JSONSchemaPropsOrArray{Schema: &extv1.JSONSchemaProps{Type: "string"}}},
						"blurb": {Type: "string", MaxLength: &big},
					},
				},
			},
		},
	}}
	done := make(chan struct{})
	var samples []Sample
	go func() {
		defer close(done)
		samples, _ = generateFuzzSamples(versions, "v1", "example.org", "Thing", 20, 5, nil)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("generation did not finish; a schema bound drove an unbounded allocation")
	}
	for _, s := range samples {
		spec, _ := s.Object["spec"].(map[string]any)
		if spec == nil {
			continue
		}
		if tags, ok := spec["tags"].([]any); ok && len(tags) > maxGeneratedSize {
			t.Fatalf("generated %d array elements, cap is %d", len(tags), maxGeneratedSize)
		}
		if b, ok := spec["blurb"].(string); ok && len(b) > maxGeneratedSize {
			t.Fatalf("generated a %d-byte string, cap is %d", len(b), maxGeneratedSize)
		}
	}
}

// An array whose items cannot be constructed cannot fall back to the empty
// array when minItems > 0: that violates the schema just as surely, and the
// self-check reports the generator's own output as a bug.
func TestFuzz_MinItemsIsNeverViolatedByUnconstructibleItems(t *testing.T) {
	two := int64(2)
	versions := []engine.VersionSchema{{
		Name: "v1", Storage: true, Served: true,
		Schema: &extv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]extv1.JSONSchemaProps{
				"spec": {
					Type: "object",
					Properties: map[string]extv1.JSONSchemaProps{
						"serials": {
							Type: "array", MinItems: &two,
							Items: &extv1.JSONSchemaPropsOrArray{Schema: &extv1.JSONSchemaProps{
								Type: "string", Pattern: `^SN-[0-9]{6}$`,
							}},
						},
					},
				},
			},
		},
	}}
	samples, err := generateFuzzSamples(versions, "v1", "example.org", "Thing", 20, 9, nil)
	if err != nil {
		t.Fatalf("generation failed: %v", err)
	}
	for _, s := range samples {
		spec, _ := s.Object["spec"].(map[string]any)
		if spec == nil {
			continue
		}
		if arr, present := spec["serials"]; present {
			if items, _ := arr.([]any); len(items) < 2 {
				t.Fatalf("generated %d items for a minItems: 2 array", len(items))
			}
		}
	}
}

// int64(0.5) truncates to 0, which is below a minimum of 0.5. Exercised
// against the generator directly: a structural schema that puts a fractional
// bound on an integer is itself invalid, so the apiserver would reject the
// CRD — but convctl reads files, and a hand-written XRD that never went
// through admission reaches this code.
func TestFuzz_FractionalIntegerBoundsRoundInward(t *testing.T) {
	g := &fuzzGenerator{rnd: rand.New(rand.NewSource(4))}
	lo, hi := 0.5, 3.5
	for i := 0; i < 50; i++ {
		v, ok := g.integer(&extv1.JSONSchemaProps{Type: "integer", Minimum: &lo, Maximum: &hi}).(int64)
		if !ok {
			t.Fatal("integer() did not return an integer")
		}
		if v < 1 || v > 3 {
			t.Fatalf("generated %d, outside the integers the bounds admit (1..3)", v)
		}
	}

	// A range containing no integer at all cannot be satisfied, and
	// clamping would invent a value outside it.
	noneLo, noneHi := 0.2, 0.8
	if got := g.integer(&extv1.JSONSchemaProps{Type: "integer", Minimum: &noneLo, Maximum: &noneHi}); got != nil {
		t.Errorf("integer() = %v for a range with no integer in it, want the field omitted", got)
	}
}

// A schema that declares one bound and not the other has to generate from
// the bound it declares. Starting from a fixed 0..100 pair meant a field
// with `minimum: 500` produced nothing at all — silently dropped from
// coverage when optional, and aborting the whole run when required.
func TestFuzz_OneSidedIntegerBoundsStillGenerate(t *testing.T) {
	g := &fuzzGenerator{rnd: rand.New(rand.NewSource(12))}
	lo := 500.0
	for i := 0; i < 50; i++ {
		v, ok := g.integer(&extv1.JSONSchemaProps{Type: "integer", Minimum: &lo}).(int64)
		if !ok {
			t.Fatal("a minimum with no maximum generated nothing")
		}
		if v < 500 {
			t.Fatalf("generated %d, below the declared minimum 500", v)
		}
	}

	hi := -5.0
	for i := 0; i < 50; i++ {
		v, ok := g.integer(&extv1.JSONSchemaProps{Type: "integer", Maximum: &hi}).(int64)
		if !ok {
			t.Fatal("a maximum with no minimum generated nothing")
		}
		if v > -5 {
			t.Fatalf("generated %d, above the declared maximum -5", v)
		}
	}

	// A one-sided bound at the edge of the range must not overflow.
	huge := float64(math.MaxInt64)
	if v := g.integer(&extv1.JSONSchemaProps{Type: "integer", Minimum: &huge}); v == nil {
		t.Error("a minimum at the top of the int64 range generated nothing")
	}
}
