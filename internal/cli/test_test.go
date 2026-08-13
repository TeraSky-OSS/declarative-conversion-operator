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
)

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

// allStrategies is every built-in strategy the engine supports. Kept here
// (rather than importing pkg/engine's Strategy constants) so this test
// fails loudly if a strategy is renamed or added without this fixture and
// this list being updated together.
var allStrategies = []string{
	"FieldRename", "ScalarToObject", "ObjectToScalar",
	"SingletonArrayToObject", "ObjectToSingletonArray",
	"FieldsToMap", "MapToFields", "ToAnnotation", "ToLabel",
	"FromAnnotation", "FromLabel",
	"EnumRemap", "DefaultValue", "Constant", "Delete", "JSONPatch", "ForEach",
	"TypeCoerce", "ScalarToFields", "FieldsToScalar",
	"ArrayToMapByKey", "MapToArrayByKey", "NumericScale", "ListJoin", "ListSplit",
}

// TestRunTest_FullCoverage_EndToEnd exercises a much richer fixture
// (testdata/full) than TestRunTest_EndToEnd: three versions (a hub plus two
// spokes) so spoke-to-spoke conversions are routed through the hub, and
// every one of the engine's 15 built-in strategies is exercised at least
// once across the two spokes' rule sets. Assertions are on general
// invariants (no errors, no unacknowledged loss, every strategy exercised)
// rather than brittle exact field/path counts, since the fixture is meant
// to grow over time without every addition requiring a rewrite here.
func TestRunTest_FullCoverage_EndToEnd(t *testing.T) {
	rep, err := RunTest(TestOptions{
		XRDPath:    "testdata/full/xrd.yaml",
		ConfigPath: "testdata/full/config.yaml",
		SamplesDir: "testdata/full/samples",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.Summary.Errors != 0 {
		t.Fatalf("expected 0 conversion errors, got %d", rep.Summary.Errors)
	}
	if rep.Summary.UnacknowledgedLoss != 0 {
		t.Fatalf("expected 0 unacknowledged losses, got %d", rep.Summary.UnacknowledgedLoss)
	}
	if rep.Summary.Samples != 3 {
		t.Fatalf("expected 3 samples (one per version), got %d", rep.Summary.Samples)
	}
	// 3 samples x 3 served versions each = 9 paths (including the identity
	// from->from path for each sample).
	if rep.Summary.PathsTested != 9 {
		t.Fatalf("expected 9 paths tested, got %d", rep.Summary.PathsTested)
	}

	// At least one spoke->spoke path (v1<->v2, neither of which is the hub
	// v3) must actually have been tested, confirming hub-routing is
	// exercised, not just hub<->spoke pairs.
	var sawSpokeToSpoke bool
	for _, s := range rep.Samples {
		for _, p := range s.Paths {
			if p.From != "v3" && p.To != "v3" && p.From != p.To {
				sawSpokeToSpoke = true
			}
			if p.Result == "fail" {
				t.Fatalf("path %s->%s (sample %s) unexpectedly failed: %+v", p.From, p.To, s.File, p.Issues)
			}
		}
	}
	if !sawSpokeToSpoke {
		t.Fatalf("expected at least one spoke<->spoke (v1<->v2) path to be tested")
	}

	// Every rule declared in the config must have been matched by at least
	// one sample, and every one of the engine's built-in strategies must be
	// represented somewhere in that coverage.
	seenStrategy := map[string]bool{}
	for _, rc := range rep.RuleCoverage {
		if rc.MatchedSamples == 0 {
			t.Fatalf("expected every declared rule to be exercised by at least one sample, %q was not", rc.RuleID)
		}
		for _, strat := range allStrategies {
			if strings.HasSuffix(rc.RuleID, ":"+strat) {
				seenStrategy[strat] = true
			}
		}
	}
	for _, strat := range allStrategies {
		if !seenStrategy[strat] {
			t.Fatalf("expected strategy %q to be exercised somewhere in the full fixture, but it wasn't", strat)
		}
	}
}

// TestStatusFieldConversion proves that conversion rules apply to
// status.* paths exactly the same way they apply to spec.* paths — the
// engine has no special-casing of the status subresource. The full fixture
// declares a FieldRename rule from the hub's status.phase to v2's
// status.state (and mirrors status identically, with no rule, on v1), so
// this exercises both a rule-driven status conversion and an
// identity-passthrough one.
func TestStatusFieldConversion(t *testing.T) {
	xrd, err := LoadXRD("testdata/full/xrd.yaml")
	if err != nil {
		t.Fatalf("load xrd: %v", err)
	}
	cfg, err := LoadConfig("testdata/full/config.yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	report, _, err := runAnalyze(xrd, cfg)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if report.HasErrors() {
		t.Fatalf("expected a valid config, got errors: %s", summarizeSpokeErrors(report))
	}
	router, err := buildRouter(cfg, report)
	if err != nil {
		t.Fatalf("build router: %v", err)
	}

	// Use the bundled hub sample rather than a hand-built minimal object,
	// since several of the other rules (e.g. the JSONPatch move) require
	// their source spec fields to be present too.
	samples, err := LoadSamples("testdata/full/samples")
	if err != nil {
		t.Fatalf("load samples: %v", err)
	}
	var hub map[string]any
	for _, s := range samples {
		if s.Version == "v3" {
			hub = s.Object
		}
	}
	if hub == nil {
		t.Fatalf("expected a v3 (hub) sample among testdata/full/samples")
	}

	// Hub -> v2: status.phase is renamed to status.state by an explicit
	// rule.
	v2, err := router.Convert(hub, "v3", "v2")
	if err != nil {
		t.Fatalf("convert v3->v2: %v", err)
	}
	v2Status, ok := v2["status"].(map[string]any)
	if !ok {
		t.Fatalf("expected status in v2 output, got %#v", v2["status"])
	}
	if v2Status["state"] != "Ready" {
		t.Fatalf("expected status.state=Ready (renamed from status.phase), got %#v", v2Status["state"])
	}
	if _, hasPhase := v2Status["phase"]; hasPhase {
		t.Fatalf("expected status.phase not to survive the rename, got %#v", v2Status)
	}

	back, err := router.Convert(v2, "v2", "v3")
	if err != nil {
		t.Fatalf("convert v2->v3: %v", err)
	}
	backStatus, ok := back["status"].(map[string]any)
	if !ok || backStatus["phase"] != "Ready" {
		t.Fatalf("expected status.phase=Ready restored on the way back to the hub, got %#v", back["status"])
	}

	// Hub -> v1: status is identical in shape on both sides, so it's
	// auto-covered by an identity op with no rule at all.
	v1, err := router.Convert(hub, "v3", "v1")
	if err != nil {
		t.Fatalf("convert v3->v1: %v", err)
	}
	v1Status, ok := v1["status"].(map[string]any)
	if !ok || v1Status["phase"] != "Ready" {
		t.Fatalf("expected status.phase to pass through identically to v1, got %#v", v1["status"])
	}
}

func TestRunTest_NoSamples_IsAnError(t *testing.T) {
	dir := t.TempDir()
	_, err := RunTest(TestOptions{XRDPath: "testdata/xrd.yaml", ConfigPath: "testdata/config.yaml", SamplesDir: dir})
	if err == nil {
		t.Fatalf("expected an error when the samples directory is empty")
	}
}
