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
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// maxFuzzObjects bounds a single run.
//
// Generation is cheap but conversion is not, and a fuzz count large enough
// to hang CI is a footgun rather than a feature: the value of fuzzing here
// comes from boundary bias, not from volume.
const maxFuzzObjects = 10000

// fuzzGenerator builds schema-valid objects from a version's own schema.
//
// Fixtures test the cases the config author thought of. They reliably miss
// the empty array, the absent optional object, the maxLength boundary, the
// enum value nobody uses, and the single-element map — which is exactly
// where conversion rules break. So generation is deliberately biased toward
// those boundaries rather than toward "typical" values.
type fuzzGenerator struct {
	rnd *rand.Rand
	// shapes maps a path to the value shape the rules there require, for
	// the cases the schema cannot express.
	shapes map[string]lexicalShape
	// path is the current position, so shapes can be consulted per leaf.
	path []string
	// unconstructible collects required fields the generator could not
	// produce a schema-valid value for, each with the reason. Their objects
	// would fail the generator's own self-check, and the honest report is
	// about the schema rather than about a conversion.
	unconstructible []unconstructibleField
}

// generate builds n objects at version, with apiVersion/kind/metadata
// matching what a real object of this type would carry.
func (g *fuzzGenerator) generate(schema *extv1.JSONSchemaProps, n int, apiVersion, kind string) []Sample {
	out := make([]Sample, 0, n)
	for i := 0; i < n; i++ {
		obj, _ := g.value(schema, 0).(map[string]any)
		if obj == nil {
			obj = map[string]any{}
		}
		obj["apiVersion"] = apiVersion
		obj["kind"] = kind
		// A stable, generated name: the object has to look real enough to
		// convert, and a random name would make failures unreproducible
		// even with the seed.
		obj["metadata"] = map[string]any{"name": fmt.Sprintf("fuzz-%d", i)}
		out = append(out, Sample{
			File:    fmt.Sprintf("fuzz-%d", i),
			Version: versionOfAPIVersion(apiVersion),
			Object:  obj,
		})
	}
	return out
}

func versionOfAPIVersion(apiVersion string) string {
	if i := strings.LastIndex(apiVersion, "/"); i >= 0 {
		return apiVersion[i+1:]
	}
	return apiVersion
}

// value produces one schema-valid value.
//
// depth guards against a self-referential schema; a $ref cycle would
// otherwise recurse until the stack runs out.
func (g *fuzzGenerator) value(s *extv1.JSONSchemaProps, depth int) any {
	if s == nil || depth > 12 {
		return nil
	}

	// A declared enum is the whole domain, and its first and last members
	// are the ones nobody writes fixtures for.
	if len(s.Enum) > 0 {
		return g.pickEnum(s.Enum)
	}
	if s.Default != nil {
		// Bias toward the default some of the time: a conversion that only
		// ever sees explicit values misses the defaulted shape entirely.
		if g.rnd.Intn(4) == 0 {
			var v any
			if json.Unmarshal(s.Default.Raw, &v) == nil {
				return v
			}
		}
	}

	switch s.Type {
	case "object":
		return g.object(s, depth)
	case "array":
		return g.array(s, depth)
	case "string":
		return g.str(s)
	case "integer":
		return g.integer(s)
	case "number":
		return g.number(s)
	case "boolean":
		return g.rnd.Intn(2) == 0
	}

	// No declared type: an x-kubernetes-preserve-unknown-fields subtree or
	// an opaque blob. Produce something plausible rather than panicking —
	// the point is that the conversion survives it.
	if s.XPreserveUnknownFields != nil && *s.XPreserveUnknownFields {
		return map[string]any{"opaque": "value"}
	}
	return nil
}

// unconstructibleField is a required field the generator could not fill,
// with the reason -- which differs between "the schema has a pattern I
// cannot invert" and "a rule wants a lexical form I cannot construct", and
// naming the wrong one sends the reader to fix the wrong file.
type unconstructibleField struct {
	path string
	why  string
}

