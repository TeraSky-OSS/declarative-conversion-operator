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

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// AnalyzeOutput is the schema-only (no samples) lossy/coverage analysis
// result — the same report the controller computes, without executing any
// conversions.
type AnalyzeOutput struct {
	XRD        string             `json:"xrd"`
	Config     string             `json:"config"`
	HubVersion string             `json:"hubVersion"`
	Lossless   bool               `json:"lossless"`
	Spokes     []AnalyzeSpokeView `json:"spokes"`
}

type AnalyzeSpokeView struct {
	Version            string   `json:"version"`
	LosslessHubToSpoke bool     `json:"losslessHubToSpoke"`
	LosslessSpokeToHub bool     `json:"losslessSpokeToHub"`
	Errors             []string `json:"errors,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
	RulesEvaluated     int      `json:"rulesEvaluated"`
}

// RunAnalyze loads the XRD and config and runs the engine's static
// analysis, with no samples involved.
func RunAnalyze(xrdPath, configPath string) (*AnalyzeOutput, error) {
	xrd, err := LoadXRD(xrdPath)
	if err != nil {
		return nil, err
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	report, _, err := runAnalyze(xrd, cfg)
	if err != nil {
		return nil, err
	}

	out := &AnalyzeOutput{XRD: xrdName(xrd), Config: cfg.Name, HubVersion: cfg.Spec.HubVersion, Lossless: report.OverallLossless()}
	for _, sr := range report.SpokeReports {
		v := AnalyzeSpokeView{Version: sr.Version, LosslessHubToSpoke: sr.Lossless.HubToSpoke, LosslessSpokeToHub: sr.Lossless.SpokeToHub, RulesEvaluated: len(sr.RuleResults)}
		for _, d := range sr.Errors {
			v.Errors = append(v.Errors, d.Message)
		}
		for _, d := range sr.Warnings {
			v.Warnings = append(v.Warnings, d.Message)
		}
		out.Spokes = append(out.Spokes, v)
	}
	return out, nil
}

// WriteTable renders the analysis as a human-readable terminal report.
func (o *AnalyzeOutput) WriteTable(w io.Writer) {
	fmt.Fprintf(w, "XRD Conversion Analysis\nXRD: %s\tConfig: %s (hub: %s)\tOverall lossless: %v\n\n", o.XRD, o.Config, o.HubVersion, o.Lossless)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SPOKE\tHUB→SPOKE\tSPOKE→HUB\tRULES\tISSUES")
	for _, s := range o.Spokes {
		issues := len(s.Errors) + len(s.Warnings)
		fmt.Fprintf(tw, "%s\t%v\t%v\t%d\t%d\n", s.Version, s.LosslessHubToSpoke, s.LosslessSpokeToHub, s.RulesEvaluated, issues)
	}
	tw.Flush()

	for _, s := range o.Spokes {
		for _, e := range s.Errors {
			fmt.Fprintf(w, "  [%s] ERROR: %s\n", s.Version, e)
		}
		for _, wmsg := range s.Warnings {
			fmt.Fprintf(w, "  [%s] WARNING: %s\n", s.Version, wmsg)
		}
	}
}
