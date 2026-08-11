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
	"io"
	"text/tabwriter"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// AnalyzeOutput is the schema-only (no samples) lossy/coverage analysis
// result — the same report the controller computes, without executing any
// conversions.
type AnalyzeOutput struct {
	ResourceKind string             `json:"resourceKind"` // "XRD" or "CRD"
	Resource     string             `json:"resource"`
	Config       string             `json:"config"`
	HubVersion   string             `json:"hubVersion"`
	Lossless     bool               `json:"lossless"`
	Spokes       []AnalyzeSpokeView `json:"spokes"`
}

type AnalyzeSpokeView struct {
	Version            string   `json:"version"`
	LosslessHubToSpoke bool     `json:"losslessHubToSpoke"`
	LosslessSpokeToHub bool     `json:"losslessSpokeToHub"`
	Errors             []string `json:"errors,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
	RulesEvaluated     int      `json:"rulesEvaluated"`
}

// RunAnalyze loads the XRD or CRD and its config and runs the engine's
// static analysis, with no samples involved. Which of xrdPath/crdPath
// applies is determined by the config's own kind.
func RunAnalyze(xrdPath, crdPath, configPath string) (*AnalyzeOutput, error) {
	kind, err := PeekConfigKind(configPath)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "CRDConversionConfig":
		if crdPath == "" {
			return nil, fmt.Errorf("%s is a CRDConversionConfig; pass its target schema with --crd, not --xrd", configPath)
		}
		return runAnalyzeCRDCmd(crdPath, configPath)
	default: // "XRDConversionConfig"
		if xrdPath == "" {
			return nil, fmt.Errorf("%s is an XRDConversionConfig; pass its target schema with --xrd, not --crd", configPath)
		}
		return runAnalyzeXRDCmd(xrdPath, configPath)
	}
}

func runAnalyzeXRDCmd(xrdPath, configPath string) (*AnalyzeOutput, error) {
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
	return buildAnalyzeOutput("XRD", xrdName(xrd), cfg.Name, cfg.Spec.HubVersion, report), nil
}

func runAnalyzeCRDCmd(crdPath, configPath string) (*AnalyzeOutput, error) {
	crd, err := LoadCRD(crdPath)
	if err != nil {
		return nil, err
	}
	cfg, err := LoadCRDConfig(configPath)
	if err != nil {
		return nil, err
	}
	report, _, err := runAnalyzeCRD(crd, cfg)
	if err != nil {
		return nil, err
	}
	return buildAnalyzeOutput("CRD", crdName(crd), cfg.Name, cfg.Spec.HubVersion, report), nil
}

func buildAnalyzeOutput(resourceKind, resourceName, configName, hubVersion string, report engine.AnalyzeReport) *AnalyzeOutput {
	out := &AnalyzeOutput{ResourceKind: resourceKind, Resource: resourceName, Config: configName, HubVersion: hubVersion, Lossless: report.OverallLossless()}
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
	return out
}

// WriteTable renders the analysis as a human-readable terminal report.
// Write errors to a terminal/CI log capture are not actionable, so they're
// deliberately ignored here rather than threaded back through a chain of
// callers that could do nothing useful with them either.
func (o *AnalyzeOutput) WriteTable(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%s Conversion Analysis\n%s: %s\tConfig: %s (hub: %s)\tOverall lossless: %v\n\n", o.ResourceKind, o.ResourceKind, o.Resource, o.Config, o.HubVersion, o.Lossless)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SPOKE\tHUB→SPOKE\tSPOKE→HUB\tRULES\tISSUES")
	for _, s := range o.Spokes {
		issues := len(s.Errors) + len(s.Warnings)
		_, _ = fmt.Fprintf(tw, "%s\t%v\t%v\t%d\t%d\n", s.Version, s.LosslessHubToSpoke, s.LosslessSpokeToHub, s.RulesEvaluated, issues)
	}
	_ = tw.Flush()

	for _, s := range o.Spokes {
		for _, e := range s.Errors {
			_, _ = fmt.Fprintf(w, "  [%s] ERROR: %s\n", s.Version, e)
		}
		for _, wmsg := range s.Warnings {
			_, _ = fmt.Fprintf(w, "  [%s] WARNING: %s\n", s.Version, wmsg)
		}
	}
}
