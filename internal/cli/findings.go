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
	"sort"
	"strings"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// Stable finding identifiers.
//
// These are the ids a SARIF consumer keys suppressions on and a reviewer
// sees in a code-scanning alert, so they are part of the CLI's compatibility
// surface: renaming one silently un-suppresses every finding somebody
// dismissed. Add ids; do not rename them.
const (
	FindingUnacknowledgedLoss = "convctl/unacknowledged-loss"
	FindingAcknowledgedLoss   = "convctl/acknowledged-loss"
	FindingConversionError    = "convctl/conversion-error"
	FindingSchemaViolation    = "convctl/schema-violation"
	FindingUncoveredField     = "convctl/uncovered-field"
	FindingCoverageGap        = "convctl/rule-never-exercised"
	FindingGoldenDrift        = "convctl/golden-drift"
	FindingConfigError        = "convctl/config-error"
	FindingConfigWarning      = "convctl/config-warning"
)

// Finding severities, named as SARIF and the GitHub workflow commands both
// name them, so neither renderer has to translate.
const (
	SeverityFindingError   = "error"
	SeverityFindingWarning = "warning"
	SeverityFindingNote    = "note"
)

// Finding is one reportable problem, in the one shape every CI format
// renders from.
//
// The point of the type is Location: a finding that knows which line of
// which file produced it lands on the pull-request diff, and every format
// below is a rendering of that same fact.
type Finding struct {
	RuleID   string `json:"ruleId"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	// FieldPath is the schema path the finding is about, when it is about
	// one. Distinct from Location, which is about the config file.
	FieldPath string         `json:"fieldPath,omitempty"`
	Location  SourceLocation `json:"location,omitempty"`
	// Spoke, From, To and Sample carry the context a reviewer needs to act
	// without opening the full report.
	Spoke  string `json:"spoke,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Sample string `json:"sample,omitempty"`
}

// Title is the short form used as a GitHub annotation title and a SARIF
// short description.
func (f Finding) Title() string {
	switch f.RuleID {
	case FindingUnacknowledgedLoss:
		return "Unacknowledged conversion loss"
	case FindingAcknowledgedLoss:
		return "Acknowledged conversion loss"
	case FindingConversionError:
		return "Conversion error"
	case FindingSchemaViolation:
		return "Converted object violates the destination schema"
	case FindingUncoveredField:
		return "Field covered by no rule"
	case FindingCoverageGap:
		return "Rule exercised by no sample"
	case FindingGoldenDrift:
		return "Golden corpus drift"
	case FindingConfigError:
		return "Conversion config error"
	case FindingConfigWarning:
		return "Conversion config warning"
	}
	return f.RuleID
}

// findingsFromAnalyze turns an analysis into findings, attributing each to
// the rule that produced it where the diagnostic names one.
func findingsFromAnalyze(report engine.AnalyzeReport, sm *ConfigSourceMap) []Finding {
	var out []Finding
	for _, sr := range report.SpokeReports {
		// Fields the engine already reported on are not reported again
		// from the coverage lists. The unmapped-field policy decides
		// whether an uncovered field is an error or a warning, and
		// emitting it a second time from FieldCoverage would show a
		// reviewer every uncovered field twice, at two severities.
		diagnosed := map[string]bool{}
		for _, d := range sr.Errors {
			out = append(out, findingFromDiagnostic(d, sr.Version, FindingConfigError, SeverityFindingError, sm))
			if d.FieldPath != "" {
				diagnosed[d.FieldPath] = true
			}
		}
		for _, d := range sr.Warnings {
			out = append(out, findingFromDiagnostic(d, sr.Version, FindingConfigWarning, SeverityFindingWarning, sm))
			if d.FieldPath != "" {
				diagnosed[d.FieldPath] = true
			}
		}
		for _, p := range sr.Uncovered.UncoveredHub {
			if diagnosed[p] {
				continue
			}
			out = append(out, Finding{
				RuleID: FindingUncoveredField, Severity: SeverityFindingWarning, Spoke: sr.Version, FieldPath: p,
				Message:  fmt.Sprintf("hub field %q is covered by no rule for spoke %s", p, sr.Version),
				Location: sm.Spoke(sr.Version),
			})
		}
		for _, p := range sr.Uncovered.UncoveredSpoke {
			if diagnosed[p] {
				continue
			}
			out = append(out, Finding{
				RuleID: FindingUncoveredField, Severity: SeverityFindingWarning, Spoke: sr.Version, FieldPath: p,
				Message:  fmt.Sprintf("spoke field %q is covered by no rule for spoke %s", p, sr.Version),
				Location: sm.Spoke(sr.Version),
			})
		}
	}
	sortFindings(out)
	return out
}

