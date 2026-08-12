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
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// DiffOptions configures RunDiff.
type DiffOptions struct {
	// ConfigPaths holds exactly two config files to compare, or exactly
	// one when Live is set (the local side of a local-vs-cluster diff).
	ConfigPaths []string
	XRDPath     string
	CRDPath     string

	// Live compares ConfigPaths[0] against whatever the cluster currently
	// has: the live XRD/CRD schema, and the ConversionConfig of the same
	// name if one exists.
	Live        bool
	Kubeconfig  string
	KubeContext string
}

// DiffOutput is the delta between two conversion configs analyzed against
// the same schema — what changes about coverage, which rules claim which
// paths, whether each direction is still lossless, and which diagnostics
// appear or disappear.
type DiffOutput struct {
	ResourceKind string `json:"resourceKind"` // "XRD" or "CRD"
	Resource     string `json:"resource"`
	// From and To label the two sides being compared, in that order: a
	// file path, or "cluster:<name>" for the live side.
	From      string `json:"from"`
	To        string `json:"to"`
	HasDeltas bool   `json:"hasDeltas"`
	// HubVersionChange is set only when the two sides disagree about which
	// version is the hub — a change that reshapes every spoke's mapping.
	HubVersionChange *StringChange `json:"hubVersionChange,omitempty"`
	SpokesAdded      []string      `json:"spokesAdded,omitempty"`
	SpokesRemoved    []string      `json:"spokesRemoved,omitempty"`
	Spokes           []SpokeDiff   `json:"spokes,omitempty"`
}

// StringChange is a single scalar that differs between the two sides.
type StringChange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RuleClaim is the set of hub and spoke paths one rule claims, identified
// by strategy rather than by index — reordering a config's rules is not a
// semantic change, and shouldn't show up as one.
type RuleClaim struct {
	Strategy   string   `json:"strategy"`
	HubPaths   []string `json:"hubPaths,omitempty"`
	SpokePaths []string `json:"spokePaths,omitempty"`
}

// LosslessChange records one direction of one spoke flipping between
// lossless and lossy.
type LosslessChange struct {
	Direction string `json:"direction"` // "hubToSpoke" | "spokeToHub"
	From      bool   `json:"from"`
	To        bool   `json:"to"`
}

// SpokeDiff is everything that changed for one spoke version present on
// both sides.
type SpokeDiff struct {
	Version               string           `json:"version"`
	UncoveredHubAdded     []string         `json:"uncoveredHubAdded,omitempty"`
	UncoveredHubRemoved   []string         `json:"uncoveredHubRemoved,omitempty"`
	UncoveredSpokeAdded   []string         `json:"uncoveredSpokeAdded,omitempty"`
	UncoveredSpokeRemoved []string         `json:"uncoveredSpokeRemoved,omitempty"`
	RuleClaimsAdded       []RuleClaim      `json:"ruleClaimsAdded,omitempty"`
	RuleClaimsRemoved     []RuleClaim      `json:"ruleClaimsRemoved,omitempty"`
	LosslessChanges       []LosslessChange `json:"losslessChanges,omitempty"`
	ErrorsAdded           []string         `json:"errorsAdded,omitempty"`
	ErrorsRemoved         []string         `json:"errorsRemoved,omitempty"`
	WarningsAdded         []string         `json:"warningsAdded,omitempty"`
	WarningsRemoved       []string         `json:"warningsRemoved,omitempty"`
}

func (s SpokeDiff) hasDeltas() bool {
	return len(s.UncoveredHubAdded)+len(s.UncoveredHubRemoved)+
		len(s.UncoveredSpokeAdded)+len(s.UncoveredSpokeRemoved)+
		len(s.RuleClaimsAdded)+len(s.RuleClaimsRemoved)+
		len(s.LosslessChanges)+
		len(s.ErrorsAdded)+len(s.ErrorsRemoved)+
		len(s.WarningsAdded)+len(s.WarningsRemoved) > 0
}

// RunDiff analyzes both sides against the same schema and reports what
// changed. Which of XRDPath/CRDPath applies is determined by the config's
// own kind, as everywhere else.
func RunDiff(opts DiffOptions) (*DiffOutput, error) {
	if opts.Live {
		if len(opts.ConfigPaths) != 1 {
			return nil, fmt.Errorf("--live compares exactly one local --config against the cluster; got %d", len(opts.ConfigPaths))
		}
	} else if len(opts.ConfigPaths) != 2 {
		return nil, fmt.Errorf("diff needs exactly two --config paths (or one with --live); got %d", len(opts.ConfigPaths))
	}

	kind, err := PeekConfigKind(opts.ConfigPaths[0])
	if err != nil {
		return nil, err
	}
	if opts.Live {
		return runDiffLive(kind, opts)
	}
	return runDiffFiles(kind, opts)
}

