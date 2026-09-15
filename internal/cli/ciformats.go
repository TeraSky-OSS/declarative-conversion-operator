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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// sarifVersion is the only SARIF version GitHub code scanning accepts.
const (
	sarifVersion = "2.1.0"
	sarifSchema  = "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/main/sarif-2.1/schema/sarif-schema-2.1.0.json"
)

// maxMarkdownRows bounds a PR comment. A thousand-row table is not review
// material, and GitHub truncates it anyway — better to say so and point at
// the full report than to be cut off mid-sentence by somebody else.
const maxMarkdownRows = 50

// WriteGitHubCommands renders findings as GitHub workflow commands, which is
// what puts a finding on the line of the file in the pull-request diff
// rather than in a log.
//
// Commands go to stdout (the runner reads them from the step's output); the
// human-readable table goes to $GITHUB_STEP_SUMMARY when that is set, which
// is the same information in the place a person looks.
func WriteGitHubCommands(w io.Writer, findings []Finding, title string) error {
	for _, f := range findings {
		level := f.Severity
		if level == SeverityFindingNote {
			level = "notice" // GitHub's spelling
		}
		var props []string
		if f.Location.File != "" {
			props = append(props, "file="+escapeCommandProperty(f.Location.File))
		}
		if f.Location.Line > 0 {
			props = append(props, fmt.Sprintf("line=%d", f.Location.Line))
			if f.Location.Column > 0 {
				props = append(props, fmt.Sprintf("col=%d", f.Location.Column))
			}
		}
		props = append(props, "title="+escapeCommandProperty(f.Title()))
		if _, err := fmt.Fprintf(w, "::%s %s::%s\n", level, strings.Join(props, ","), escapeCommandData(f.Message)); err != nil {
			return err
		}
	}
	return writeStepSummary(findings, title)
}

// writeStepSummary appends the markdown table to $GITHUB_STEP_SUMMARY when
// the runner provides one. Absent the variable this is a no-op rather than
// an error: the same command has to work on a laptop.
func writeStepSummary(findings []Finding, title string) error {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return nil
	}
	// #nosec G304,G703 -- the path is the runner's own, read from the
	// variable the runner itself set; there is no user input on this path,
	// and writing somewhere else would defeat the feature.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("writing the job summary: %w", err)
	}
	defer func() { _ = f.Close() }()
	WriteFindingsMarkdown(f, findings, title)
	return nil
}

// escapeCommandData escapes the message body of a workflow command. A
// literal newline would end the command and drop everything after it, which
// is how a multi-line diagnostic becomes a one-line one.
func escapeCommandData(s string) string {
	r := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")
	return r.Replace(s)
}

// escapeCommandProperty escapes a property value, which additionally cannot
// contain the separators the command syntax uses.
func escapeCommandProperty(s string) string {
	r := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C")
	return r.Replace(s)
}

// WriteFindingsMarkdown renders findings as a markdown table for a PR
// comment or a job summary.
//
// Deterministic between runs on the same input — no timestamps, fixed
// ordering — so a sticky comment updates in place rather than producing a
// fresh diff every run.
func WriteFindingsMarkdown(w io.Writer, findings []Finding, title string) {
	if title != "" {
		_, _ = fmt.Fprintf(w, "### %s\n\n", title)
	}
	if len(findings) == 0 {
		_, _ = fmt.Fprintln(w, "No findings.")
		return
	}

	counts := map[string]int{}
	for _, f := range findings {
		counts[f.Severity]++
	}
	_, _ = fmt.Fprintf(w, "%d error(s), %d warning(s), %d note(s).\n\n",
		counts[SeverityFindingError], counts[SeverityFindingWarning], counts[SeverityFindingNote])

	_, _ = fmt.Fprintln(w, "| Severity | Finding | Location | Detail |")
	_, _ = fmt.Fprintln(w, "|---|---|---|---|")
	shown := findings
	if len(shown) > maxMarkdownRows {
		shown = shown[:maxMarkdownRows]
	}
	for _, f := range shown {
		_, _ = fmt.Fprintf(w, "| %s | `%s` | %s | %s |\n",
			f.Severity, f.RuleID, mdEscape(f.Location.String()), mdEscape(f.Message))
	}
	if len(findings) > len(shown) {
		_, _ = fmt.Fprintf(w, "\n_%d more finding(s) not shown — see the uploaded report for the full list._\n",
			len(findings)-len(shown))
	}
}

// mdEscape keeps a message containing a pipe from breaking the table it is
// rendered into.
func mdEscape(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}

