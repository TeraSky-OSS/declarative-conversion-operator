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
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	jsonpatch "github.com/evanphx/json-patch/v5"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// identityOp copies a value verbatim from input to output at the same path.
// Compile emits one of these for every leaf field whose shape is identical
// on both sides of a hub<->spoke pair, so field-coverage reporting reflects
// every field, not just the ones with an explicit rule.
type identityOp struct{ path FieldPath }

func (o identityOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.path)
	if !ok {
		return nil
	}
	return setValue(ctx.output, o.path, deepCopyValue(v))
}

// renameOp copies a value from one path to a different path (FieldRename).
type renameOp struct{ from, to FieldPath }

func (o renameOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.from)
	if !ok {
		return nil
	}
	return setValue(ctx.output, o.to, deepCopyValue(v))
}

// wrapScalarOp reads a scalar and writes it into an object under key,
// filling any other declared keys from defaults. Used for the
// hub(scalar)->spoke(object) direction of ScalarToObject, and the
// spoke(scalar)->hub(object) direction of ObjectToScalar.
type wrapScalarOp struct {
	scalarPath, objectPath FieldPath
	key                    string
	defaults               map[string]any
}

func (o wrapScalarOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.scalarPath)
	if !ok {
		return nil
	}
	obj := map[string]any{}
	for k, d := range o.defaults {
		obj[k] = deepCopyValue(d)
	}
	obj[o.key] = deepCopyValue(v)
	return setValue(ctx.output, o.objectPath, obj)
}

// unwrapScalarOp reads a key out of an object and writes it as a bare
// scalar. Used for the hub(object)->spoke(scalar) direction of
// ObjectToScalar, and the spoke(object)->hub(scalar) direction of
// ScalarToObject.
type unwrapScalarOp struct {
	objectPath, scalarPath FieldPath
	key                    string
}

func (o unwrapScalarOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.objectPath)
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	val, ok := m[o.key]
	if !ok {
		return nil
	}
	return setValue(ctx.output, o.scalarPath, deepCopyValue(val))
}

// arrayFirstToObjectOp reads the first element of an array and writes it as
// a bare object (array->object direction of SingletonArrayToObject /
// ObjectToSingletonArray).
type arrayFirstToObjectOp struct{ arrayPath, objectPath FieldPath }

func (o arrayFirstToObjectOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.arrayPath)
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	return setValue(ctx.output, o.objectPath, deepCopyValue(arr[0]))
}

// objectToSingletonArrayOp wraps an object as the sole element of an array.
type objectToSingletonArrayOp struct{ objectPath, arrayPath FieldPath }

func (o objectToSingletonArrayOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.objectPath)
	if !ok {
		return nil
	}
	return setValue(ctx.output, o.arrayPath, []any{deepCopyValue(v)})
}

// collapseToMapOp reads several sibling fields and aggregates them into a
// single map (FieldsToMap's hub->spoke direction, MapToFields' spoke->hub
// direction).
type collapseToMapOp struct {
	fieldPaths []FieldPath
	mapPath    FieldPath
	keyNames   map[string]string
}

func (o collapseToMapOp) apply(ctx *execContext) error {
	m := map[string]any{}
	found := false
	for _, fp := range o.fieldPaths {
		v, ok := getValue(ctx.input, fp)
		if !ok {
			continue
		}
		found = true
		key := o.keyNames[fp.String()]
		if key == "" {
			key = lastSegment(fp)
		}
		m[key] = deepCopyValue(v)
	}
	if !found {
		return nil
	}
	return setValue(ctx.output, o.mapPath, m)
}

// expandFromMapOp reads a map and expands its keys back into sibling
// fields (the inverse of collapseToMapOp). onUnknownKey governs behavior
// when the map contains a key not present in keyNames' values.
type expandFromMapOp struct {
	mapPath      FieldPath
	fieldPaths   []FieldPath
	keyNames     map[string]string // fieldPath.String() -> map key
	onUnknownKey UnknownKeyPolicy
}

func (o expandFromMapOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.mapPath)
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("expandFromMap: value at %q is not an object", o.mapPath)
	}
	known := map[string]bool{}
	for _, fp := range o.fieldPaths {
		key := o.keyNames[fp.String()]
		if key == "" {
			key = lastSegment(fp)
		}
		known[key] = true
		if val, ok := m[key]; ok {
			if err := setValue(ctx.output, fp, deepCopyValue(val)); err != nil {
				return err
			}
		}
	}
	if o.onUnknownKey == UnknownKeyError {
		for k := range m {
			if !known[k] {
				return fmt.Errorf("expandFromMap: unexpected key %q at %q", k, o.mapPath)
			}
		}
	}
	return nil
}