func runDiffFiles(kind string, opts DiffOptions) (*DiffOutput, error) {
	otherKind, err := PeekConfigKind(opts.ConfigPaths[1])
	if err != nil {
		return nil, err
	}
	if otherKind != kind {
		return nil, fmt.Errorf("cannot diff a %s against a %s", kind, otherKind)
	}

	if kind == "CRDConversionConfig" {
		if opts.CRDPath == "" {
			return nil, fmt.Errorf("%s is a CRDConversionConfig; pass its target schema with --crd, not --xrd", opts.ConfigPaths[0])
		}
		crd, err := LoadCRD(opts.CRDPath)
		if err != nil {
			return nil, err
		}
		from, err := LoadCRDConfig(opts.ConfigPaths[0])
		if err != nil {
			return nil, err
		}
		to, err := LoadCRDConfig(opts.ConfigPaths[1])
		if err != nil {
			return nil, err
		}
		return diffCRDConfigs(crd, from, to, opts.ConfigPaths[0], opts.ConfigPaths[1])
	}

	if opts.XRDPath == "" {
		return nil, fmt.Errorf("%s is an XRDConversionConfig; pass its target schema with --xrd, not --crd", opts.ConfigPaths[0])
	}
	xrd, err := LoadXRD(opts.XRDPath)
	if err != nil {
		return nil, err
	}
	from, err := LoadConfig(opts.ConfigPaths[0])
	if err != nil {
		return nil, err
	}
	to, err := LoadConfig(opts.ConfigPaths[1])
	if err != nil {
		return nil, err
	}
	return diffXRDConfigs(xrd, from, to, opts.ConfigPaths[0], opts.ConfigPaths[1])
}

// runDiffLive compares the cluster's current state (the "from" side)
// against the local config (the "to" side), which is the direction that
// makes a diff read like "what applying this would change".
func runDiffLive(kind string, opts DiffOptions) (*DiffOutput, error) {
	dyn, err := buildDynamicClient(KubeOptions{Kubeconfig: opts.Kubeconfig, Context: opts.KubeContext})
	if err != nil {
		return nil, err
	}
	ctx := context.Background()

	if kind == "CRDConversionConfig" {
		local, err := LoadCRDConfig(opts.ConfigPaths[0])
		if err != nil {
			return nil, err
		}
		crd, err := FetchLiveCRD(ctx, dyn, local.Spec.TargetCRD.Name)
		if err != nil {
			return nil, err
		}
		live, err := FetchLiveCRDConversionConfig(ctx, dyn, local.Name)
		if err != nil {
			return nil, err
		}
		fromLabel := "cluster:" + local.Name
		if live == nil {
			live = emptyRulesCRDConfig(local)
			fromLabel = "cluster:(no CRDConversionConfig " + local.Name + ")"
		}
		return diffCRDConfigs(crd, live, local, fromLabel, opts.ConfigPaths[0])
	}

	local, err := LoadConfig(opts.ConfigPaths[0])
	if err != nil {
		return nil, err
	}
	xrd, err := FetchLiveXRD(ctx, dyn, local.Spec.TargetXRD.Name)
	if err != nil {
		return nil, err
	}
	live, err := FetchLiveXRDConversionConfig(ctx, dyn, local.Name)
	if err != nil {
		return nil, err
	}
	fromLabel := "cluster:" + local.Name
	if live == nil {
		live = emptyRulesXRDConfig(local)
		fromLabel = "cluster:(no XRDConversionConfig " + local.Name + ")"
	}
	return diffXRDConfigs(xrd, live, local, fromLabel, opts.ConfigPaths[0])
}

func diffXRDConfigs(xrd *unstructured.Unstructured, from, to *teraskyv1alpha1.XRDConversionConfig, fromLabel, toLabel string) (*DiffOutput, error) {
	fromReport, _, err := runAnalyze(xrd, from)
	if err != nil {
		return nil, fmt.Errorf("analyzing %s: %w", fromLabel, err)
	}
	toReport, _, err := runAnalyze(xrd, to)
	if err != nil {
		return nil, fmt.Errorf("analyzing %s: %w", toLabel, err)
	}
	return buildDiffOutput("XRD", xrdName(xrd), fromLabel, toLabel, from.Spec.HubVersion, to.Spec.HubVersion, fromReport, toReport), nil
}

func diffCRDConfigs(crd *extv1.CustomResourceDefinition, from, to *teraskyv1alpha1.CRDConversionConfig, fromLabel, toLabel string) (*DiffOutput, error) {
	fromReport, _, err := runAnalyzeCRD(crd, from)
	if err != nil {
		return nil, fmt.Errorf("analyzing %s: %w", fromLabel, err)
	}
	toReport, _, err := runAnalyzeCRD(crd, to)
	if err != nil {
		return nil, fmt.Errorf("analyzing %s: %w", toLabel, err)
	}
	return buildDiffOutput("CRD", crdName(crd), fromLabel, toLabel, from.Spec.HubVersion, to.Spec.HubVersion, fromReport, toReport), nil
}

