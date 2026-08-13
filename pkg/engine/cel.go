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
	"fmt"
	"reflect"

	"github.com/google/cel-go/cel"
)

func celEnv() (*cel.Env, error) {
	return cel.NewEnv(cel.Variable("object", cel.DynType))
}

func compileCELProgram(expr string) (cel.Program, error) {
	env, err := celEnv()
	if err != nil {
		return nil, err
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	return env.Program(ast)
}

// celOp evaluates a precompiled CEL expression against the source object
// and writes only the declared destination paths from the resulting map.
type celOp struct {
	prg   cel.Program
	dests []FieldPath
}

func (o celOp) apply(ctx *execContext) error {
	out, _, err := o.prg.Eval(map[string]any{"object": ctx.input})
	if err != nil {
		return fmt.Errorf("cel: %w", err)
	}
	native, err := out.ConvertToNative(reflect.TypeOf(map[string]any{}))
	if err != nil {
		return fmt.Errorf("cel: result must be a map: %w", err)
	}
	m, ok := native.(map[string]any)
	if !ok {
		return fmt.Errorf("cel: result must be a map, got %T", native)
	}
	for _, p := range o.dests {
		if v, ok := lookupCELResult(m, p); ok {
			if err := setValue(ctx.output, p, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func lookupCELResult(m map[string]any, path FieldPath) (any, bool) {
	if v, ok := getValue(m, path); ok {
		return v, true
	}
	if v, ok := m[path.String()]; ok {
		return v, true
	}
	return nil, false
}