// stashAnnotationOp writes a hub field's value into a metadata annotation
// (or label) so it survives a conversion direction where the spoke schema
// has no field for it.
type stashAnnotationOp struct {
	hubPath       FieldPath
	metadataField string // "annotations" or "labels"
	key           string
	serialization string // "JSON" | "String"
}

func (o stashAnnotationOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.hubPath)
	if !ok {
		return nil
	}
	var strVal string
	if o.serialization == "String" {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("stashAnnotation: value at %q is not a string but serialization=String", o.hubPath)
		}
		strVal = s
	} else {
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("stashAnnotation: marshal %q: %w", o.hubPath, err)
		}
		strVal = string(b)
	}
	if o.metadataField == "labels" {
		if msgs := k8svalidation.IsQualifiedName(o.key); len(msgs) > 0 {
			return fmt.Errorf("stashAnnotation: label key %q is invalid: %s", o.key, strings.Join(msgs, "; "))
		}
		if msgs := k8svalidation.IsValidLabelValue(strVal); len(msgs) > 0 {
			return fmt.Errorf("stashAnnotation: label value for key %q is invalid: %s", o.key, strings.Join(msgs, "; "))
		}
	}
	return setValue(ctx.output, FieldPath{"metadata", o.metadataField, o.key}, strVal)
}

// restoreAnnotationOp is the inverse of stashAnnotationOp: it reads the
// metadata annotation/label back and decodes it into the hub field, then
// removes the bookkeeping key from output metadata so it doesn't leak back
// out as user-visible metadata.
type restoreAnnotationOp struct {
	hubPath       FieldPath
	metadataField string
	key           string
	serialization string
}

func (o restoreAnnotationOp) apply(ctx *execContext) error {
	raw, ok := getValue(ctx.input, FieldPath{"metadata", o.metadataField, o.key})
	if !ok {
		return nil
	}
	strVal, ok := raw.(string)
	if !ok {
		return fmt.Errorf("restoreAnnotation: value at metadata.%s.%s is not a string", o.metadataField, o.key)
	}
	var val any
	if o.serialization == "String" {
		val = strVal
	} else {
		if err := json.Unmarshal([]byte(strVal), &val); err != nil {
			return fmt.Errorf("restoreAnnotation: unmarshal metadata.%s.%s: %w", o.metadataField, o.key, err)
		}
	}
	if err := setValue(ctx.output, o.hubPath, val); err != nil {
		return err
	}
	deleteValuePruningEmptyMaps(ctx.output, FieldPath{"metadata", o.metadataField, o.key})
	return nil
}

// remapEnumOp rewrites a scalar field's value through a precompiled
// one-way lookup table. Keys are canonicalized so string, integer, and
// boolean enums share the same machinery.
type remapEnumOp struct {
	path       FieldPath
	table      map[string]any
	onUnmapped UnknownKeyPolicy
}

func (o remapEnumOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.path)
	if !ok {
		return nil
	}
	mapped, ok := o.table[enumKey(v)]
	if !ok {
		if o.onUnmapped == UnknownKeyError {
			return fmt.Errorf("remapEnum: unmapped value %v at %q", v, o.path)
		}
		return nil
	}
	return setValue(ctx.output, o.path, mapped)
}

func enumKey(v any) string {
	switch t := v.(type) {
	case string:
		return "s:" + t
	case bool:
		return "b:" + strconv.FormatBool(t)
	default:
		if f, ok := AsFloat64(v); ok {
			return "n:" + formatNumber(f)
		}
		return fmt.Sprintf("x:%T:%v", v, v)
	}
}

// injectDefaultOp writes a fixed default into a field that exists only on
// the destination side (used when converting toward the side that declares
// the field).
type injectDefaultOp struct {
	path  FieldPath
	value any
}

func (o injectDefaultOp) apply(ctx *execContext) error {
	return setValue(ctx.output, o.path, deepCopyValue(o.value))
}

// dropFieldOp is a no-op writer: it simply omits copying the source field
// (used for the direction of DefaultValue/Delete that drops a field that
// only exists on the source side).
type dropFieldOp struct{}