// findingFromDiagnostic keeps the engine's own code when it has one: those
// codes are already stable identifiers, and inventing a second name for the
// same thing would make suppressions depend on which command reported it.
func findingFromDiagnostic(d engine.Diagnostic, spoke, fallbackID, severity string, sm *ConfigSourceMap) Finding {
	id := fallbackID
	if d.Code != "" {
		id = "convctl/" + kebab(d.Code)
	}
	loc := sm.Spoke(spoke)
	if d.RuleIndex >= 0 {
		loc = sm.Rule(spoke, d.RuleIndex)
	}
	return Finding{
		RuleID: id, Severity: severity, Spoke: spoke,
		FieldPath: d.FieldPath, Message: d.Message, Location: loc,
	}
}

// findingsFromReport turns a test run into findings.
//
// An acknowledged loss is reported at note severity rather than dropped: it
// is not a failure, but "this conversion drops this field, on purpose" is
// exactly the thing a reviewer of a config change wants to see on the diff.
func findingsFromReport(rep *Report, sm *ConfigSourceMap) []Finding {
	if rep == nil {
		return nil
	}
	var out []Finding
	for _, s := range rep.Samples {
		for _, p := range s.Paths {
			for _, is := range p.Issues {
				id, sev := findingClassOf(is.Type)
				out = append(out, Finding{
					RuleID: id, Severity: sev, FieldPath: is.Field,
					Message:  fmt.Sprintf("%s → %s: %s (%s)", is.From, is.To, is.Detail, is.Field),
					Location: locationForPath(sm, is, rep.Meta.HubVersion),
					From:     is.From, To: is.To, Sample: is.Sample,
					Spoke: spokeOf(is.From, is.To, rep.Meta.HubVersion),
				})
			}
		}
	}
	for _, rc := range rep.RuleCoverage {
		if rc.MatchedSamples > 0 {
			continue
		}
		spoke, idx := parseRuleID(rc.RuleID)
		out = append(out, Finding{
			RuleID: FindingCoverageGap, Severity: SeverityFindingWarning, Spoke: spoke,
			Message:  fmt.Sprintf("rule %s was exercised by no sample, so nothing here says whether it works", rc.RuleID),
			Location: sm.Rule(spoke, idx),
		})
	}
	if rep.Golden != nil {
		for _, d := range rep.Golden.Drifts {
			out = append(out, Finding{
				RuleID: FindingGoldenDrift, Severity: SeverityFindingError,
				Message:  fmt.Sprintf("%s: %s", d.Kind, driftDetail(d)),
				Location: sm.Document(),
			})
		}
	}
	sortFindings(out)
	return out
}

func driftDetail(d GoldenDrift) string {
	if len(d.Fields) > 0 {
		return d.File + ": " + strings.Join(d.Fields, ", ")
	}
	return d.File + ": " + d.Detail
}

func findingClassOf(issueType string) (id, severity string) {
	switch issueType {
	case "unacknowledged-loss":
		return FindingUnacknowledgedLoss, SeverityFindingError
	case "acknowledged-loss":
		return FindingAcknowledgedLoss, SeverityFindingNote
	case "schema-violation":
		return FindingSchemaViolation, SeverityFindingError
	}
	return FindingConversionError, SeverityFindingError
}

