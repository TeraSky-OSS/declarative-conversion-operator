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
	"os"
	"runtime"
	"sync"
	"time"

	internalwebhook "github.com/terasky-oss/declarative-conversion-operator/internal/webhook"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// TestOptions configures RunTest.
type TestOptions struct {
	XRDPath      string
	CRDPath      string
	ConfigPath   string
	SamplesDir   string
	SkipIdentity bool
	// RestrictVersionPairs, if non-empty, limits testing to exactly these
	// "from:to" pairs (both directions still need listing explicitly).
	RestrictVersionPairs []string

	// Live, when set, fetches samples from a real cluster instead of
	// SamplesDir: every existing instance of the XRD's (or CRD's)
	// generated type, read at its hub/storage version. This is the
	// "pre-upgrade check" mode — test a config-to-be-applied against
	// every object that already exists, not just hand-written fixtures.
	Live        bool
	Kubeconfig  string
	KubeContext string

	// Concurrency is how many samples to test at once. Zero or negative
	// means runtime.GOMAXPROCS(0). Testing a cluster's entire population
	// of objects (Live) is embarrassingly parallel — every sample is
	// independent, and the compiled Router is read-only once built.
	Concurrency int
	// Quiet suppresses the progress line written to stderr.
	Quiet bool
}

// effectiveConcurrency clamps Concurrency to at least one worker, and to
// no more workers than there are samples to give them.
func (o TestOptions) effectiveConcurrency(samples int) int {
	n := o.Concurrency
	if n <= 0 {
		n = runtime.GOMAXPROCS(0)
	}
	if n > samples {
		n = samples
	}
	if n < 1 {
		n = 1
	}
	return n
}

// RunTest loads the config, its target XRD or CRD, and samples, validates
// the configuration exactly like the controller and admission webhook
// would, then tests every sample across every served-version pair
// (spoke_i -> hub -> spoke_j, including the hub itself as source or
// target), reporting timing, pass/loss/fail, rules exercised, and
// precisely which fields diverged where.
//
// Which of XRDPath/CRDPath applies is determined by the config's own
// kind, not by which field the caller happened to set.
func RunTest(opts TestOptions) (*Report, error) {
	kind, err := PeekConfigKind(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "CRDConversionConfig":
		if opts.CRDPath == "" {
			return nil, fmt.Errorf("%s is a CRDConversionConfig; pass its target schema with --crd, not --xrd", opts.ConfigPath)
		}
		return runTestCRD(opts)
	default: // "XRDConversionConfig"
		if opts.XRDPath == "" {
			return nil, fmt.Errorf("%s is an XRDConversionConfig; pass its target schema with --xrd, not --crd", opts.ConfigPath)
		}
		return runTestXRD(opts)
	}
}

func runTestXRD(opts TestOptions) (*Report, error) {
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
	return runTestCommon(opts, "XRD", xrdName(xrd), cfg.Name, cfg.Spec.HubVersion, samples, versions, report, router, start)
}

