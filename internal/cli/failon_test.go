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
	"fmt"
	"testing"
)

// outcome is one of the report shapes a run can end in, named the way
// docs/cli.md's exit-code matrix names them so the table below and the
// documented matrix can be read side by side.
type outcome string

const (
	outcomePass            outcome = "pass"
	outcomeAcknowledged    outcome = "acknowledged loss only"
	outcomeUnacknowledged  outcome = "unacknowledged loss"
	outcomeError           outcome = "conversion error"
	outcomeUnusedRule      outcome = "unused rule coverage"
	outcomeAckAndUnusedRle outcome = "acknowledged loss and unused rule"
)

func reportFor(o outcome) *Report {
	rep := &Report{}
	rep.Summary.Samples = 1
	rep.Summary.PathsTested = 1
	rep.RuleCoverage = []RuleCoverage{{RuleID: "v1:rule[0]:FieldRename", MatchedSamples: 1}}
	switch o {
	case outcomePass:
		rep.Summary.Pass = 1
	case outcomeAcknowledged:
		rep.Summary.AcknowledgedLoss = 1
	case outcomeUnacknowledged:
		rep.Summary.UnacknowledgedLoss = 1
	case outcomeError:
		rep.Summary.Errors = 1
	case outcomeUnusedRule:
		rep.Summary.Pass = 1
		rep.RuleCoverage[0].MatchedSamples = 0
	case outcomeAckAndUnusedRle:
		rep.Summary.AcknowledgedLoss = 1
		rep.RuleCoverage[0].MatchedSamples = 0
	}
	return rep
}

// TestDecideExitCode covers every cell of the --fail-on × outcome ×
// --strict matrix documented in docs/cli.md. If a cell here changes, that
// table has to change with it.
func TestDecideExitCode(t *testing.T) {
	outcomes := []outcome{
		outcomePass, outcomeAcknowledged, outcomeUnacknowledged,
		outcomeError, outcomeUnusedRule, outcomeAckAndUnusedRle,
	}
	// want[failOn][strict][outcome]
	want := map[string]map[bool]map[outcome]int{
		failOnNone: {
			false: {
				outcomePass: ExitOK, outcomeAcknowledged: ExitOK, outcomeUnacknowledged: ExitOK,
				outcomeError: ExitOK, outcomeUnusedRule: ExitOK, outcomeAckAndUnusedRle: ExitOK,
			},
			// --fail-on none wins outright: it is the explicit "report,
			// never gate" switch, so --strict cannot resurrect a failure.
			true: {
				outcomePass: ExitOK, outcomeAcknowledged: ExitOK, outcomeUnacknowledged: ExitOK,
				outcomeError: ExitOK, outcomeUnusedRule: ExitOK, outcomeAckAndUnusedRle: ExitOK,
			},
		},
		failOnWarn: {
			false: {
				outcomePass: ExitOK, outcomeAcknowledged: ExitOK, outcomeUnacknowledged: ExitTestFailure,
				outcomeError: ExitTestFailure, outcomeUnusedRule: ExitTestFailure, outcomeAckAndUnusedRle: ExitTestFailure,
			},
			true: {
				outcomePass: ExitOK, outcomeAcknowledged: ExitOK, outcomeUnacknowledged: ExitTestFailure,
				outcomeError: ExitTestFailure, outcomeUnusedRule: ExitTestFailure, outcomeAckAndUnusedRle: ExitTestFailure,
			},
		},
		failOnLoss: {
			// The default: an acknowledged loss is a decision already
			// made, and a coverage gap is only a warning, so neither
			// fails on its own.
			false: {
				outcomePass: ExitOK, outcomeAcknowledged: ExitOK, outcomeUnacknowledged: ExitTestFailure,
				outcomeError: ExitTestFailure, outcomeUnusedRule: ExitOK, outcomeAckAndUnusedRle: ExitOK,
			},
			// --strict escalates coverage gaps to failures, matching what
			// --fail-on warn does for the same check.
			true: {
				outcomePass: ExitOK, outcomeAcknowledged: ExitOK, outcomeUnacknowledged: ExitTestFailure,
				outcomeError: ExitTestFailure, outcomeUnusedRule: ExitTestFailure, outcomeAckAndUnusedRle: ExitTestFailure,
			},
		},
	}

	for _, failOn := range []string{failOnNone, failOnWarn, failOnLoss} {
		for _, strict := range []bool{false, true} {
			for _, o := range outcomes {
				name := fmt.Sprintf("fail-on=%s/strict=%v/%s", failOn, strict, o)
				t.Run(name, func(t *testing.T) {
					got := decideExitCode(reportFor(o), failOn, strict)
					if got != want[failOn][strict][o] {
						t.Fatalf("decideExitCode = %d, want %d", got, want[failOn][strict][o])
					}
				})
			}
		}
	}
}

func TestTestCmd_RejectsInvalidFailOn(t *testing.T) {
	cmd := newTestCmd()
	cmd.SetArgs([]string{
		"--config", "testdata/config.yaml", "--xrd", "testdata/xrd.yaml",
		"--samples", "testdata/samples", "--fail-on", "everything",
	})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected an invalid --fail-on value to be a usage error")
	}
}

func TestTestCmd_AcceptsEveryFailOnValue(t *testing.T) {
	for _, v := range []string{failOnNone, failOnWarn, failOnLoss} {
		t.Run(v, func(t *testing.T) {
			cmd := newTestCmd()
			cmd.SetOut(discardWriter{})
			cmd.SetArgs([]string{
				"--config", "testdata/config.yaml", "--xrd", "testdata/xrd.yaml",
				"--samples", "testdata/samples", "--fail-on", v,
			})
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error for --fail-on %s: %v", v, err)
			}
		})
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