func (dropFieldOp) apply(*execContext) error { return nil }

// stripMetadataKeyOp deletes a bookkeeping annotation/label key from the
// output's metadata without restoring it anywhere. Used for the reverse
// direction of a ToAnnotation/ToLabel rule with RestoreOnReverse=false: the
// baseline metadata passthrough in Convert would otherwise carry the stash
// key forward into an object that should have no trace of it.
type stripMetadataKeyOp struct {
	metadataField string
	key           string
}

func (o stripMetadataKeyOp) apply(ctx *execContext) error {
	deleteValuePruningEmptyMaps(ctx.output, FieldPath{"metadata", o.metadataField, o.key})
	return nil
}

// injectConstantOp forces a field to a fixed value, overwriting whatever a
// prior op in the same direction may have written.
type injectConstantOp struct {
	path  FieldPath
	value any
}

func (o injectConstantOp) apply(ctx *execContext) error {
	return setValue(ctx.output, o.path, deepCopyValue(o.value))
}

// jsonPatchOp applies a precompiled RFC 6902 JSON Patch to a copy of the
// pristine input object, then copies just the paths the patch touched
// (both "path" and, for move/copy, "from") into output. The patch is
// parsed once at compile time; touchedPaths is derived from it at the same
// time.
//
// Applying against input rather than output is deliberate: RFC 6902
// "move"/"copy" operations reference a source path that must exist in the
// document being patched. If this op instead patched the
// progressively-built output (as every other Op does implicitly by only
// ever reading input), a "move" whose source hasn't been separately copied
// into output yet would fail even though the source genuinely exists in
// the object being converted. Patching input and then merging only the
// touched paths (rather than the whole patched document) avoids the
// opposite problem — clobbering sibling fields other ops already wrote
// into output for paths this patch never mentioned.
type jsonPatchOp struct {
	patch        jsonpatch.Patch
	touchedPaths []FieldPath
}

func (o jsonPatchOp) apply(ctx *execContext) error {
	b, err := json.Marshal(ctx.input)
	if err != nil {
		return fmt.Errorf("jsonPatch: marshal input: %w", err)
	}
	patched, err := o.patch.Apply(b)
	if err != nil {
		return fmt.Errorf("jsonPatch: apply: %w", err)
	}
	var full map[string]any
	if err := json.Unmarshal(patched, &full); err != nil {
		return fmt.Errorf("jsonPatch: unmarshal patched input: %w", err)
	}
	for _, p := range o.touchedPaths {
		if v, ok := getValue(full, p); ok {
			if err := setValue(ctx.output, p, deepCopyValue(v)); err != nil {
				return fmt.Errorf("jsonPatch: writing %q: %w", p, err)
			}
		} else {
			// Not present after patching (e.g. the source of a move, or an
			// explicit remove) — make sure it's absent from output too,
			// rather than leaving behind whatever a stray earlier op wrote.
			deleteValue(ctx.output, p)
		}
	}
	return nil
}

// forEachOp applies a nested list of Ops to each element of a hub array and
// the corresponding spoke array, requiring strict positional correspondence.
type forEachOp struct {
	srcItemsPath, dstItemsPath FieldPath
	nested                     []Op
}

func (o forEachOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.srcItemsPath)
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return fmt.Errorf("forEach: value at %q is not an array", o.srcItemsPath)
	}
	// Strict positional correspondence: if the destination-side array is
	// already present on the input (paired hub/spoke paths both populated),
	// its length must match the source. Building from source alone when the
	// dest path is absent is fine.
	if dst, ok := getValue(ctx.input, o.dstItemsPath); ok {
		dstArr, isArr := dst.([]any)
		if !isArr {
			return fmt.Errorf("forEach: value at %q is not an array", o.dstItemsPath)
		}
		if len(dstArr) != len(arr) {
			return fmt.Errorf("forEach: length mismatch between %q (%d) and %q (%d)",
				o.srcItemsPath, len(arr), o.dstItemsPath, len(dstArr))
		}
	}
	outArr := make([]any, len(arr))
	for i, elem := range arr {
		elemMap, ok := elem.(map[string]any)
		if !ok {
			return fmt.Errorf("forEach: element %d at %q is not an object", i, o.srcItemsPath)
		}
		elemCtx := &execContext{input: elemMap, output: map[string]any{}}
		for _, op := range o.nested {
			if err := op.apply(elemCtx); err != nil {
				return fmt.Errorf("forEach: element %d: %w", i, err)
			}
		}
		outArr[i] = elemCtx.output
	}
	return setValue(ctx.output, o.dstItemsPath, outArr)
}