// spokeOf names which spoke a conversion path belongs to: every path has
// one end at the hub, and the other end is the spoke whose rules ran.
func spokeOf(from, to, hub string) string {
	switch {
	case from == hub:
		return to
	case to == hub:
		return from
	}
	// Spoke-to-spoke routes through the hub; the destination's rules are
	// the ones that produced the output being judged.
	return to
}

// locationForPath attributes a field-level issue to the rule that claims
// that destination path, when exactly one does.
//
// Conservative on purpose: two claimants means the answer would be a guess,
// and an annotation on the wrong rule is worse than one on the spoke.
func locationForPath(sm *ConfigSourceMap, is Issue, hub string) SourceLocation {
	return sm.Spoke(spokeOf(is.From, is.To, hub))
}

// parseRuleID splits the "v1:rule[3]:FieldRename" form the coverage report
// uses back into the spoke and index the source map is keyed on.
func parseRuleID(id string) (spoke string, index int) {
	index = -1
	parts := strings.SplitN(id, ":", 3)
	if len(parts) < 2 {
		return id, index
	}
	spoke = parts[0]
	open := strings.Index(parts[1], "[")
	closeIdx := strings.Index(parts[1], "]")
	if open < 0 || closeIdx < open {
		return spoke, index
	}
	n := 0
	if _, err := fmt.Sscanf(parts[1][open+1:closeIdx], "%d", &n); err == nil {
		index = n
	}
	return spoke, index
}

// kebab turns an engine diagnostic code (CamelCase) into the lower-kebab
// form the finding ids use, so convctl/required-field-unsatisfiable rather
// than convctl/RequiredFieldUnsatisfiable.
func kebab(code string) string {
	var b strings.Builder
	for i, r := range code {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sortFindings makes every format deterministic between runs on the same
// input, which is what lets a sticky PR comment update in place instead of
// producing a fresh diff each time.
func sortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		a, b := f[i], f[j]
		if a.Severity != b.Severity {
			return severityOrder(a.Severity) < severityOrder(b.Severity)
		}
		if a.Location.File != b.Location.File {
			return a.Location.File < b.Location.File
		}
		if a.Location.Line != b.Location.Line {
			return a.Location.Line < b.Location.Line
		}
		if a.RuleID != b.RuleID {
			return a.RuleID < b.RuleID
		}
		if a.Sample != b.Sample {
			return a.Sample < b.Sample
		}
		return a.Message < b.Message
	})
}

func severityOrder(s string) int {
	switch s {
	case SeverityFindingError:
		return 0
	case SeverityFindingWarning:
		return 1
	}
	return 2
}

// isCIFormat reports whether an --output value is one of the CI-native
// formats, all of which render the same findings.
func isCIFormat(output string) bool {
	switch output {
	case "github", "sarif", "markdown":
		return true
	}
	return false
}

// checkOutputFormat rejects an unknown --output value rather than silently
// falling back to the table, which is how a CI job ends up reporting success
// from a format nobody rendered.
func checkOutputFormat(output string, allowed ...string) error {
	for _, a := range allowed {
		if output == a {
			return nil
		}
	}
	return fmt.Errorf("invalid --output value %q (want %s)", output, strings.Join(allowed, ", "))
}

// validateFindings renders a validate result as findings.
//
// When the schema analysis ran, its diagnostics carry rule indexes and
// become per-rule annotations. A structural failure has no rule to point at
// and lands on the config file itself — still reported, because a config
// that does not parse is the most serious finding there is.
func validateFindings(res *ValidateResult, configPath string) []Finding {
	sm := SourceMapForConfig(configPath)
	if res.Analysis != nil {
		if f := findingsFromAnalyze(*res.Analysis, sm); len(f) > 0 {
			return f
		}
	}
	var out []Finding
	for _, e := range res.Errors {
		out = append(out, Finding{
			RuleID: FindingConfigError, Severity: SeverityFindingError,
			Message: e, Location: sm.Document(),
		})
	}
	return out
}
