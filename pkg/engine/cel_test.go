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
	"strings"
	"testing"
)

func TestCELCostLimitRejectsExpensiveEval(t *testing.T) {
	prg, err := compileCELProgramWithLimit(`{"n": object.n + object.n + object.n + object.n + object.n}`, 1)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, _, err = prg.Eval(map[string]any{"object": map[string]any{"n": 1}})
	if err == nil {
		t.Fatal("expected evaluation to fail under CostLimit=1")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "cost") && !strings.Contains(strings.ToLower(err.Error()), "limit") {
		t.Fatalf("expected a cost-limit error, got %v", err)
	}
}