// whenOp skips inner unless the input object has path equal to equals.
type whenOp struct {
	path   FieldPath
	equals any
	inner  Op
}

func (o whenOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.path)
	if !ok || !jsonValuesEqual(v, o.equals) {
		return nil
	}
	return o.inner.apply(ctx)
}

// jsonValuesEqual compares decoded JSON values, treating numeric types
// that JSON/YAML might produce (float64 vs int64) as equal when they
// represent the same number.
func jsonValuesEqual(a, b any) bool {
	if af, ok := AsFloat64(a); ok {
		if bf, ok := AsFloat64(b); ok {
			return af == bf
		}
	}
	return reflect.DeepEqual(a, b)
}

// coerceOp reads a scalar and rewrites it as whatever JSON type toKind
// expects (TypeCoerce). It needs no source-kind parameter: it type-
// switches on the value it actually reads.
type coerceOp struct {
	path   FieldPath
	toKind FieldKind
	frac   FractionalIntegerPolicy
}

func (o coerceOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.path)
	if !ok {
		return nil
	}
	coerced, err := coerceScalarValueWithPolicy(v, o.toKind, o.frac)
	if err != nil {
		return fmt.Errorf("typeCoerce: %q: %w", o.path, err)
	}
	return setValue(ctx.output, o.path, coerced)
}

// splitFieldOp reads a single string, matches it against pattern, and
// writes each named capture group to its destination field, coerced to
// that field's declared type. Used for ScalarToFields' hub->spoke
// direction and FieldsToScalar's spoke->hub direction.
type splitFieldOp struct {
	sourcePath FieldPath
	pattern    *regexp.Regexp
	destFields map[string]FieldPath
	destKinds  map[string]FieldKind
}

func (o splitFieldOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.sourcePath)
	if !ok {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("splitField: value at %q is not a string", o.sourcePath)
	}
	match := o.pattern.FindStringSubmatch(s)
	if match == nil {
		return fmt.Errorf("splitField: value %q at %q does not match pattern %q", s, o.sourcePath, o.pattern.String())
	}
	captured := map[string]string{}
	for i, name := range o.pattern.SubexpNames() {
		if name == "" {
			continue
		}
		captured[name] = match[i]
	}
	for name, destPath := range o.destFields {
		raw, ok := captured[name]
		if !ok {
			return fmt.Errorf("splitField: pattern has no capture group named %q", name)
		}
		coerced, err := coerceScalarValue(raw, o.destKinds[name])
		if err != nil {
			return fmt.Errorf("splitField: group %q: %w", name, err)
		}
		if err := setValue(ctx.output, destPath, coerced); err != nil {
			return err
		}
	}
	return nil
}

// joinFieldsOp reads several named fields and renders destPath by
// executing tmpl against them. Used for FieldsToScalar's hub->spoke
// direction and ScalarToFields' spoke->hub direction.
type joinFieldsOp struct {
	srcFields map[string]FieldPath
	destPath  FieldPath
	tmpl      *template.Template
}

func (o joinFieldsOp) apply(ctx *execContext) error {
	data := map[string]any{}
	anyPresent := false
	for name, srcPath := range o.srcFields {
		v, ok := getValue(ctx.input, srcPath)
		if !ok {
			continue
		}
		anyPresent = true
		data[name] = v
	}
	if !anyPresent {
		return nil
	}
	var buf strings.Builder
	if err := o.tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("joinFields: template execution: %w", err)
	}
	return setValue(ctx.output, o.destPath, buf.String())
}

// arrayToMapByKeyOp converts an array of objects into a map keyed by each
// object's KeyField value, removing KeyField from the value itself (it's
// recoverable from the map key). A duplicate or missing key is a runtime
// error, never a silent drop.
type arrayToMapByKeyOp struct {
	arrayPath, mapPath FieldPath
	keyField           string
}

