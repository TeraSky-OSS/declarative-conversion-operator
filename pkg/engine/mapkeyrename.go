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

import "fmt"

// mapKeyRenameOp copies a free-form map, renaming keys listed in renames
// and passing every other key through unchanged. A collision (two source
// keys landing on the same destination key) is a runtime error.
type mapKeyRenameOp struct {
	src, dst FieldPath
	renames  map[string]string
}

func (o mapKeyRenameOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.src)
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("mapKeyRename: value at %q is not an object", o.src)
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		dk := k
		if mapped, ok := o.renames[k]; ok {
			dk = mapped
		}
		if _, exists := out[dk]; exists {
			return fmt.Errorf("mapKeyRename: destination key %q collision at %q", dk, o.dst)
		}
		out[dk] = val
	}
	return setValue(ctx.output, o.dst, out)
}
