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

// Severity classifies a Diagnostic.
type Severity string

const (
	SeverityError   Severity = "Error"
	SeverityWarning Severity = "Warning"
)

// UncoveredSide tags leftover uncovered-field diagnostics so Analyze can
// populate FieldCoverage without parsing message text. Empty for every
// other diagnostic.
type UncoveredSide string

const (
	UncoveredSideHub   UncoveredSide = "hub"
	UncoveredSideSpoke UncoveredSide = "spoke"
)

// Diagnostic is one issue found while analyzing or compiling a RuleSet.
type Diagnostic struct {
	Severity  Severity
	Message   string
	RuleIndex int    // -1 if not associated with a specific rule
	FieldPath string // empty if not associated with a specific field
	// UncoveredSide is set only for leftover uncovered-leaf diagnostics
	// (hub or spoke); empty otherwise.
	UncoveredSide UncoveredSide
}

func errorf(ruleIndex int, format string, args ...any) Diagnostic {
	return Diagnostic{Severity: SeverityError, Message: fmt.Sprintf(format, args...), RuleIndex: ruleIndex}
}

func warnf(ruleIndex int, format string, args ...any) Diagnostic {
	return Diagnostic{Severity: SeverityWarning, Message: fmt.Sprintf(format, args...), RuleIndex: ruleIndex}
}

// uncoveredFieldDiag reports a leaf path left unclaimed by any rule (and
// without an identical counterpart on the other side). FieldPath and
// UncoveredSide are always set so Analyze can populate FieldCoverage for
// both Error and Warn unmapped-field policies.
func uncoveredFieldDiag(severity Severity, side UncoveredSide, path string) Diagnostic {
	var msg string
	switch side {
	case UncoveredSideHub:
		msg = fmt.Sprintf("hub field %q is not covered by any rule and has no identical counterpart in the spoke schema", path)
	case UncoveredSideSpoke:
		msg = fmt.Sprintf("spoke field %q is not covered by any rule and has no identical counterpart in the hub schema", path)
	default:
		msg = fmt.Sprintf("field %q is not covered by any rule", path)
	}
	return Diagnostic{
		Severity:      severity,
		Message:       msg,
		RuleIndex:     -1,
		FieldPath:     path,
		UncoveredSide: side,
	}
}

// LosslessVerdict records whether a mapping is lossless in each direction
// independently — a mapping can be lossless hub->spoke but lossy spoke->hub
// (e.g. default-value injection on read, drop on write-back).
type LosslessVerdict struct {
	HubToSpoke bool
	SpokeToHub bool
}

func (v LosslessVerdict) and(o LosslessVerdict) LosslessVerdict {
	return LosslessVerdict{
		HubToSpoke: v.HubToSpoke && o.HubToSpoke,
		SpokeToHub: v.SpokeToHub && o.SpokeToHub,
	}
}

// RuleResult reports the outcome of resolving and analyzing a single rule.
type RuleResult struct {
	Index      int
	Strategy   Strategy
	HubPaths   []string
	SpokePaths []string
	Lossless   LosslessVerdict
	Errors     []string
	Warnings   []string
}

// FieldCoverage lists leaf fields left unclaimed by any rule and not
// structurally identical on both sides.
type FieldCoverage struct {
	UncoveredHub   []string
	UncoveredSpoke []string
}

// SpokeReport is the analysis outcome for one hub<->spoke version pair.
type SpokeReport struct {
	Version     string
	Lossless    LosslessVerdict
	Uncovered   FieldCoverage
	RuleResults []RuleResult
	Errors      []Diagnostic
	Warnings    []Diagnostic
	// CompiledPlan is non-nil only when Errors is empty — a Plan is never
	// produced from an invalid analysis, so "keep serving the last
	// known-good plan on a failed re-validation" falls out for free: a
	// failing re-analysis simply never yields a replacement Plan.
	CompiledPlan *Plan
}

// AnalyzeReport is the result of analyzing an entire XRDConversionConfig
// (one hub, N spokes) against live schemas.
type AnalyzeReport struct {
	ResourceGeneration int64
	SpokeReports       []SpokeReport
}

// OverallLossless reports whether every spoke's mapping is lossless in both
// directions.
func (r AnalyzeReport) OverallLossless() bool {
	for _, s := range r.SpokeReports {
		if !s.Lossless.HubToSpoke || !s.Lossless.SpokeToHub {
			return false
		}
	}
	return true
}

// HasErrors reports whether any spoke's analysis produced an error
// diagnostic (uncovered field, unacknowledged lossy rule, path/type
// mismatch, etc).
func (r AnalyzeReport) HasErrors() bool {
	for _, s := range r.SpokeReports {
		if len(s.Errors) > 0 {
			return true
		}
	}
	return false
}
