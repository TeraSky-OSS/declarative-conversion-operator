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
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole point of the source map is that a finding lands on the line a
// human edits. A rule's line, not the document's.
func TestSourceMapForConfig_LocatesSpokesAndRules(t *testing.T) {
	m := SourceMapForConfig("testdata/config.yaml")
	if m.File != "testdata/config.yaml" {
		t.Fatalf("File = %q", m.File)
	}
	hub := m.HubVersion()
	if !hub.Known() {
		t.Error("hubVersion has no location")
	}
	spoke := m.Spoke("v1")
	if !spoke.Known() {
		t.Fatal("spoke v1 has no location")
	}
	r0, r1 := m.Rule("v1", 0), m.Rule("v1", 1)
	if !r0.Known() || !r1.Known() {
		t.Fatalf("rules have no locations: %v %v", r0, r1)
	}
	if r0.Line <= spoke.Line {
		t.Errorf("rule 0 at line %d is not after the spoke at line %d", r0.Line, spoke.Line)
	}
	if r1.Line <= r0.Line {
		t.Errorf("rule 1 at line %d is not after rule 0 at line %d", r1.Line, r0.Line)
	}

	// The line has to be the rule the reader would recognise, so check the
	// file actually says what we claim it does there.
	data, err := os.ReadFile("testdata/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if got := strings.TrimSpace(lines[r0.Line-1]); !strings.Contains(got, "strategy:") {
		t.Errorf("rule 0 points at %q, want the line declaring the strategy", got)
	}
}

// An unknown spoke or rule index must still answer with somewhere real: a
// finding with no file at all cannot be rendered by any of the formats.
func TestSourceMapForConfig_FallsBackRatherThanReturningNothing(t *testing.T) {
	m := SourceMapForConfig("testdata/config.yaml")
	if got := m.Spoke("v99"); got.File == "" {
		t.Error("an unknown spoke returned no location at all")
	}
	if got := m.Rule("v1", 99); !got.Known() {
		t.Errorf("an out-of-range rule index returned %v, want the spoke's location", got)
	}
	if got := m.Rule("v99", 0); got.File == "" {
		t.Error("an unknown spoke's rule returned no location at all")
	}
}

