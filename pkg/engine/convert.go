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

// ConvertInput is one conversion request: a compiled Plan, the direction to
// run it, and the object to convert.
type ConvertInput struct {
	Plan      *Plan
	Direction Direction
	Object    map[string]any
}

// Convert applies a compiled Plan to an object and returns the converted
// result. It never mutates Object: every Op reads from the pristine input
// and writes into a fresh output tree, so ordering between ops can never
// cause one op to observe another's partial output.
//
// This is the hot path invoked by the webhook server on every admission
// request, so it does no schema/YAML parsing — everything it needs was
// precomputed by Compile.
func Convert(in ConvertInput) (map[string]any, error) {
	if in.Plan == nil {
		return nil, fmt.Errorf("convert: nil plan")
	}
	output := map[string]any{}
	// Baseline passthrough: metadata survives untouched except for fields
	// that annotation/label-stash-and-restore ops explicitly rewrite below.
	if md, ok := in.Object["metadata"]; ok {
		output["metadata"] = deepCopyValue(md)
	}
	ctx := &execContext{input: in.Object, output: output}
	for _, op := range in.Plan.ops(in.Direction) {
		if err := op.apply(ctx); err != nil {
			return nil, fmt.Errorf("convert (%s): %w", in.Direction, err)
		}
	}
	return output, nil
}

// Router chains Convert calls to move an object between any two versions
// of a resource by always routing through the hub — the same pattern
// controller-runtime's Hub/Convertible interfaces use for native CRD
// conversion webhooks. Compiling only hub<->spoke plans (O(N) for N
// spokes, never pairwise O(N^2)) and routing spoke<->spoke requests through
// two Convert calls keeps compilation cheap regardless of version count.
type Router struct {
	Hub   string
	Plans map[string]*Plan // keyed by spoke version name
}

// Convert moves obj from version `from` to version `to`, routing through
// the hub when neither endpoint is the hub itself.
func (r *Router) Convert(obj map[string]any, from, to string) (map[string]any, error) {
	if from == to {
		return obj, nil
	}
	hubObj := obj
	if from != r.Hub {
		plan, ok := r.Plans[from]
		if !ok {
			return nil, fmt.Errorf("router: no compiled plan for version %q", from)
		}
		converted, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: obj})
		if err != nil {
			return nil, fmt.Errorf("router: %s->hub: %w", from, err)
		}
		hubObj = converted
	}
	if to == r.Hub {
		return hubObj, nil
	}
	plan, ok := r.Plans[to]
	if !ok {
		return nil, fmt.Errorf("router: no compiled plan for version %q", to)
	}
	converted, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: hubObj})
	if err != nil {
		return nil, fmt.Errorf("router: hub->%s: %w", to, err)
	}
	return converted, nil
}