// SARIF 2.1.0, in the subset GitHub code scanning reads.
type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri,omitempty"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID               string            `json:"id"`
	Name             string            `json:"name,omitempty"`
	ShortDescription sarifText         `json:"shortDescription"`
	FullDescription  *sarifText        `json:"fullDescription,omitempty"`
	HelpURI          string            `json:"helpUri,omitempty"`
	Properties       map[string]string `json:"properties,omitempty"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   sarifText       `json:"message"`
	Locations []sarifLocation `json:"locations,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           *sarifRegion          `json:"region,omitempty"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn,omitempty"`
}

const findingsHelpURI = "https://terasky-oss.github.io/declarative-conversion-operator/cli/#finding-ids"

// WriteSARIF renders findings as SARIF 2.1.0, so they land in GitHub code
// scanning — and therefore on the pull-request diff and in the Security tab,
// where they can be triaged and suppressed like any other scanner's.
func WriteSARIF(w io.Writer, findings []Finding, toolVersion string) error {
	log := sarifLog{Schema: sarifSchema, Version: sarifVersion}
	run := sarifRun{Tool: sarifTool{Driver: sarifDriver{
		Name:           "convctl",
		Version:        toolVersion,
		InformationURI: "https://github.com/TeraSky-OSS/declarative-conversion-operator",
	}}}

	// One rule per id actually present, so the rules array describes this
	// run rather than the tool's whole vocabulary.
	seen := map[string]Finding{}
	for _, f := range findings {
		if _, ok := seen[f.RuleID]; !ok {
			seen[f.RuleID] = f
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		run.Tool.Driver.Rules = append(run.Tool.Driver.Rules, sarifRule{
			ID:               id,
			Name:             sarifRuleName(id),
			ShortDescription: sarifText{Text: seen[id].Title()},
			HelpURI:          findingsHelpURI,
		})
	}
	if run.Tool.Driver.Rules == nil {
		run.Tool.Driver.Rules = []sarifRule{}
	}

	run.Results = make([]sarifResult, 0, len(findings))
	for _, f := range findings {
		res := sarifResult{
			RuleID:  f.RuleID,
			Level:   sarifLevel(f.Severity),
			Message: sarifText{Text: f.Message},
		}
		// A finding with no line still gets a location: dropping it would
		// hide whole-config errors, which are the most serious kind.
		if f.Location.File != "" {
			phys := sarifPhysicalLocation{ArtifactLocation: sarifArtifactLocation{URI: toSARIFURI(f.Location.File)}}
			if f.Location.Line > 0 {
				phys.Region = &sarifRegion{StartLine: f.Location.Line, StartColumn: f.Location.Column}
			}
			res.Locations = []sarifLocation{{PhysicalLocation: phys}}
		}
		run.Results = append(run.Results, res)
	}
	log.Runs = []sarifRun{run}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(log)
}

// sarifRuleName is the id without the tool prefix, which is what a code
// scanning alert shows as the rule's name.
func sarifRuleName(id string) string {
	return strings.TrimPrefix(id, "convctl/")
}

// sarifLevel maps to SARIF's own vocabulary, which spells "note" the same
// way but has no "notice".
func sarifLevel(severity string) string {
	switch severity {
	case SeverityFindingError:
		return "error"
	case SeverityFindingWarning:
		return "warning"
	}
	return "note"
}

// toSARIFURI normalises separators: SARIF artifact URIs are slash-separated
// regardless of the platform that produced them, and a Windows-style path
// makes an alert unmatchable against the repository's files.
func toSARIFURI(p string) string {
	return strings.ReplaceAll(strings.TrimPrefix(p, "./"), "\\", "/")
}

// writeFindings renders findings in whichever CI-native format was asked
// for, to the command's stdout.
func writeFindings(cmd interface{ OutOrStdout() io.Writer }, output string, findings []Finding, title string) error {
	w := cmd.OutOrStdout()
	switch output {
	case "github":
		return WriteGitHubCommands(w, findings, title)
	case "sarif":
		return WriteSARIF(w, findings, Version)
	case "markdown":
		WriteFindingsMarkdown(w, findings, title)
		return nil
	}
	return fmt.Errorf("unsupported CI output format %q", output)
}

// writeFindingsTo is writeFindings against an arbitrary writer, for the
// paths that buffer the report before deciding where it goes.
func writeFindingsTo(w io.Writer, output string, findings []Finding, title string) error {
	switch output {
	case "github":
		return WriteGitHubCommands(w, findings, title)
	case "sarif":
		return WriteSARIF(w, findings, Version)
	case "markdown":
		WriteFindingsMarkdown(w, findings, title)
		return nil
	}
	return fmt.Errorf("unsupported CI output format %q", output)
}