func runTestCRD(opts TestOptions) (*Report, error) {
	start := time.Now()

	crd, err := LoadCRD(opts.CRDPath)
	if err != nil {
		return nil, err
	}
	cfg, err := LoadCRDConfig(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	if err := internalwebhook.ValidateCRDStructure(cfg); err != nil {
		return nil, fmt.Errorf("configuration is structurally invalid: %w", err)
	}
	var samples []Sample
	if opts.Live {
		dyn, err := buildDynamicClient(KubeOptions{Kubeconfig: opts.Kubeconfig, Context: opts.KubeContext})
		if err != nil {
			return nil, err
		}
		samples, err = FetchLiveSamplesCRD(context.Background(), dyn, crd, cfg.Spec.HubVersion)
		if err != nil {
			return nil, fmt.Errorf("fetching live samples: %w", err)
		}
		if len(samples) == 0 {
			return nil, fmt.Errorf("no live objects of %s found at version %s", crdName(crd), cfg.Spec.HubVersion)
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

	report, versions, err := runAnalyzeCRD(crd, cfg)
	if err != nil {
		return nil, err
	}
	if report.HasErrors() {
		return nil, fmt.Errorf("configuration is invalid against the CRD schema, cannot test conversions:%s", summarizeSpokeErrors(report))
	}
	router, err := buildRouterCRD(cfg, report)
	if err != nil {
		return nil, err
	}
	return runTestCommon(opts, "CRD", crdName(crd), cfg.Name, cfg.Spec.HubVersion, samples, versions, report, router, start)
}

// runTestCommon is runTestXRD/runTestCRD's shared tail: exercising every
// sample across every served-version pair is entirely independent of
// whether the target is an XRD or a native CRD, once a Router and an
// AnalyzeReport already exist.
func runTestCommon(opts TestOptions, resourceKind, resourceName, configName, hubVersion string, samples []Sample, versions []engine.VersionSchema, report engine.AnalyzeReport, router *engine.Router, start time.Time) (*Report, error) {
	served := servedVersions(versions)
	targets := served
	if len(opts.RestrictVersionPairs) > 0 {
		targets = restrictTargets(served, opts.RestrictVersionPairs)
	}

	lossyPaths := buildLossyPathIndex(report)
	ruleUsage := map[string]int{}

	rep := &Report{}
	rep.Meta.ResourceKind = resourceKind
	rep.Meta.Resource = resourceName
	rep.Meta.Config = configName
	rep.Meta.HubVersion = hubVersion
	rep.Meta.ServedVersions = served
	rep.Meta.GeneratedAt = nowRFC3339()

	// Samples are tested by a worker pool, but the results are collected
	// by index and only appended to the Report afterwards, so the report
	// a run produces never depends on how many workers produced it.
	results := make([]SampleResult, len(samples))
	var (
		mu       sync.Mutex
		done     int
		next     = make(chan int)
		wg       sync.WaitGroup
		progress = !opts.Quiet && len(samples) > 1
	)
	go func() {
		defer close(next)
		for i := range samples {
			next <- i
		}
	}()
	for w := 0; w < opts.effectiveConcurrency(len(samples)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				sr, counts, usage := testOneSample(opts, router, hubVersion, lossyPaths, report, samples[i], targets)

				mu.Lock()
				results[i] = sr
				rep.Summary.PathsTested += counts.pathsTested
				rep.Summary.Pass += counts.pass
				rep.Summary.AcknowledgedLoss += counts.acknowledgedLoss
				rep.Summary.UnacknowledgedLoss += counts.unacknowledgedLoss
				rep.Summary.Errors += counts.errors
				for id, n := range usage {
					ruleUsage[id] += n
				}
				done++
				if progress {
					_, _ = fmt.Fprintf(os.Stderr, "\rtested %d/%d samples", done, len(samples))
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if progress {
		_, _ = fmt.Fprintln(os.Stderr)
	}

	rep.Samples = results
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

// sampleCounts is one sample's contribution to the report summary, tallied
// inside a worker so the shared Report is touched exactly once per sample
// rather than once per path.
type sampleCounts struct {
	pathsTested        int
	pass               int
	acknowledgedLoss   int
	unacknowledgedLoss int
	errors             int
}

// testOneSample runs one sample across every target version. Paths within
// a sample stay sequential: they're cheap next to the coordination cost,
// and keeping the unit of parallelism at the sample level is what makes
// deterministic result ordering trivial.
func testOneSample(opts TestOptions, router *engine.Router, hubVersion string, lossyPaths map[string]map[string]bool, report engine.AnalyzeReport, s Sample, targets []string) (SampleResult, sampleCounts, map[string]int) {
	sr := SampleResult{File: s.File, AssertedVersion: s.Version}
	var counts sampleCounts
	usage := map[string]int{}
	for _, target := range targets {
		if opts.SkipIdentity && target == s.Version {
			continue
		}
		pr := testOnePath(router, hubVersion, lossyPaths, report, s, target, usage)
		sr.Paths = append(sr.Paths, pr)
		counts.pathsTested++
		switch pr.Result {
		case "pass":
			counts.pass++
		case "loss":
			counts.acknowledgedLoss++
		case "fail":
			counts.unacknowledgedLoss++
		case "error":
			counts.errors++
		}
	}
	return sr, counts, usage
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
