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

package webhook

import (
	"strings"
	"testing"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// The admission path's entire diagnostic value is this string. Untested, it
// was silently rewritten to return "" for every report, which turns a
// rejection into an unexplained one.
func TestSummarizeErrors(t *testing.T) {
	report := engine.AnalyzeReport{SpokeReports: []engine.SpokeReport{
		{Version: "v1", Errors: []engine.Diagnostic{{Message: "spec.size: no such field"}}},
		{Version: "v2", Errors: []engine.Diagnostic{{Message: "spec.mode: type mismatch"}}},
	}}

	got := summarizeErrors(report)
	if got == "" {
		t.Fatal("returned an empty summary despite two errors; admission then rejects the config without saying why")
	}
	for _, want := range []string{
		"[spoke v1] spec.size: no such field; ",
		"[spoke v2] spec.mode: type mismatch; ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "spec.size"); n != 1 {
		t.Errorf("the first error appears %d times, want 1:\n%s", n, got)
	}
}

func TestSummarizeErrors_NoErrors(t *testing.T) {
	if got := summarizeErrors(engine.AnalyzeReport{SpokeReports: []engine.SpokeReport{{Version: "v1"}}}); got != "" {
		t.Errorf("expected an empty summary for a clean report, got %q", got)
	}
}
