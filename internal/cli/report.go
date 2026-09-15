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
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Issue is one detected field-level problem on a specific conversion path.
type Issue struct {
	Field  string `json:"field"`
	From   string `json:"from"`
	To     string `json:"to"`
	Type   string `json:"type"` // "acknowledged-loss" | "unacknowledged-loss"
	Detail string `json:"detail"`
	Sample string `json:"sample,omitempty"`
}

// write renders the corpus outcome. Each drift kind has its own remedy, so
// they are grouped rather than listed as one undifferentiated failure.
func (g *GoldenReport) write(w io.Writer) {
	if g == nil {
		return
	}
	if g.Mode == "record" {
		_, _ = fmt.Fprintf(w, "GOLDEN CORPUS: recorded %d conversion(s) into %s\n\n", g.Written, g.Dir)
		return
	}
	if len(g.Drifts) == 0 {
		_, _ = fmt.Fprintf(w, "GOLDEN CORPUS: %s — no drift\n\n", g.Dir)
		return
	}
	_, _ = fmt.Fprintf(w, "GOLDEN CORPUS: %s — %d difference(s)\n", g.Dir, len(g.Drifts))
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  KIND\tFILE\tDETAIL")
	for _, d := range g.Drifts {
		detail := d.Detail
		if len(d.Fields) > 0 {
			detail = strings.Join(d.Fields, ", ")
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", d.Kind, d.File, detail)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w)
}

// driftFatal reports whether the corpus outcome should fail the run. A
// manifest mismatch alone is a warning: the corpus may legitimately predate
// a config edit, and the field diffs are the real evidence either way.
func (g *GoldenReport) driftFatal() bool {
	if g == nil {
		return false
	}
	for _, d := range g.Drifts {
		if d.Kind != "manifest" {
			return true
		}
	}
	return false
}

// FuzzMeta records how a --fuzz run was generated.
type FuzzMeta struct {
	Objects int   `json:"objects"`
	Seed    int64 `json:"seed"`
}

// GoldenReport is the outcome of a --record or --golden run.
type GoldenReport struct {
	Dir     string        `json:"dir"`
	Mode    string        `json:"mode"`
	Written int           `json:"written,omitempty"`
	Drifts  []GoldenDrift `json:"drifts,omitempty"`
}

// PathResult is one sample tested along one conversion path.
type PathResult struct {
	From            string   `json:"from"`
	To              string   `json:"to"`
	Result          string   `json:"result"` // pass|loss|fail
	TimingMicros    int64    `json:"timingMicros"`
	FieldsConverted int      `json:"fieldsConverted"`
	RulesMatched    []string `json:"rulesMatched,omitempty"`
	Issues          []Issue  `json:"issues,omitempty"`
}

// SampleResult is one sample file tested across every conversion path.
type SampleResult struct {
	File            string       `json:"file"`
	AssertedVersion string       `json:"assertedVersion"`
	Paths           []PathResult `json:"paths"`
	// CRD and CRDRole attribute a --live sample to the generated CRD it
	// came from. A claim-offering XRD generates two, and a failure that
	// does not say which half it is in is much harder to act on. Empty for
	// file-loaded samples.
	CRD     string `json:"crd,omitempty"`
	CRDRole string `json:"crdRole,omitempty"`
}

// CRDSampleCount is the per-generated-CRD breakdown of a --live run.
type CRDSampleCount struct {
	CRD     string `json:"crd"`
	Role    string `json:"role"`
	Samples int    `json:"samples"`
}

// RuleCoverage reports whether a declared rule was exercised by any sample.
type RuleCoverage struct {
	RuleID         string `json:"ruleId"`
	MatchedSamples int    `json:"matchedSamples"`
}

// Report is the full convctl test result, also used (in reduced form)
// by analyze/validate for JSON output.
type Report struct {
	Meta struct {
		ResourceKind string `json:"resourceKind"` // "XRD" or "CRD"
		Resource     string `json:"resource"`
		Config       string `json:"config"`
		// ConfigPath is the file the config was read from, as typed. The
		// CI output formats report findings against it, and metadata.name
		// is not a path anyone can open.
		ConfigPath     string   `json:"configPath,omitempty"`
		HubVersion     string   `json:"hubVersion"`
		ServedVersions []string `json:"servedVersions"`
		GeneratedAt    string   `json:"generatedAt,omitempty"`
		DurationMs     float64  `json:"durationMs"`
		// Fuzz records the generated-object count and the seed that
		// produced them. The seed is printed on failure and on success:
		// a fuzz failure nobody can reproduce is noise, and the seed is
		// the whole reproduction.
		Fuzz *FuzzMeta `json:"fuzz,omitempty"`
		// Sampling is present only when a --live run was bounded. Its
		// absence means every live object was tested; a sampled result
		// that reads like an exhaustive one is the failure this guards
		// against.
		Sampling *SamplingReport `json:"sampling,omitempty"`
		// Scope is the detected Crossplane scope (XRD targets only) —
		// which injected-field set is in play, and how much the resolver
		// trusts the answer. See pkg/xrdadapter.ResolveScope.
		Scope *ScopeView `json:"scope,omitempty"`
	} `json:"meta"`
	// Propagation is present only for --live --verify-propagation runs
	// against an XRD.
	Propagation *PropagationReport `json:"propagation,omitempty"`
	// Golden is present only for --record or --golden runs.
	Golden  *GoldenReport `json:"golden,omitempty"`
	Summary struct {
		Samples            int `json:"samples"`
		PathsTested        int `json:"pathsTested"`
		Pass               int `json:"pass"`
		AcknowledgedLoss   int `json:"acknowledgedLoss"`
		UnacknowledgedLoss int `json:"unacknowledgedLoss"`
		Errors             int `json:"errors"`
		// SamplesByCRD breaks the sample count down per generated CRD.
		// Present only for --live runs against an XRD that generates more
		// than one (a LegacyCluster XRD with claimNames).
		SamplesByCRD []CRDSampleCount `json:"samplesByCRD,omitempty"`
	} `json:"summary"`
	Samples      []SampleResult `json:"samples"`
	RuleCoverage []RuleCoverage `json:"ruleCoverage,omitempty"`
}

// WriteTable renders the report as a human-readable terminal report. Write
// errors to a terminal/CI log capture are not actionable, so they're
// deliberately ignored here rather than threaded back through a chain of
// callers that could do nothing useful with them either.
func (r *Report) WriteTable(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%s Conversion Test Report\n", r.Meta.ResourceKind)
	_, _ = fmt.Fprintf(w, "%s: %s\tConfig: %s (hub: %s)\n", r.Meta.ResourceKind, r.Meta.Resource, r.Meta.Config, r.Meta.HubVersion)
	r.Meta.Scope.write(w)
	_, _ = fmt.Fprintf(w, "Samples: %d\tPaths tested: %d\tTotal time: %.1fms\n", r.Summary.Samples, r.Summary.PathsTested, r.Meta.DurationMs)
	for _, c := range r.Summary.SamplesByCRD {
		_, _ = fmt.Fprintf(w, "  %s (%s): %d samples\n", c.CRD, c.Role, c.Samples)
	}
	_, _ = fmt.Fprintln(w)

	if r.Meta.Fuzz != nil {
		_, _ = fmt.Fprintf(w, "FUZZ: %d generated object(s), seed %d — reproduce with --fuzz %d --seed %d\n\n",
			r.Meta.Fuzz.Objects, r.Meta.Fuzz.Seed, r.Meta.Fuzz.Objects, r.Meta.Fuzz.Seed)
	}
	if r.Meta.Sampling != nil {
		_, _ = fmt.Fprintf(w, "%s\n\n", r.Meta.Sampling.String())
	}
	r.Golden.write(w)

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SAMPLE\tPATH\tRESULT\tFIELDS\tTIME(µs)\tRULES MATCHED")
	for _, s := range r.Samples {
		for i, p := range s.Paths {
			sampleCol := ""
			if i == 0 {
				sampleCol = s.File
			}
			rules := strings.Join(p.RulesMatched, ",")
			if rules == "" {
				rules = "(identity)"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s→%s\t%s\t%d\t%d\t%s\n", sampleCol, p.From, p.To, strings.ToUpper(p.Result), p.FieldsConverted, p.TimingMicros, rules)
		}
	}
	_ = tw.Flush()

	var issues []Issue
	for _, s := range r.Samples {
		for _, p := range s.Paths {
			issues = append(issues, p.Issues...)
		}
	}
	if len(issues) > 0 {
		_, _ = fmt.Fprintf(w, "\nISSUES (%d)\n", len(issues))
		tw2 := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw2, "SAMPLE\tFIELD\tFROM → TO\tTYPE\tDETAIL")
		for _, is := range issues {
			_, _ = fmt.Fprintf(tw2, "%s\t%s\t%s → %s\t%s\t%s\n", is.Sample, is.Field, is.From, is.To, is.Type, is.Detail)
		}
		_ = tw2.Flush()
	}

	if len(r.RuleCoverage) > 0 {
		_, _ = fmt.Fprintln(w, "\nRULE COVERAGE")
		sorted := append([]RuleCoverage{}, r.RuleCoverage...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].RuleID < sorted[j].RuleID })
		for _, rc := range sorted {
			if rc.MatchedSamples == 0 {
				_, _ = fmt.Fprintf(w, "  %s\tNOT EXERCISED by any sample (warning)\n", rc.RuleID)
			} else {
				_, _ = fmt.Fprintf(w, "  %s\tmatched %d sample(s)\n", rc.RuleID, rc.MatchedSamples)
			}
		}
	}

	if r.Propagation != nil {
		r.Propagation.WriteTable(w)
	}

	_, _ = fmt.Fprintln(w)
	r.WriteSummaryLine(w)
}

