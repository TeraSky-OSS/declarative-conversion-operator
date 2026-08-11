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
	"time"

	internalwebhook "github.com/vrabbi/declarative-conversion-operator/internal/webhook"
	"github.com/vrabbi/declarative-conversion-operator/pkg/engine"
)

// TestOptions configures RunTest.
type TestOptions struct {
	XRDPath      string
	ConfigPath   string
	SamplesDir   string
	SkipIdentity bool
	// RestrictVersionPairs, if non-empty, limits testing to exactly these
	// "from:to" pairs (both directions still need listing explicitly).
	RestrictVersionPairs []string

	// Live, when set, fetches samples from a real cluster instead of
	// SamplesDir: every existing instance of the XRD's generated type,
	// read at its hub/storage version. This is the "pre-upgrade check"
	// mode — test a config-to-be-applied against every object that
	// already exists, not just hand-written fixtures.
	Live        bool
	Kubeconfig  string
	KubeContext string
}

// RunTest loads the XRD/config/samples, validates the configuration
// exactly like the controller and admission webhook would, then tests
// every sample across every served-version pair (spoke_i -> hub -> spoke_j,
// including the hub itself as source or target), reporting timing, pass/
// loss/fail, rules exercised, and precisely which fields diverged where.
func RunTest(opts TestOptions) (*Report, error) {
	start := time.Now()

	xrd, err := LoadXRD(opts.XRDPath)
	if err != nil {
		return nil, err
	}
	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	if err := internalwebhook.ValidateStructure(cfg); err != nil {
		return nil, fmt.Errorf("configuration is structurally invalid: %w", err)
	}
	var samples []Sample
	if opts.Live {
		dyn, err := buildDynamicClient(KubeOptions{Kubeconfig: opts.Kubeconfig, Context: opts.KubeContext})
		if err != nil {
			return nil, err
		}
		samples, err = FetchLiveSamples(context.Background(), dyn, xrd, cfg.Spec.HubVersion)
		if err != nil {
			return nil, fmt.Errorf("fetching live samples: %w", err)
		}
		if len(samples) == 0 {
			return nil, fmt.Errorf("no live objects of %s found at version %s", xrdName(xrd), cfg.Spec.HubVersion)
		}
	} else {
		samples, err = LoadSamples(opts.SamplesDir)
		if err != nil {
			return nil, err
		}
		if len(samples) == 0 {
			return nil, fmt.Errorf("no sample files found under %s", opts.SamplesDir)
		}
	}

	report, versions, err := runAnalyze(xrd, cfg)
	if err != nil {
		return nil, err
	}
	if report.HasErrors() {
		return nil, fmt.Errorf("configuration is invalid against the XRD schema, cannot test conversions:%s", summarizeSpokeErrors(report))
	}
	router, err := buildRouter(cfg, report)
	if err != nil {
		return nil, err
	}

	served := servedVersions(versions)
	targets := served
	if len(opts.RestrictVersionPairs) > 0 {
		targets = restrictTargets(served, opts.RestrictVersionPairs)
	}

	lossyPaths := buildLossyPathIndex(report)
	ruleUsage := map[string]int{}

	rep := &Report{}
	rep.Meta.XRD = xrdName(xrd)
	rep.Meta.Config = cfg.Name
	rep.Meta.HubVersion = cfg.Spec.HubVersion
	rep.Meta.ServedVersions = served
	rep.Meta.GeneratedAt = nowRFC3339()

	for _, s := range samples {
		sr := SampleResult{File: s.File, AssertedVersion: s.Version}
		for _, target := range targets {
			if opts.SkipIdentity && target == s.Version {
				continue
			}
			pr := testOnePath(router, cfg.Spec.HubVersion, lossyPaths, report, s, target, ruleUsage)
			sr.Paths = append(sr.Paths, pr)
			rep.Summary.PathsTested++
			switch pr.Result {
			case "pass":
				rep.Summary.Pass++
			case "loss":
				rep.Summary.AcknowledgedLoss++
			case "fail":
				rep.Summary.UnacknowledgedLoss++
			case "error":
				rep.Summary.Errors++
			}
		}
		rep.Samples = append(rep.Samples, sr)
	}
	rep.Summary.Samples = len(samples)

	for _, sr := range report.SpokeReports {
		for _, rr := range sr.RuleResults {
			id := ruleID(sr.Version, rr)
			rep.RuleCoverage = append(rep.RuleCoverage, RuleCoverage{RuleID: id, MatchedSamples: ruleUsage[id]})
		}
	}

	rep.Meta.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
	return rep, nil
}