// emptyRulesXRDConfig mirrors cfg's spokes with no rules at all, standing
// in for the cluster side when no config has been applied yet: diffing
// against "every field unclaimed" is far more useful than refusing to run.
func emptyRulesXRDConfig(cfg *teraskyv1alpha1.XRDConversionConfig) *teraskyv1alpha1.XRDConversionConfig {
	out := &teraskyv1alpha1.XRDConversionConfig{}
	out.Name = cfg.Name
	out.Spec.TargetXRD = cfg.Spec.TargetXRD
	out.Spec.HubVersion = cfg.Spec.HubVersion
	out.Spec.UnmappedFieldPolicy = cfg.Spec.UnmappedFieldPolicy
	for _, s := range cfg.Spec.Spokes {
		out.Spec.Spokes = append(out.Spec.Spokes, teraskyv1alpha1.SpokeVersionRules{Version: s.Version})
	}
	return out
}

func emptyRulesCRDConfig(cfg *teraskyv1alpha1.CRDConversionConfig) *teraskyv1alpha1.CRDConversionConfig {
	out := &teraskyv1alpha1.CRDConversionConfig{}
	out.Name = cfg.Name
	out.Spec.TargetCRD = cfg.Spec.TargetCRD
	out.Spec.HubVersion = cfg.Spec.HubVersion
	out.Spec.UnmappedFieldPolicy = cfg.Spec.UnmappedFieldPolicy
	for _, s := range cfg.Spec.Spokes {
		out.Spec.Spokes = append(out.Spec.Spokes, teraskyv1alpha1.SpokeVersionRules{Version: s.Version})
	}
	return out
}

func buildDiffOutput(resourceKind, resourceName, fromLabel, toLabel, fromHub, toHub string, from, to engine.AnalyzeReport) *DiffOutput {
	out := &DiffOutput{ResourceKind: resourceKind, Resource: resourceName, From: fromLabel, To: toLabel}
	if fromHub != toHub {
		out.HubVersionChange = &StringChange{From: fromHub, To: toHub}
	}

	fromSpokes := spokeReportsByVersion(from)
	toSpokes := spokeReportsByVersion(to)
	for _, v := range sortedKeys(toSpokes) {
		if _, ok := fromSpokes[v]; !ok {
			out.SpokesAdded = append(out.SpokesAdded, v)
		}
	}
	for _, v := range sortedKeys(fromSpokes) {
		if _, ok := toSpokes[v]; !ok {
			out.SpokesRemoved = append(out.SpokesRemoved, v)
		}
	}

	for _, v := range sortedKeys(toSpokes) {
		f, ok := fromSpokes[v]
		if !ok {
			continue
		}
		if sd := diffSpokeReports(v, f, toSpokes[v]); sd.hasDeltas() {
			out.Spokes = append(out.Spokes, sd)
		}
	}

	out.HasDeltas = out.HubVersionChange != nil || len(out.SpokesAdded) > 0 || len(out.SpokesRemoved) > 0 || len(out.Spokes) > 0
	return out
}

func diffSpokeReports(version string, from, to engine.SpokeReport) SpokeDiff {
	sd := SpokeDiff{Version: version}
	sd.UncoveredHubAdded, sd.UncoveredHubRemoved = stringSetDiff(from.Uncovered.UncoveredHub, to.Uncovered.UncoveredHub)
	sd.UncoveredSpokeAdded, sd.UncoveredSpokeRemoved = stringSetDiff(from.Uncovered.UncoveredSpoke, to.Uncovered.UncoveredSpoke)
	sd.RuleClaimsAdded, sd.RuleClaimsRemoved = claimSetDiff(ruleClaims(from), ruleClaims(to))
	if from.Lossless.HubToSpoke != to.Lossless.HubToSpoke {
		sd.LosslessChanges = append(sd.LosslessChanges, LosslessChange{Direction: "hubToSpoke", From: from.Lossless.HubToSpoke, To: to.Lossless.HubToSpoke})
	}
	if from.Lossless.SpokeToHub != to.Lossless.SpokeToHub {
		sd.LosslessChanges = append(sd.LosslessChanges, LosslessChange{Direction: "spokeToHub", From: from.Lossless.SpokeToHub, To: to.Lossless.SpokeToHub})
	}
	sd.ErrorsAdded, sd.ErrorsRemoved = stringSetDiff(diagMessages(from.Errors), diagMessages(to.Errors))
	sd.WarningsAdded, sd.WarningsRemoved = stringSetDiff(diagMessages(from.Warnings), diagMessages(to.Warnings))
	return sd
}