// WriteSummaryLine renders just the one-line pass/loss/fail/error summary,
// without the rest of the table — used when the full report is written to
// a file (--output-file) but a short result still belongs on the screen.
func (r *Report) WriteSummaryLine(w io.Writer) {
	_, _ = fmt.Fprintf(w, "SUMMARY: %d samples, %d paths — %d PASS, %d LOSS(acknowledged), %d FAIL(unacknowledged loss), %d ERROR\n",
		r.Summary.Samples, r.Summary.PathsTested, r.Summary.Pass, r.Summary.AcknowledgedLoss, r.Summary.UnacknowledgedLoss, r.Summary.Errors)
}

func nowRFC3339() string { return time.Now().Format(time.RFC3339) }

// junitTestSuites/junitTestSuite/junitTestCase/junitMessage model just
// enough of the de facto JUnit XML schema for CI systems (GitHub Actions,
// GitLab, Jenkins) to render pass/fail/error per conversion path — one
// <testsuite> for the whole report, one <testcase> per sample/path pair.
type junitTestSuites struct {
	XMLName  xml.Name         `xml:"testsuites"`
	Name     string           `xml:"name,attr"`
	Tests    int              `xml:"tests,attr"`
	Failures int              `xml:"failures,attr"`
	Errors   int              `xml:"errors,attr"`
	Time     string           `xml:"time,attr"`
	Suites   []junitTestSuite `xml:"testsuite"`
}