func ruleID(spokeVersion string, rr engine.RuleResult) string {
	return fmt.Sprintf("%s:rule[%d]:%s", spokeVersion, rr.Index, rr.Strategy)
}

func restrictTargets(served []string, pairs []string) []string {
	set := map[string]bool{}
	for _, p := range pairs {
		for _, v := range splitPair(p) {
			set[v] = true
		}
	}
	var out []string
	for _, v := range served {
		if set[v] {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return served
	}
	return out
}

func splitPair(p string) []string {
	for i := 0; i < len(p); i++ {
		if p[i] == ':' {
			return []string{p[:i], p[i+1:]}
		}
	}
	return []string{p}
}

// buildLossyPathIndex maps each spoke version to the set of dotted paths
// that at least one rule declared lossy (in either direction) for that
// spoke — used to classify a round-trip diff as "acknowledged" vs. not.
func buildLossyPathIndex(report engine.AnalyzeReport) map[string]map[string]bool {
	idx := map[string]map[string]bool{}
	for _, sr := range report.SpokeReports {
		set := map[string]bool{}
		for _, rr := range sr.RuleResults {
			if rr.Lossless.HubToSpoke && rr.Lossless.SpokeToHub {
				continue
			}
			for _, p := range rr.HubPaths {
				set[p] = true
			}
			for _, p := range rr.SpokePaths {
				set[p] = true
			}
		}
		idx[sr.Version] = set
	}
	return idx
}

func touchedSpokes(from, to, hub string) []string {
	var out []string
	if from != hub {
		out = append(out, from)
	}
	if to != hub && to != from {
		out = append(out, to)
	}
	return out
}

func testOnePath(router *engine.Router, hub string, lossyPaths map[string]map[string]bool, report engine.AnalyzeReport, s Sample, target string, ruleUsage map[string]int) PathResult {
	start := time.Now()
	pr := PathResult{From: s.Version, To: target}

	if s.Version == target {
		pr.Result = "pass"
		pr.FieldsConverted = countLeaves(s.Object)
		pr.RulesMatched = []string{"(identity)"}
		pr.TimingMicros = time.Since(start).Microseconds()
		return pr
	}

	forward, err := router.Convert(s.Object, s.Version, target)
	if err != nil {
		pr.Result = "error"
		pr.Issues = append(pr.Issues, Issue{Field: "(conversion)", From: s.Version, To: target, Type: "error", Detail: err.Error(), Sample: s.File})
		pr.TimingMicros = time.Since(start).Microseconds()
		return pr
	}
	back, err := router.Convert(forward, target, s.Version)
	if err != nil {
		pr.Result = "error"
		pr.Issues = append(pr.Issues, Issue{Field: "(round-trip conversion)", From: target, To: s.Version, Type: "error", Detail: err.Error(), Sample: s.File})
		pr.TimingMicros = time.Since(start).Microseconds()
		return pr
	}

	spokes := touchedSpokes(s.Version, target, hub)
	diffs := diffLeaves(s.Object, back)
	unacknowledged := 0
	for _, d := range diffs {
		ack := false
		for _, spoke := range spokes {
			if pathMatchesAny(d, lossyPaths[spoke]) {
				ack = true
				break
			}
		}
		if ack {
			pr.Issues = append(pr.Issues, Issue{Field: d, From: s.Version, To: target, Type: "acknowledged-loss", Detail: "round-trip mismatch; declared lossy by a matching rule", Sample: s.File})
		} else {
			unacknowledged++
			pr.Issues = append(pr.Issues, Issue{Field: d, From: s.Version, To: target, Type: "unacknowledged-loss", Detail: "round-trip mismatch; no rule declares this field lossy", Sample: s.File})
		}
	}

	switch {
	case unacknowledged > 0:
		pr.Result = "fail"
	case len(diffs) > 0:
		pr.Result = "loss"
	default:
		pr.Result = "pass"
	}
	pr.FieldsConverted = countLeaves(forward)
	pr.TimingMicros = time.Since(start).Microseconds()

	for _, sr := range report.SpokeReports {
		matches := false
		for _, spoke := range spokes {
			if sr.Version == spoke {
				matches = true
			}
		}
		if !matches {
			continue
		}
		for _, rr := range sr.RuleResults {
			id := ruleID(sr.Version, rr)
			ruleUsage[id]++
			pr.RulesMatched = append(pr.RulesMatched, id)
		}
	}
	return pr
}
