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

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// This function had no test, which is how an automated lint fix was able to
// rewrite it into a builder that shadowed itself and returned "" for every
// report — turning "your config is invalid, here is why" into "your config is
// invalid". The whole value of the function is the text it produces, so that
// is what this asserts.
func TestSummarizeSpokeErrors(t *testing.T) {
	report := engine.AnalyzeReport{SpokeReports: []engine.SpokeReport{
		{Version: "v1", Errors: []engine.Diagnostic{
			{Message: "spec.size: no such field"},
			{Message: "spec.mode: type mismatch"},
		}},
		{Version: "v2", Errors: []engine.Diagnostic{
			{Message: "spec.other: no such field"},
		}},
	}}

	got := summarizeSpokeErrors(report)
	if got == "" {
		t.Fatal("returned an empty summary despite three errors; the caller then reports a failure with no reason")
	}
	for _, want := range []string{
		"[spoke v1] spec.size: no such field",
		"[spoke v1] spec.mode: type mismatch",
		"[spoke v2] spec.other: no such field",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q:\n%s", want, got)
		}
	}
	// Every error exactly once: the broken version appended a builder to
	// itself, which doubles rather than drops.
	if n := strings.Count(got, "spec.size: no such field"); n != 1 {
		t.Errorf("the first error appears %d times, want 1:\n%s", n, got)
	}
}

func TestSummarizeSpokeErrors_NoErrors(t *testing.T) {
	report := engine.AnalyzeReport{SpokeReports: []engine.SpokeReport{{Version: "v1"}}}
	if got := summarizeSpokeErrors(report); got != "" {
		t.Errorf("expected an empty summary for a clean report, got %q", got)
	}
}