type junitTestSuite struct {
	Name     string           `xml:"name,attr"`
	Tests    int              `xml:"tests,attr"`
	Failures int              `xml:"failures,attr"`
	Errors   int              `xml:"errors,attr"`
	Time     string           `xml:"time,attr"`
	Props    *junitProperties `xml:"properties,omitempty"`
	Cases    []junitTestCase  `xml:"testcase"`
}

type junitTestCase struct {
	Name      string           `xml:"name,attr"`
	Classname string           `xml:"classname,attr"`
	Time      string           `xml:"time,attr"`
	Props     *junitProperties `xml:"properties,omitempty"`
	Failure   *junitMessage    `xml:"failure,omitempty"`
	Error     *junitMessage    `xml:"error,omitempty"`
	SystemOut string           `xml:"system-out,omitempty"`
}

// junitProperties carries the generated CRD a --live sample came from, so
// a CI system rendering the report can tell a claim failure from a
// composite one without parsing the case name.
type junitProperties struct {
	Properties []junitProperty `xml:"property"`
}

type junitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Content string `xml:",chardata"`
}

// WriteJUnit renders the report as JUnit XML — one testcase per sample/path
// pair tested. A "pass" is a bare testcase; an acknowledged "loss" is a
// passing testcase with the round-trip detail attached as system-out
// (informational, not a failure); "fail" (unacknowledged loss) and "error"
// map to <failure>/<error> respectively, so CI systems that only surface
// failed testcases still catch exactly what --fail-on would.
func (r *Report) junitSuite() junitTestSuite {
	suite := junitTestSuite{Name: fmt.Sprintf("%s/%s", r.Meta.ResourceKind, r.Meta.Resource)}
	// A sampled run has to say so in every format it can be read in. A
	// JUnit reporter showing 50 green tests, from a population of 40,000,
	// with nothing saying which, is the exact false confidence the cap
	// exists to make explicit.
	if r.Meta.Sampling != nil {
		suite.Props = &junitProperties{Properties: []junitProperty{
			{Name: "sampled", Value: "true"},
			{Name: "sampleStrategy", Value: r.Meta.Sampling.Strategy},
			{Name: "samplePopulation", Value: strconv.Itoa(r.Meta.Sampling.Population)},
			{Name: "sampleTested", Value: strconv.Itoa(r.Meta.Sampling.Tested)},
		}}
	}
	var totalTime float64
	for _, s := range r.Samples {
		for _, p := range s.Paths {
			seconds := float64(p.TimingMicros) / 1e6
			totalTime += seconds
			tc := junitTestCase{
				Name:      fmt.Sprintf("%s: %s→%s", s.File, p.From, p.To),
				Classname: fmt.Sprintf("%s.%s", r.Meta.Config, r.Meta.HubVersion),
				Time:      fmt.Sprintf("%.6f", seconds),
			}
			if s.CRD != "" {
				tc.Props = &junitProperties{Properties: []junitProperty{
					{Name: "crd", Value: s.CRD},
					{Name: "crdRole", Value: s.CRDRole},
				}}
			}
			switch p.Result {
			case "fail":
				tc.Failure = &junitMessage{Message: "unacknowledged loss", Content: issuesText(p.Issues)}
				suite.Failures++
			case "error":
				tc.Error = &junitMessage{Message: "conversion error", Content: issuesText(p.Issues)}
				suite.Errors++
			case "loss":
				tc.SystemOut = issuesText(p.Issues)
			}
			suite.Cases = append(suite.Cases, tc)
		}
	}
	suite.Tests = len(suite.Cases)
	suite.Time = fmt.Sprintf("%.6f", totalTime)
	return suite
}

func writeJUnitSuites(w io.Writer, root junitTestSuites) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(root); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func (r *Report) WriteJUnit(w io.Writer) error {
	suite := r.junitSuite()
	root := junitTestSuites{
		Name:     fmt.Sprintf("%s conversion test: %s", r.Meta.ResourceKind, r.Meta.Resource),
		Tests:    suite.Tests,
		Failures: suite.Failures,
		Errors:   suite.Errors,
		Time:     suite.Time,
		Suites:   []junitTestSuite{suite},
	}
	return writeJUnitSuites(w, root)
}

func issuesText(issues []Issue) string {
	if len(issues) == 0 {
		return ""
	}
	var b strings.Builder
	for _, is := range issues {
		fmt.Fprintf(&b, "%s: %s (%s -> %s): %s\n", is.Type, is.Field, is.From, is.To, is.Detail)
	}
	return b.String()
}