func (o arrayToMapByKeyOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.arrayPath)
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return fmt.Errorf("arrayToMapByKey: value at %q is not an array", o.arrayPath)
	}
	out := map[string]any{}
	for i, elem := range arr {
		m, ok := elem.(map[string]any)
		if !ok {
			return fmt.Errorf("arrayToMapByKey: element %d at %q is not an object", i, o.arrayPath)
		}
		keyVal, ok := m[o.keyField]
		if !ok {
			return fmt.Errorf("arrayToMapByKey: element %d at %q is missing key field %q", i, o.arrayPath, o.keyField)
		}
		keyStr, ok := keyVal.(string)
		if !ok {
			return fmt.Errorf("arrayToMapByKey: element %d at %q: key field %q is not a string", i, o.arrayPath, o.keyField)
		}
		if _, dup := out[keyStr]; dup {
			return fmt.Errorf("arrayToMapByKey: duplicate key %q at %q", keyStr, o.arrayPath)
		}
		rest := map[string]any{}
		for k, vv := range m {
			if k == o.keyField {
				continue
			}
			rest[k] = deepCopyValue(vv)
		}
		out[keyStr] = rest
	}
	return setValue(ctx.output, o.mapPath, out)
}

// mapToArrayByKeyOp is the inverse of arrayToMapByKeyOp: it re-inserts
// each map key as KeyField on its value object and emits the results as
// an array sorted by key, for determinism (Go map iteration order is not
// stable, and the original array's order — if any — cannot be recovered).
type mapToArrayByKeyOp struct {
	mapPath, arrayPath FieldPath
	keyField           string
}

func (o mapToArrayByKeyOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.mapPath)
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("mapToArrayByKey: value at %q is not an object", o.mapPath)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]any, 0, len(keys))
	for _, k := range keys {
		valMap, ok := m[k].(map[string]any)
		if !ok {
			return fmt.Errorf("mapToArrayByKey: value at %q[%q] is not an object", o.mapPath, k)
		}
		elem := map[string]any{o.keyField: k}
		for kk, vv := range valMap {
			elem[kk] = deepCopyValue(vv)
		}
		out = append(out, elem)
	}
	return setValue(ctx.output, o.arrayPath, out)
}

// numericScaleOp reads a number and writes it multiplied or divided by
// factor, optionally rounded to the nearest whole number when the
// destination field is integer-typed.
type numericScaleOp struct {
	fromPath, toPath FieldPath
	factor           float64
	multiply         bool // true: to = from*factor; false: to = from/factor
	round            bool
}

func (o numericScaleOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.fromPath)
	if !ok {
		return nil
	}
	// AsFloat64 also accepts int/int32/int64: client-go's dynamic client
	// (used by `convctl test --live`) decodes whole JSON numbers as int64,
	// unlike the real admission-webhook path (plain encoding/json), which
	// always produces float64 -- see coerce.go's doc comment.
	f, ok := AsFloat64(v)
	if !ok {
		return fmt.Errorf("numericScale: value at %q is not numeric", o.fromPath)
	}
	var result float64
	if o.multiply {
		result = f * o.factor
	} else {
		result = f / o.factor
	}
	if o.round {
		result = math.Round(result)
	}
	return setValue(ctx.output, o.toPath, result)
}

// joinListOp reads an array of scalars and writes it as a single string,
// joining string-coerced elements with separator.
type joinListOp struct {
	arrayPath, stringPath FieldPath
	separator             string
}

func (o joinListOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.arrayPath)
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return fmt.Errorf("listJoin: value at %q is not an array", o.arrayPath)
	}
	parts := make([]string, len(arr))
	for i, elem := range arr {
		s, err := coerceScalarValue(elem, FieldKindString)
		if err != nil {
			return fmt.Errorf("listJoin: element %d: %w", i, err)
		}
		parts[i] = s.(string)
	}
	return setValue(ctx.output, o.stringPath, strings.Join(parts, o.separator))
}

// splitListOp is the inverse of joinListOp: it splits a string on
// separator and writes the parts as an array, each coerced to itemKind.
// An empty string produces an empty array, not a one-element array
// containing "".
type splitListOp struct {
	stringPath, arrayPath FieldPath
	separator             string
	itemKind              FieldKind
}

func (o splitListOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.stringPath)
	if !ok {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("listSplit: value at %q is not a string", o.stringPath)
	}
	var parts []string
	if s != "" {
		parts = strings.Split(s, o.separator)
	}
	out := make([]any, len(parts))
	for i, p := range parts {
		coerced, err := coerceScalarValue(p, o.itemKind)
		if err != nil {
			return fmt.Errorf("listSplit: element %d: %w", i, err)
		}
		out[i] = coerced
	}
	return setValue(ctx.output, o.arrayPath, out)
}