func (g *fuzzGenerator) whyUnconstructible(prop *extv1.JSONSchemaProps, path string) string {
	if len(g.shapes) > 0 && g.shapes[path] == shapeUnknown {
		return "a rule at this path needs a lexical form the generator cannot construct (a pattern it cannot invert, a template, a CEL expression or a JSON Patch)"
	}
	if prop.Pattern != "" {
		return fmt.Sprintf("the schema constrains it with pattern %q, which the generator cannot invert", prop.Pattern)
	}
	return "the generator has no way to construct a value the schema accepts"
}

func (g *fuzzGenerator) object(s *extv1.JSONSchemaProps, depth int) any {
	out := map[string]any{}
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}

	names := make([]string, 0, len(s.Properties))
	for n := range s.Properties {
		names = append(names, n)
	}
	// Sorted, so the same seed produces the same object: ranging a map
	// would make --seed meaningless across processes.
	sort.Strings(names)

	for _, name := range names {
		prop := s.Properties[name]
		// Required fields are always present; optional ones are absent a
		// quarter of the time, because "the optional object that wasn't
		// there" is a boundary fixtures routinely miss.
		if !required[name] && g.rnd.Intn(4) == 0 {
			continue
		}
		g.path = append(g.path, name)
		v := g.value(&prop, depth+1)
		g.path = g.path[:len(g.path)-1]
		if v != nil {
			out[name] = v
			continue
		}
		// A required property the generator could not construct -- a
		// pattern it cannot invert, with no default and no enum -- would
		// make the whole object fail the schema it was generated from, and
		// the self-check would report that as a generator bug. Record it
		// and let the caller drop the candidate instead.
		if required[name] {
			full := strings.Join(append(append([]string{}, g.path...), name), ".")
			g.unconstructible = append(g.unconstructible, unconstructibleField{path: full, why: g.whyUnconstructible(&prop, full)})
		}
	}
	return out
}

// maxGeneratedSize caps how large a single generated array or string may
// be, independently of --fuzz's cap on the number of objects.
//
// minItems and maxLength arrive from a CRD as int64 and are not validated
// against anything: a schema asking for a million-element array would have
// the generator allocate it. The point of fuzzing is to find conversion
// bugs, not to discover that Go can exhaust memory.
const maxGeneratedSize = 256

// boundedSize clamps a schema-derived size, and reports whether a lower
// bound was itself out of range -- which makes the field unconstructible
// rather than merely large.
func boundedSize(n int) (int, bool) {
	if n > maxGeneratedSize {
		return maxGeneratedSize, true
	}
	if n < 0 {
		return 0, false
	}
	return n, false
}

func (g *fuzzGenerator) array(s *extv1.JSONSchemaProps, depth int) any {
	if s.Items == nil || s.Items.Schema == nil {
		return []any{}
	}
	minItems, minTooLarge := 0, false
	if s.MinItems != nil {
		minItems, minTooLarge = boundedSize(int(*s.MinItems))
	}
	if minTooLarge {
		// A minimum beyond the generation cap cannot be satisfied without
		// allocating what the cap exists to prevent, so the field is left
		// out and reported if it was required.
		return nil
	}
	maxItems := minItems + 2
	if s.MaxItems != nil {
		maxItems, _ = boundedSize(int(*s.MaxItems))
	}
	if maxItems < minItems {
		maxItems = minItems
	}

	// Biased to the ends: the empty array and the single-element array are
	// where forEach, arrayToMapByKey and listJoin rules break.
	var n int
	switch g.rnd.Intn(4) {
	case 0:
		n = minItems
	case 1:
		n = maxItems
	case 2:
		if minItems <= 1 && maxItems >= 1 {
			n = 1
		} else {
			n = minItems
		}
	default:
		if maxItems > minItems {
			n = minItems + g.rnd.Intn(maxItems-minItems+1)
		} else {
			n = minItems
		}
	}

	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		v := g.value(s.Items.Schema, depth+1)
		if v == nil {
			// A nil element would serialize as null and violate the item
			// schema's own type. An absent element is a legitimate shorter
			// array; a null one is an invalid object.
			continue
		}
		out = append(out, v)
	}
	// minItems still has to hold after skipping unconstructible elements.
	// An empty array is not the fallback: with minItems > 0 it violates the
	// schema just as surely, and the self-check would report the
	// generator's own output as a bug. The field is omitted instead, and
	// reported if it was required.
	if len(out) < minItems {
		return nil
	}
	return out
}