// A config that does not parse for positions must not take the report down
// with it: the typed loader has already accepted these bytes, and a report
// without line numbers beats no report.
func TestSourceMapForConfig_SurvivesAnUnparseableFile(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("spec: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := SourceMapForConfig(bad)
	if m.Document().File != bad {
		t.Errorf("Document() = %v, want the file with no line", m.Document())
	}
	if got := SourceMapForConfig(filepath.Join(dir, "missing.yaml")); got == nil {
		t.Error("a missing file returned a nil map, which would panic every caller")
	}
}

// A literal newline ends a workflow command, dropping everything after it —
// which is how a multi-line diagnostic silently becomes a one-line one.
func TestWriteGitHubCommands_EscapesData(t *testing.T) {
	var buf bytes.Buffer
	err := WriteGitHubCommands(&buf, []Finding{{
		RuleID: FindingUnacknowledgedLoss, Severity: SeverityFindingError,
		Message:  "first line\nsecond line 100% of the time",
		Location: SourceLocation{File: "config.yaml", Line: 12, Column: 3},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimRight(buf.String(), "\n")
	if strings.Count(got, "\n") != 0 {
		t.Fatalf("command spans multiple lines, so the runner would drop part of it:\n%s", got)
	}
	for _, want := range []string{"::error ", "file=config.yaml", "line=12", "col=3", "%0A", "%25"} {
		if !strings.Contains(got, want) {
			t.Errorf("command is missing %q:\n%s", want, got)
		}
	}
}

// A property value containing a colon or comma would be read as the end of
// the property, silently corrupting the file name a finding points at.
func TestWriteGitHubCommands_EscapesProperties(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteGitHubCommands(&buf, []Finding{{
		RuleID: FindingConfigError, Severity: SeverityFindingWarning,
		Message:  "m",
		Location: SourceLocation{File: "weird,name:config.yaml", Line: 1},
	}}, "")
	got := buf.String()
	if strings.Contains(got, "weird,name:config.yaml") {
		t.Errorf("separators in the file name were not escaped:\n%s", got)
	}
	if !strings.Contains(got, "%2C") || !strings.Contains(got, "%3A") {
		t.Errorf("want the comma and colon percent-encoded:\n%s", got)
	}
}

// GitHub spells the lowest level "notice"; SARIF spells it "note". Getting
// this wrong makes the runner ignore the command entirely.
func TestWriteGitHubCommands_UsesNoticeForNotes(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteGitHubCommands(&buf, []Finding{{RuleID: FindingAcknowledgedLoss, Severity: SeverityFindingNote, Message: "m"}}, "")
	if !strings.HasPrefix(buf.String(), "::notice ") {
		t.Errorf("want ::notice, got %q", buf.String())
	}
}

// The job summary is where a person looks; the commands are for the runner.
// Both have to be produced, and the summary only when the runner asked.
func TestWriteGitHubCommands_WritesTheStepSummaryWhenAsked(t *testing.T) {
	dir := t.TempDir()
	summary := filepath.Join(dir, "summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", summary)

	var buf bytes.Buffer
	if err := WriteGitHubCommands(&buf, []Finding{{RuleID: FindingConfigError, Severity: SeverityFindingError, Message: "boom"}}, "convctl"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(summary)
	if err != nil {
		t.Fatalf("no job summary written: %v", err)
	}
	if !strings.Contains(string(data), "boom") || !strings.Contains(string(data), "| Severity |") {
		t.Errorf("summary is not the findings table:\n%s", data)
	}
}

// Without the variable — on a laptop — the same command has to work.
func TestWriteGitHubCommands_NoSummaryVariableIsNotAnError(t *testing.T) {
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	var buf bytes.Buffer
	if err := WriteGitHubCommands(&buf, []Finding{{RuleID: FindingConfigError, Severity: SeverityFindingError, Message: "m"}}, "t"); err != nil {
		t.Errorf("running outside a runner failed: %v", err)
	}
}

func TestWriteSARIF_IsValidAndCarriesRulesAndLocations(t *testing.T) {
	var buf bytes.Buffer
	err := WriteSARIF(&buf, []Finding{
		{RuleID: FindingUnacknowledgedLoss, Severity: SeverityFindingError, Message: "lost a field",
			Location: SourceLocation{File: "./config.yaml", Line: 12, Column: 5}},
		{RuleID: FindingConfigError, Severity: SeverityFindingError, Message: "whole-config problem",
			Location: SourceLocation{File: "config.yaml"}},
		{RuleID: FindingAcknowledgedLoss, Severity: SeverityFindingNote, Message: "declared lossy"},
	}, "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}

	var log map[string]any
	if err := json.Unmarshal(buf.Bytes(), &log); err != nil {
		t.Fatalf("SARIF is not valid JSON: %v", err)
	}
	if log["version"] != "2.1.0" {
		t.Errorf("version = %v, want 2.1.0 (the only one code scanning accepts)", log["version"])
	}
	runs, _ := log["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	run, _ := runs[0].(map[string]any)
	driver, _ := run["tool"].(map[string]any)["driver"].(map[string]any)
	if driver["version"] != "v1.2.3" {
		t.Errorf("driver version = %v, want the tool version", driver["version"])
	}
	rules, _ := driver["rules"].([]any)
	if len(rules) != 3 {
		t.Errorf("rules = %d, want one per distinct finding id", len(rules))
	}
	results, _ := run["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("results = %d, want one per finding", len(results))
	}

	// A finding with no line still needs a location, and a whole-config
	// error is the most serious kind to drop.
	second, _ := results[1].(map[string]any)
	locs, _ := second["locations"].([]any)
	if len(locs) != 1 {
		t.Errorf("a locationless-but-filed finding lost its location: %v", second)
	}
	// And one with no file at all must not invent one.
	third, _ := results[2].(map[string]any)
	if _, ok := third["locations"]; ok {
		t.Errorf("a finding with no file was given a location: %v", third)
	}
}

// A code-scanning alert can only be matched to a file when the URI is
// slash-separated, whatever platform produced it.
func TestToSARIFURI_NormalisesSeparatorsAndPrefixes(t *testing.T) {
	for in, want := range map[string]string{
		"./a/b.yaml":     "a/b.yaml",
		`dir\sub\c.yaml`: "dir/sub/c.yaml",
		"plain.yaml":     "plain.yaml",
	} {
		if got := toSARIFURI(in); got != want {
			t.Errorf("toSARIFURI(%q) = %q, want %q", in, got, want)
		}
	}
}

// Stable ids are a compatibility surface: renaming one silently
// un-suppresses every finding somebody dismissed in code scanning.
func TestFindingIDs_AreStableAndNamespaced(t *testing.T) {
	for _, id := range []string{
		FindingUnacknowledgedLoss, FindingAcknowledgedLoss, FindingConversionError,
		FindingSchemaViolation, FindingUncoveredField, FindingCoverageGap,
		FindingGoldenDrift, FindingConfigError, FindingConfigWarning,
	} {
		if !strings.HasPrefix(id, "convctl/") {
			t.Errorf("%q is not namespaced", id)
		}
		if strings.ToLower(id) != id {
			t.Errorf("%q is not lower-kebab, so it will be inconsistent with engine-derived ids", id)
		}
	}
	if got := kebab("RequiredFieldUnsatisfiable"); got != "required-field-unsatisfiable" {
		t.Errorf("kebab() = %q", got)
	}
}

// A message containing a pipe would otherwise break the table it is in.
func TestWriteFindingsMarkdown_EscapesAndBounds(t *testing.T) {
	var many []Finding
	for i := 0; i < maxMarkdownRows+10; i++ {
		many = append(many, Finding{RuleID: FindingConfigError, Severity: SeverityFindingError, Message: "a | b"})
	}
	var buf bytes.Buffer
	WriteFindingsMarkdown(&buf, many, "t")
	got := buf.String()
	if strings.Contains(got, "| a | b |") {
		t.Error("an unescaped pipe broke the table")
	}
	if !strings.Contains(got, "more finding(s) not shown") {
		t.Error("an unbounded table was rendered; GitHub truncates it mid-sentence")
	}
	if n := strings.Count(got, "convctl/config-error"); n != maxMarkdownRows {
		t.Errorf("rendered %d rows, want the cap of %d", n, maxMarkdownRows)
	}
}

func TestWriteFindingsMarkdown_SaysSoWhenClean(t *testing.T) {
	var buf bytes.Buffer
	WriteFindingsMarkdown(&buf, nil, "convctl validate")
	if !strings.Contains(buf.String(), "No findings.") {
		t.Errorf("a clean run rendered %q", buf.String())
	}
}

// Determinism is what lets a sticky PR comment update in place instead of
// producing a fresh diff on every run.
func TestSortFindings_IsDeterministicAndSeverityOrdered(t *testing.T) {
	in := []Finding{
		{RuleID: "convctl/b", Severity: SeverityFindingNote, Message: "n", Location: SourceLocation{File: "b.yaml", Line: 2}},
		{RuleID: "convctl/a", Severity: SeverityFindingError, Message: "e", Location: SourceLocation{File: "b.yaml", Line: 9}},
		{RuleID: "convctl/c", Severity: SeverityFindingWarning, Message: "w", Location: SourceLocation{File: "a.yaml", Line: 1}},
	}
	first := append([]Finding{}, in...)
	sortFindings(first)
	if first[0].Severity != SeverityFindingError || first[2].Severity != SeverityFindingNote {
		t.Fatalf("not ordered by severity: %+v", first)
	}
	shuffled := []Finding{in[2], in[0], in[1]}
	sortFindings(shuffled)
	for i := range first {
		if shuffled[i] != first[i] {
			t.Fatalf("ordering depends on input order at %d: %+v vs %+v", i, shuffled[i], first[i])
		}
	}
}

// The coverage report identifies rules as "v1:rule[3]:FieldRename"; the
// source map is keyed on the spoke and the index.
func TestParseRuleID(t *testing.T) {
	spoke, idx := parseRuleID("v1:rule[3]:FieldRename")
	if spoke != "v1" || idx != 3 {
		t.Errorf("parseRuleID = %q,%d want v1,3", spoke, idx)
	}
	if s, i := parseRuleID("nonsense"); s != "nonsense" || i != -1 {
		t.Errorf("parseRuleID(nonsense) = %q,%d want nonsense,-1", s, i)
	}
}

// An unknown format has to be rejected rather than silently falling back to
// the table, which is how a job reports success from output nobody rendered.
func TestCheckOutputFormat_RejectsUnknown(t *testing.T) {
	if err := checkOutputFormat("sarif", "table", "json", "sarif"); err != nil {
		t.Errorf("a valid format was rejected: %v", err)
	}
	err := checkOutputFormat("saarif", "table", "json", "sarif")
	if err == nil {
		t.Fatal("a typo was accepted")
	}
	if !strings.Contains(err.Error(), "saarif") || !strings.Contains(err.Error(), "sarif") {
		t.Errorf("error should name what was given and what is accepted: %v", err)
	}
}

// The formats exist to put a finding on the line that caused it, so the
// mapping from a real run has to produce real locations.
func TestFindingsFromAnalyze_AttributesToTheConfigLines(t *testing.T) {
	out, err := RunAnalyze("../../examples/crossplane-xr-multiversion/02-add-v2/xrd.yaml", "",
		"../../examples/crossplane-xr-multiversion/mistakes/02-missing-rename.yaml")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if out.Analysis == nil {
		t.Fatal("analyze did not carry its report, so no CI format can attribute anything")
	}
	findings := findingsFromAnalyze(*out.Analysis, SourceMapForConfig("../../examples/crossplane-xr-multiversion/mistakes/02-missing-rename.yaml"))
	if len(findings) == 0 {
		t.Fatal("a config with an uncovered field produced no findings")
	}
	for _, f := range findings {
		if !f.Location.Known() {
			t.Errorf("finding has no line: %+v", f)
		}
	}
}

// The engine already reports an uncovered field; reporting it again from
// the coverage lists shows a reviewer the same field twice, at two
// severities, which reads as two problems.
func TestFindingsFromAnalyze_DoesNotReportUncoveredFieldsTwice(t *testing.T) {
	out, err := RunAnalyze("../../examples/crossplane-xr-multiversion/02-add-v2/xrd.yaml", "",
		"../../examples/crossplane-xr-multiversion/mistakes/02-missing-rename.yaml")
	if err != nil {
		t.Fatal(err)
	}
	findings := findingsFromAnalyze(*out.Analysis, SourceMapForConfig("x.yaml"))
	seen := map[string]int{}
	for _, f := range findings {
		if f.FieldPath != "" {
			seen[f.Spoke+"/"+f.FieldPath]++
		}
	}
	for k, n := range seen {
		if n > 1 {
			t.Errorf("%s reported %d times", k, n)
		}
	}
}

// A test run's findings have to reach the file the user passed, not the
// config's metadata.name — which is not a path anyone can open.
func TestFindingsFromReport_UsesTheConfigPath(t *testing.T) {
	rep, err := RunTest(TestOptions{
		XRDPath: "testdata/full/xrd.yaml", ConfigPath: "testdata/full/config.yaml",
		SamplesDir: "testdata/full/samples", Quiet: true,
	})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if rep.Meta.ConfigPath != "testdata/full/config.yaml" {
		t.Fatalf("Meta.ConfigPath = %q, want the path as typed", rep.Meta.ConfigPath)
	}
	findings := findingsFromReport(rep, SourceMapForConfig(rep.Meta.ConfigPath))
	if len(findings) == 0 {
		t.Fatal("a run with acknowledged losses produced no findings")
	}
	for _, f := range findings {
		if f.Location.File != "testdata/full/config.yaml" {
			t.Fatalf("finding points at %q, not at the config file", f.Location.File)
		}
	}
}

// An acknowledged loss is not a failure, but it is exactly what a reviewer
// of a config change wants to see — so it is reported at note severity
// rather than dropped.
func TestFindingsFromReport_ClassifiesBySeverity(t *testing.T) {
	rep := &Report{}
	rep.Meta.HubVersion = "v2"
	rep.Samples = []SampleResult{{File: "s.yaml", Paths: []PathResult{{From: "v2", To: "v1", Issues: []Issue{
		{Field: "spec.a", From: "v2", To: "v1", Type: "acknowledged-loss", Detail: "declared"},
		{Field: "spec.b", From: "v2", To: "v1", Type: "unacknowledged-loss", Detail: "undeclared"},
		{Field: "spec.c", From: "v2", To: "v1", Type: "schema-violation", Detail: "invalid"},
		{Field: "(conversion)", From: "v2", To: "v1", Type: "error", Detail: "boom"},
	}}}}}
	got := map[string]string{}
	for _, f := range findingsFromReport(rep, SourceMapForConfig("nope.yaml")) {
		got[f.RuleID] = f.Severity
	}
	for id, want := range map[string]string{
		FindingAcknowledgedLoss:   SeverityFindingNote,
		FindingUnacknowledgedLoss: SeverityFindingError,
		FindingSchemaViolation:    SeverityFindingError,
		FindingConversionError:    SeverityFindingError,
	} {
		if got[id] != want {
			t.Errorf("%s severity = %q, want %q", id, got[id], want)
		}
	}
}

// A fleet run that could not reach a cluster must report that as a finding.
// Silence there is how a check covers four of five clusters and reports
// green.
func TestFleetFindings_ReportAnUnreachableCluster(t *testing.T) {
	f := &FleetReport{Clusters: []ClusterResult{{Label: "prod-eu", Error: "dial tcp: timeout"}}}
	got := f.findings()
	if len(got) != 1 {
		t.Fatalf("findings = %+v, want the unreachable cluster reported", got)
	}
	if got[0].Severity != SeverityFindingError || !strings.Contains(got[0].Message, "prod-eu") {
		t.Errorf("finding does not name the cluster as an error: %+v", got[0])
	}
}