func spokeReportsByVersion(r engine.AnalyzeReport) map[string]engine.SpokeReport {
	out := map[string]engine.SpokeReport{}
	for _, sr := range r.SpokeReports {
		out[sr.Version] = sr
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func diagMessages(ds []engine.Diagnostic) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Message)
	}
	return out
}

// stringSetDiff reports which entries appear only in to (added) and only in
// from (removed), as sorted sets — duplicates carry no meaning for any of
// the path/message lists this compares.
func stringSetDiff(from, to []string) (added, removed []string) {
	fromSet := map[string]bool{}
	for _, s := range from {
		fromSet[s] = true
	}
	toSet := map[string]bool{}
	for _, s := range to {
		toSet[s] = true
	}
	for s := range toSet {
		if !fromSet[s] {
			added = append(added, s)
		}
	}
	for s := range fromSet {
		if !toSet[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func ruleClaims(sr engine.SpokeReport) []RuleClaim {
	out := make([]RuleClaim, 0, len(sr.RuleResults))
	for _, rr := range sr.RuleResults {
		out = append(out, RuleClaim{Strategy: string(rr.Strategy), HubPaths: rr.HubPaths, SpokePaths: rr.SpokePaths})
	}
	return out
}

// claimSetDiff is a multiset diff: two rules with the same strategy
// claiming the same paths are interchangeable, but declaring the same rule
// twice and declaring it once are genuinely different configs.
func claimSetDiff(from, to []RuleClaim) (added, removed []RuleClaim) {
	counts := map[string]int{}
	byKey := map[string]RuleClaim{}
	for _, c := range from {
		k := claimKey(c)
		counts[k]--
		byKey[k] = c
	}
	for _, c := range to {
		k := claimKey(c)
		counts[k]++
		byKey[k] = c
	}
	for _, k := range sortedKeys(counts) {
		for i := 0; i < counts[k]; i++ {
			added = append(added, byKey[k])
		}
		for i := 0; i > counts[k]; i-- {
			removed = append(removed, byKey[k])
		}
	}
	return added, removed
}

func claimKey(c RuleClaim) string {
	return fmt.Sprintf("%s|%s|%s", c.Strategy, strings.Join(c.HubPaths, ","), strings.Join(c.SpokePaths, ","))
}

// WriteTable renders the diff for a terminal. Write errors to a
// terminal/CI log capture are not actionable, so they're deliberately
// ignored here, as elsewhere in this package.
func (d *DiffOutput) WriteTable(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%s Conversion Diff\n%s: %s\n%s → %s\n\n", d.ResourceKind, d.ResourceKind, d.Resource, d.From, d.To)
	if !d.HasDeltas {
		_, _ = fmt.Fprintln(w, "no differences")
		return
	}
	if d.HubVersionChange != nil {
		_, _ = fmt.Fprintf(w, "hub version: %s → %s\n", d.HubVersionChange.From, d.HubVersionChange.To)
	}
	for _, v := range d.SpokesAdded {
		_, _ = fmt.Fprintf(w, "+ spoke %s\n", v)
	}
	for _, v := range d.SpokesRemoved {
		_, _ = fmt.Fprintf(w, "- spoke %s\n", v)
	}
	for _, s := range d.Spokes {
		_, _ = fmt.Fprintf(w, "\n[spoke %s]\n", s.Version)
		for _, c := range s.LosslessChanges {
			_, _ = fmt.Fprintf(w, "  lossless %s: %v → %v\n", c.Direction, c.From, c.To)
		}
		writePathDeltas(w, "uncovered hub", s.UncoveredHubAdded, s.UncoveredHubRemoved)
		writePathDeltas(w, "uncovered spoke", s.UncoveredSpokeAdded, s.UncoveredSpokeRemoved)
		for _, c := range s.RuleClaimsAdded {
			_, _ = fmt.Fprintf(w, "  + rule %s\n", claimKey(c))
		}
		for _, c := range s.RuleClaimsRemoved {
			_, _ = fmt.Fprintf(w, "  - rule %s\n", claimKey(c))
		}
		writePathDeltas(w, "ERROR", s.ErrorsAdded, s.ErrorsRemoved)
		writePathDeltas(w, "WARNING", s.WarningsAdded, s.WarningsRemoved)
	}
}

func writePathDeltas(w io.Writer, label string, added, removed []string) {
	for _, p := range added {
		_, _ = fmt.Fprintf(w, "  + %s %s\n", label, p)
	}
	for _, p := range removed {
		_, _ = fmt.Fprintf(w, "  - %s %s\n", label, p)
	}
}