func (g *fuzzGenerator) currentShape() lexicalShape {
	if len(g.shapes) == 0 {
		return shapeFree
	}
	return g.shapes[strings.Join(g.path, ".")]
}

// schemaVouchedString falls back to a value the schema itself vouches for
// when the generator cannot construct one: a default, or an enum member.
// Returns nil when there is neither, which leaves the field absent.
func (g *fuzzGenerator) schemaVouchedString(s *extv1.JSONSchemaProps) any {
	if s.Default != nil {
		var v any
		if json.Unmarshal(s.Default.Raw, &v) == nil {
			return v
		}
	}
	if len(s.Enum) > 0 {
		return g.pickEnum(s.Enum)
	}
	return nil
}

// fitsStringSchema reports whether a generated string satisfies the string
// constraints the schema does declare.
func fitsStringSchema(v any, s *extv1.JSONSchemaProps) bool {
	str, ok := v.(string)
	if !ok {
		return false
	}
	if s.MinLength != nil && int64(len(str)) < *s.MinLength {
		return false
	}
	if s.MaxLength != nil && int64(len(str)) > *s.MaxLength {
		return false
	}
	if s.Pattern != "" {
		re, err := regexp.Compile(s.Pattern)
		if err != nil || !re.MatchString(str) {
			return false
		}
	}
	return true
}

func (g *fuzzGenerator) str(s *extv1.JSONSchemaProps) any {
	// A rule at this path may require a lexical form the schema does not
	// describe. Honour it, or leave the field out when it cannot be
	// constructed — see lexicalShape.
	switch shape := g.currentShape(); shape {
	case shapeQuantity, shapeDuration, shapeNumeric:
		// The rule's lexical form still has to satisfy the schema the
		// object is generated from. A field with both a `pattern` and a
		// quantity rule would otherwise produce a value the self-check
		// rejects, and the run would abort reporting a generator bug.
		if v := g.shapedString(shape); fitsStringSchema(v, s) {
			return v
		}
		return g.schemaVouchedString(s)
	case shapeUnknown:
		return g.schemaVouchedString(s)
	}

	// A pattern is a constraint the generator cannot invert in general.
	// Rather than emit a value that violates the source schema — which
	// would read as a conversion bug — fall back to something the schema
	// itself vouches for, and otherwise skip the field.
	if s.Pattern != "" {
		return g.schemaVouchedString(s)
	}

	minLen, maxLen := 1, 8
	minTooLong := false
	if s.MinLength != nil {
		minLen, minTooLong = boundedSize(int(*s.MinLength))
	}
	if minTooLong {
		return nil
	}
	if s.MaxLength != nil {
		maxLen, _ = boundedSize(int(*s.MaxLength))
	}
	if maxLen < minLen {
		maxLen = minLen
	}

	// Both boundaries, deliberately: maxLength overflow on the way back is
	// a classic conversion failure.
	var n int
	switch g.rnd.Intn(3) {
	case 0:
		n = minLen
	case 1:
		n = maxLen
	default:
		if maxLen > minLen {
			n = minLen + g.rnd.Intn(maxLen-minLen+1)
		} else {
			n = minLen
		}
	}
	if n <= 0 {
		return ""
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[g.rnd.Intn(len(alphabet))]
	}
	return string(b)
}

func (g *fuzzGenerator) integer(s *extv1.JSONSchemaProps) any {
	// Ceil and Floor, not a cast: int64(0.5) truncates to 0, which is below
	// a minimum of 0.5 and produces an object the generator's own
	// self-check rejects as a bug.
	//
	// The span is derived from whichever bound the schema declares, not
	// from a fixed default pair: a schema with `minimum: 500` and no
	// maximum would otherwise end up with lo=500, hi=100 and generate
	// nothing at all -- silently dropping an optional field from fuzz
	// coverage, and aborting the run for a required one.
	const span = 100
	loSet, hiSet := s.Minimum != nil, s.Maximum != nil
	lo, hi := int64(0), int64(span)
	if loSet {
		lo = int64(math.Ceil(*s.Minimum))
	}
	if hiSet {
		hi = int64(math.Floor(*s.Maximum))
	}
	switch {
	case loSet && hiSet:
		if hi < lo {
			// A range with no integer in it -- minimum 0.2, maximum 0.8 --
			// is the only unsatisfiable case, and clamping either bound
			// would invent a value outside it.
			return nil
		}
	case loSet:
		hi = lo + span
		if hi < lo { // overflowed
			hi = math.MaxInt64
		}
	case hiSet:
		lo = hi - span
		if lo > hi { // underflowed
			lo = math.MinInt64
		}
	}
	switch g.rnd.Intn(3) {
	case 0:
		return lo
	case 1:
		return hi
	default:
		if hi > lo {
			return lo + g.rnd.Int63n(hi-lo+1)
		}
		return lo
	}
}

