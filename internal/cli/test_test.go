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

package cli

import "testing"

// TestRunTest_EndToEnd exercises the full offline pipeline (load -> analyze
// -> compile -> test every sample across every version pair) against fixed
// fixtures with one lossless FieldRename and one intentionally lossy,
// acknowledged Delete rule, so the expected pass/loss counts are known
// exactly and any regression in the round-trip/diff/acknowledgement logic
// shows up as a concrete assertion failure.
func TestRunTest_EndToEnd(t *testing.T) {
	rep, err := RunTest(TestOptions{
		XRDPath:    "testdata/xrd.yaml",
		ConfigPath: "testdata/config.yaml",
		SamplesDir: "testdata/samples",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.Summary.Samples != 2 {
		t.Fatalf("expected 2 samples, got %d", rep.Summary.Samples)
	}
	if rep.Summary.PathsTested != 4 {
		t.Fatalf("expected 4 paths tested (2 samples x 2 served versions), got %d", rep.Summary.PathsTested)
	}
	if rep.Summary.Errors != 0 {
		t.Fatalf("expected 0 errors, got %d", rep.Summary.Errors)
	}
	if rep.Summary.UnacknowledgedLoss != 0 {
		t.Fatalf("expected 0 unacknowledged losses (the only lossy rule is acknowledged), got %d", rep.Summary.UnacknowledgedLoss)
	}
	if rep.Summary.AcknowledgedLoss != 1 {
		t.Fatalf("expected exactly 1 acknowledged loss (sample2's legacyDebugFlag round trip through v1), got %d", rep.Summary.AcknowledgedLoss)
	}
	if rep.Summary.Pass != 3 {
		t.Fatalf("expected 3 passing paths, got %d", rep.Summary.Pass)
	}

	if len(rep.RuleCoverage) != 2 {
		t.Fatalf("expected 2 declared rules in coverage, got %d", len(rep.RuleCoverage))
	}
	for _, rc := range rep.RuleCoverage {
		if rc.MatchedSamples == 0 {
			t.Fatalf("expected every declared rule to be exercised by at least one sample, %q was not", rc.RuleID)
		}
	}

	// Find sample2's v2->v1 path and confirm the issue is precisely
	// identified: the right field, the right direction, acknowledged.
	var found bool
	for _, s := range rep.Samples {
		if s.AssertedVersion != "v2" {
			continue
		}
		for _, p := range s.Paths {
			if p.To != "v1" {
				continue
			}
			if p.Result != "loss" {
				t.Fatalf("expected sample2's v2->v1 path to be classified as 'loss', got %q", p.Result)
			}
			if len(p.Issues) != 1 || p.Issues[0].Field != "spec.legacyDebugFlag" || p.Issues[0].Type != "acknowledged-loss" {
				t.Fatalf("unexpected issues for sample2's v2->v1 path: %+v", p.Issues)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("expected to find sample2's v2->v1 path result")
	}
}

func TestRunTest_NoSamples_IsAnError(t *testing.T) {
	dir := t.TempDir()
	_, err := RunTest(TestOptions{XRDPath: "testdata/xrd.yaml", ConfigPath: "testdata/config.yaml", SamplesDir: dir})
	if err == nil {
		t.Fatalf("expected an error when the samples directory is empty")
	}
}
