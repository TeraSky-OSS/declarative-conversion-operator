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

// Direction selects which way a Plan's operations run.
type Direction int

const (
	HubToSpoke Direction = iota
	SpokeToHub
)

func (d Direction) String() string {
	if d == HubToSpoke {
		return "hub_to_spoke"
	}
	return "spoke_to_hub"
}

// execContext is the per-conversion working state an Op reads from and
// writes to. input is the pristine source object; every Op reads only from
// input and writes only into output, so operations can never observe each
// other's partial output — the only ordering hazard Compile must guard
// against is two rules writing the same terminal output path.
type execContext struct {
	input  map[string]any
	output map[string]any
}

// Op is one precompiled, resolved-path conversion step. Concrete Op
// implementations store pre-split path segments and pre-decoded values
// (enum tables, parsed JSON Patches, etc.) at compile time so the hot
// (request-serving) path never re-parses YAML/schema or does reflection-
// based generic tree diffing.
type Op interface {
	apply(ctx *execContext) error
}

// Plan is the compiled, directly-executable output of Compile for one
// hub<->spoke version pair. HubToSpoke and SpokeToHub are independently
// derived operation lists — not literally mirror images of one another,
// since e.g. annotation-stash and annotation-restore are genuinely
// different operations compiled from the same rule.
type Plan struct {
	HubVersion, SpokeVersion string
	HubToSpoke               []Op
	SpokeToHub               []Op
}

// ops returns the operation list for the given direction.
func (p *Plan) ops(dir Direction) []Op {
	if dir == HubToSpoke {
		return p.HubToSpoke
	}
	return p.SpokeToHub
}