func (g *fuzzGenerator) number(s *extv1.JSONSchemaProps) any {
	lo, hi := 0.0, 100.0
	if s.Minimum != nil {
		lo = *s.Minimum
	}
	if s.Maximum != nil {
		hi = *s.Maximum
	}
	if hi < lo {
		hi = lo
	}
	switch g.rnd.Intn(3) {
	case 0:
		return lo
	case 1:
		return hi
	default:
		return lo + g.rnd.Float64()*(hi-lo)
	}
}

func (g *fuzzGenerator) pickEnum(enum []extv1.JSON) any {
	// First and last preferentially: the enum member nobody uses is the one
	// an EnumRemap rule forgets to map.
	var idx int
	switch g.rnd.Intn(3) {
	case 0:
		idx = 0
	case 1:
		idx = len(enum) - 1
	default:
		idx = g.rnd.Intn(len(enum))
	}
	var v any
	if err := json.Unmarshal(enum[idx].Raw, &v); err != nil {
		return nil
	}
	return v
}

// generateFuzzSamples builds n objects at the hub version.
//
// Self-checking: every generated object is validated against the schema it
// was generated from, because a generator bug that emits an invalid object
// would surface as a conversion failure and send the reader hunting in the
// wrong place entirely.
func generateFuzzSamples(versions []engine.VersionSchema, hubVersion, apiVersionGroup, kind string, n int, seed int64, shapes map[string]lexicalShape) ([]Sample, error) {
	if n <= 0 {
		return nil, fmt.Errorf("--fuzz must be positive, got %d", n)
	}
	if n > maxFuzzObjects {
		return nil, fmt.Errorf("--fuzz %d exceeds the per-run cap of %d; a run large enough to hang CI is not a useful gate", n, maxFuzzObjects)
	}

	var hub *engine.VersionSchema
	for i := range versions {
		if versions[i].Name == hubVersion {
			hub = &versions[i]
		}
	}
	if hub == nil || hub.Schema == nil {
		return nil, fmt.Errorf("hub version %q has no schema to generate from", hubVersion)
	}

	// #nosec G404 -- reproducibility from --seed is the point; nothing here is a secret
	g := &fuzzGenerator{rnd: rand.New(rand.NewSource(seed)), shapes: shapes}
	samples := g.generate(hub.Schema, n, apiVersionGroup+"/"+hubVersion, kind)
	if len(g.unconstructible) > 0 {
		// Not a generator bug and not a conversion bug: the schema requires
		// a field whose form cannot be constructed from the schema alone.
		// Saying so beats aborting, and beats silently fuzzing a shape that
		// can never be valid.
		u := g.unconstructible[0]
		return nil, fmt.Errorf("cannot generate objects for %s: required field %q cannot be filled — %s, and it has neither a default nor an enum to fall back on. Give it one in the schema, or test that path with --samples instead of --fuzz", hubVersion, u.path, u.why)
	}

	validator, err := newOutputValidator([]engine.VersionSchema{*hub}, nil)
	if err != nil {
		return nil, err
	}
	for _, s := range samples {
		if vs := validator.validate(s.Object, hubVersion); len(vs) > 0 {
			return nil, fmt.Errorf("internal error: the fuzz generator produced an object that does not satisfy the %s schema it was generated from (%s); this is a generator bug, not a conversion bug — please report it with --seed %d", hubVersion, vs[0].String(), seed)
		}
	}
	return samples, nil
}

