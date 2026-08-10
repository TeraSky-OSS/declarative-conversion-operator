/*
Copyright 2026 The xrd-conversion-operator Authors.

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

	jsonpatch "github.com/evanphx/json-patch/v5"
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
	deleteValue(ctx.output, FieldPath{"metadata", o.metadataField, o.key})
	return nil
}

// remapEnumOp rewrites a scalar string field's value through a precompiled
// one-way lookup table.
type remapEnumOp struct {
	path       FieldPath
	table      map[string]string
	onUnmapped UnknownKeyPolicy
}

func (o remapEnumOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.path)
	if !ok {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("remapEnum: value at %q is not a string", o.path)
	}
	mapped, ok := o.table[s]
	if !ok {
		if o.onUnmapped == UnknownKeyError {
			return fmt.Errorf("remapEnum: unmapped value %q at %q", s, o.path)
		}
		return nil // Drop: leave the field unset in output.
	}
	return setValue(ctx.output, o.path, mapped)
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
	deleteValue(ctx.output, FieldPath{"metadata", o.metadataField, o.key})
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

// jsonPatchOp applies a precompiled RFC 6902 JSON Patch to the whole
// object. The patch is parsed once at compile time.
type jsonPatchOp struct{ patch jsonpatch.Patch }

func (o jsonPatchOp) apply(ctx *execContext) error {
	// Patches apply against the object accumulated so far in output; JSON
	// Patch ops are the one strategy allowed to see partial output, since
	// arbitrary patches can't be expressed as pure input->output mappings.
	b, err := json.Marshal(ctx.output)
	if err != nil {
		return fmt.Errorf("jsonPatch: marshal current output: %w", err)
	}
	patched, err := o.patch.Apply(b)
	if err != nil {
		return fmt.Errorf("jsonPatch: apply: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(patched, &m); err != nil {
		return fmt.Errorf("jsonPatch: unmarshal patched output: %w", err)
	}
	for k, v := range m {
		ctx.output[k] = v
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