// recordFailingSamples writes every sample that failed conversion into dir
// as an ordinary sample file.
//
// This is what makes fuzzing pay off over time rather than being a one-off:
// a case the generator discovered can be promoted into the permanent fixture
// corpus, where it is tested on every run by everyone, instead of depending
// on somebody re-rolling the same seed.
func recordFailingSamples(dir string, samples []Sample, results []SampleResult) error {
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- promoted fixtures are committed and read by everyone
		return fmt.Errorf("creating --record-failures directory: %w", err)
	}
	written := 0
	for i, sr := range results {
		if i >= len(samples) || !sampleFailed(sr) {
			continue
		}
		data, err := marshalStable(samples[i].Object)
		if err != nil {
			return fmt.Errorf("serializing failing sample %s: %w", samples[i].File, err)
		}
		name := filepath.Join(dir, sanitizeForPath(samples[i].File)+".yaml")
		if err := os.WriteFile(name, data, 0o644); err != nil { // #nosec G306 -- see above
			return fmt.Errorf("writing failing sample %s: %w", name, err)
		}
		written++
	}
	return nil
}

func sampleFailed(sr SampleResult) bool {
	for _, p := range sr.Paths {
		if p.Result == "fail" || p.Result == "error" {
			return true
		}
	}
	return false
}

// lexicalShape is a value shape a rule expects at a path that the schema
// itself does not describe.
//
// A schema can say `type: string` while a rule expects a Kubernetes
// quantity, a Go duration, or a number. Generating a random string for those
// produces an object that is perfectly schema-valid and that the conversion
// cannot possibly handle — which surfaces as a conversion error and sends
// the reader hunting for a bug in their rules that is really a bug in the
// generator. The issue this implements calls that out as the thing to avoid,
// and the schema alone cannot avoid it.
type lexicalShape int

const (
	shapeFree lexicalShape = iota
	shapeQuantity
	shapeDuration
	shapeNumeric
	// shapeUnknown marks a path whose expected lexical form the generator
	// cannot construct — a pattern it cannot invert, a split/join template.
	// Those paths are left absent rather than filled with something the
	// conversion will certainly reject.
	shapeUnknown
)

// shapesFromRules maps destination and source paths to the value shape the
// rules at that path require.
func shapesFromRules(report engine.AnalyzeReport) map[string]lexicalShape {
	out := map[string]lexicalShape{}
	mark := func(paths []string, shape lexicalShape) {
		for _, p := range paths {
			// A stricter shape wins: a path touched by both a free-form
			// rename and a quantity rule still has to parse as a quantity.
			if cur, ok := out[p]; ok && cur > shape {
				continue
			}
			out[p] = shape
		}
	}
	for _, sr := range report.SpokeReports {
		for _, rr := range sr.RuleResults {
			switch rr.Strategy {
			case engine.StrategyQuantity:
				mark(rr.HubPaths, shapeQuantity)
				mark(rr.SpokePaths, shapeQuantity)
			case engine.StrategyDuration:
				mark(rr.HubPaths, shapeDuration)
				mark(rr.SpokePaths, shapeDuration)
			case engine.StrategyNumericScale, engine.StrategyTypeCoerce:
				mark(rr.HubPaths, shapeNumeric)
				mark(rr.SpokePaths, shapeNumeric)
			case engine.StrategyScalarToFields, engine.StrategyFieldsToScalar,
				engine.StrategyScalarToObject, engine.StrategyObjectToScalar,
				engine.StrategyListJoin, engine.StrategyListSplit,
				engine.StrategyCEL, engine.StrategyJSONPatch:
				// These carry a pattern, a template or an expression the
				// generator cannot invert. Leaving the path absent is the
				// honest choice: absence is a legitimate input and a real
				// boundary, whereas a random string is neither.
				mark(rr.HubPaths, shapeUnknown)
				mark(rr.SpokePaths, shapeUnknown)
			}
		}
	}
	return out
}

func (g *fuzzGenerator) shapedString(shape lexicalShape) any {
	switch shape {
	case shapeQuantity:
		units := []string{"", "m", "Ki", "Mi", "Gi", "k", "M", "G"}
		return fmt.Sprintf("%d%s", 1+g.rnd.Intn(512), units[g.rnd.Intn(len(units))])
	case shapeDuration:
		units := []string{"ns", "us", "ms", "s", "m", "h"}
		return fmt.Sprintf("%d%s", 1+g.rnd.Intn(120), units[g.rnd.Intn(len(units))])
	case shapeNumeric:
		return strconv.Itoa(g.rnd.Intn(1000))
	}
	return nil
}
