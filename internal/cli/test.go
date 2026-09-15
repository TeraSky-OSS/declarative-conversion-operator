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
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	internalwebhook "github.com/terasky-oss/declarative-conversion-operator/internal/webhook"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// TestOptions configures RunTest.
type TestOptions struct {
	XRDPath string
	// PackagePath is a Crossplane package to read the XRD from instead of
	// a file: the unit of API change for a platform shipped as a
	// Configuration is a package version, not a commit and not the live
	// cluster.
	PackagePath string
	// PackageTarget selects one XRD when the package ships several.
	PackageTarget string
	CRDPath       string
	ConfigPath    string
	SamplesDir    string
	SkipIdentity  bool
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
	// Contexts, when set, runs --live once per kubeconfig context and
	// aggregates the reports. Mutually exclusive with KubeContext.
	Contexts []string
	// KubeconfigDir, when set, treats each regular file in the directory
	// as a kubeconfig. Mutually exclusive with Kubeconfig.
	KubeconfigDir string

	// Concurrency is how many samples to test at once. Zero or negative
	// means runtime.GOMAXPROCS(0). Testing a cluster's entire population
	// of objects (Live) is embarrassingly parallel — every sample is
	// independent, and the compiled Router is read-only once built.
	Concurrency int
	// Quiet suppresses the progress line written to stderr.
	Quiet bool
	// Sampling bounds a --live run on a cluster whose population does not
	// fit in memory. Zero-valued means every object, as before.
	Sampling SamplingOptions
	// Namespace narrows a --live run to one namespace. Only the namespaced
	// object class is affected: on a claim-offering XRD the composites are
	// cluster-scoped, so narrowing them is not a thing that exists.
	Namespace string

	// ValidateOutput additionally validates every converted object against
	// the destination version's own schema, using the apiextensions
	// structural-schema validator. Without it, a conversion that drops a
	// required field or produces an out-of-enum value is reported as PASS
	// and then rejected by the apiserver in production, with an error that
	// names the object rather than the rule that produced it.
	//
	// Off by default: turning it on would make existing green pipelines
	// red on first upgrade. The default flips in a later release.
	ValidateOutput bool

	// Fuzz generates N schema-valid objects from the hub version's own
	// schema and runs them through every conversion path, alongside (or
	// instead of) SamplesDir.
	//
	// Fixtures test the cases the author thought of; they reliably miss the
	// empty array, the absent optional, the maxLength boundary and the enum
	// value nobody uses, which is where conversion rules break. Generation
	// is biased toward exactly those.
	Fuzz int
	// FuzzSeed makes a run reproducible. Zero means "pick one and print
	// it", so a CI failure is replayable locally.
	FuzzSeed int64
	// RecordFailuresDir writes objects that failed conversion as ordinary
	// sample files, so a discovered case can be promoted into the permanent
	// fixture corpus. That promotion path is what makes fuzzing pay off
	// over time rather than being a one-off.
	RecordFailuresDir string

	// RecordDir writes the conversion result for every sample on every
	// path into a golden corpus, and GoldenDir replays one. A committed
	// corpus turns the next rule change into a reviewable diff: "this
	// changes the output for these three objects, in these fields", which
	// a YAML diff of the rules plus a green check cannot show.
	//
	// Mutually exclusive: recording while comparing would compare a corpus
	// against itself.
	RecordDir string
	GoldenDir string

	// VerifyPropagation additionally checks, against the same cluster,
	// that every CRD Crossplane generates from the target XRD actually
	// carries the conversion webhook the XRD points at. Samples passing
	// through the engine says the rules are right; this says the cluster
	// will use them. Requires Live, and is XRD-only — a
	// CRDConversionConfig's target *is* the CRD, so there is nothing to
	// propagate.
	VerifyPropagation bool
}

// effectiveSeed returns the seed a fuzz run should use, choosing one when
// the caller did not. The chosen seed is reported so a CI failure is
// replayable locally — a fuzz failure nobody can reproduce is noise.
func (o TestOptions) effectiveSeed() int64 {
	if o.FuzzSeed != 0 {
		return o.FuzzSeed
	}
	return time.Now().UnixNano()
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
// would, then tests every sample across every configured-version pair
// (spoke_i -> hub -> spoke_j, including the hub itself as source or
// target), reporting timing, pass/loss/fail, rules exercised, and
// precisely which fields diverged where.
//
// Targets are the hub plus every spoke the config compiled a plan for,
// intersected with served versions. A served version the config does not
// claim is not a conversion path — dropping a spoke before setting
// served:false is the required order, and test must still run in that
// window. --version-pair further restricts that set.
//
// Which of XRDPath/CRDPath applies is determined by the config's own
// kind, not by which field the caller happened to set.
func RunTest(opts TestOptions) (*Report, error) {
	// Resolve the seed once, here, so the objects that were generated and
	// the seed the report prints for reproducing them cannot disagree.
	if opts.Fuzz > 0 && opts.FuzzSeed == 0 {
		opts.FuzzSeed = time.Now().UnixNano()
	}
	// Validated here rather than only in the cobra command: RunTest is
	// exported, and a caller that bypasses the flag parsing would otherwise
	// reach the sampler with options it rejects — a negative cap disables
	// the bound entirely and paginates the whole population into memory,
	// and an unknown strategy keeps nothing and then fails the run for
	// having no samples.
	if err := ValidateSamplingOptions(opts.Sampling); err != nil {
		return nil, err
	}
	kind, err := PeekConfigKind(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	// Enforce VerifyPropagation's documented constraints here rather than
	// only in the cobra command: TestOptions is exported, and silently
	// skipping a check the caller asked for is the failure mode the check
	// exists to prevent.
	if opts.VerifyPropagation {
		if !opts.Live {
			return nil, errors.New("--verify-propagation requires --live: it reads the target's generated CRDs from a cluster")
		}
		if kind == "CRDConversionConfig" {
			return nil, errors.New("--verify-propagation applies only to an XRDConversionConfig: a CRDConversionConfig's target IS the CRD, so there is no generated CRD for the conversion webhook to propagate into")
		}
	}

	switch kind {
	case "CRDConversionConfig":
		if opts.CRDPath == "" {
			return nil, fmt.Errorf("%s is a CRDConversionConfig; pass its target schema with --crd, not --xrd", opts.ConfigPath)
		}
		return runTestCRD(opts)
	default: // "XRDConversionConfig"
		if opts.XRDPath == "" && opts.PackagePath == "" {
			return nil, fmt.Errorf("%s is an XRDConversionConfig; pass its target schema with --xrd or --package, not --crd", opts.ConfigPath)
		}
		return runTestXRD(opts)
	}
}

func runTestXRD(opts TestOptions) (*Report, error) {
	start := time.Now()

	xrd, err := XRDFromSource(opts.XRDPath, opts.PackagePath, opts.PackageTarget)
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
	var sampling *SamplingReport
	// Which fields Crossplane injects, and where they sit, is decided by
	// the XRD's scope — so report it alongside the results rather than
	// making an author infer it, and say so when the manifest does not
	// settle the question.
	scope := xrdadapter.ResolveScope(xrd)
	var (
		propagation *PropagationReport
		perr        error
	)
	if opts.Live {
		dyn, err := buildDynamicClient(KubeOptions{Kubeconfig: opts.Kubeconfig, Context: opts.KubeContext})
		if err != nil {
			return nil, err
		}
		if opts.VerifyPropagation {
			propagation, perr = VerifyPropagation(context.Background(), dyn, xrdName(xrd))
			if perr != nil {
				return nil, fmt.Errorf("verifying conversion propagation: %w", perr)
			}
		}
		samples, sampling, err = FetchLiveSamplesSampled(context.Background(), dyn, xrd, cfg.Spec.HubVersion, opts.Sampling, opts.Namespace)
		if err != nil {
			return nil, fmt.Errorf("fetching live samples: %w", err)
		}
		if len(samples) == 0 {
			return nil, fmt.Errorf("no live objects of %s found at version %s", xrdName(xrd), cfg.Spec.HubVersion)
		}
	} else if opts.SamplesDir != "" {
		samples, err = LoadSamples(opts.SamplesDir)
		if err != nil {
			return nil, err
		}
		group, kind, gvkErr := xrdGroupKind(xrd)
		if gvkErr != nil {
			return nil, gvkErr
		}
		samples, err = filterSamplesByGVK(samples, group, kind)
		if err != nil {
			return nil, err
		}
		if len(samples) == 0 {
			return nil, fmt.Errorf("no %s.%s objects under %s", kind, group, opts.SamplesDir)
		}
	}

	report, versions, err := runAnalyze(xrd, cfg)
	if err != nil {
		return nil, err
	}
	if report.HasErrors() {
		return nil, fmt.Errorf("configuration is invalid against the XRD schema, cannot test conversions:%s", summarizeSpokeErrors(report))
	}
	// Generated after analysis, because generation needs the hub schema the
	// analysis just read.
	if opts.Fuzz > 0 {
		group, kind, gvkErr := xrdGroupKind(xrd)
		if gvkErr != nil {
			return nil, gvkErr
		}
		fuzzed, ferr := generateFuzzSamples(versions, cfg.Spec.HubVersion, group, kind, opts.Fuzz, opts.effectiveSeed(), shapesFromRules(report))
		if ferr != nil {
			return nil, ferr
		}
		samples = append(samples, fuzzed...)
	}
	if len(samples) == 0 {
		return nil, errors.New("nothing to test: pass --samples, --live, or --fuzz")
	}
	router, err := buildRouter(cfg, report)
	if err != nil {
		return nil, err
	}
	// The injected set is what --validate-output must ignore: Crossplane
	// merges these into the generated CRD, so they are present on a real
	// object and absent from the XRD's authored schema — the only schema
	// this tool has.
	rep, err := runTestCommon(opts, "XRD", xrdName(xrd), cfg.Name, cfg.Spec.HubVersion, samples, versions, report, router, xrdadapter.New(xrd).PlatformInjectedPaths().Paths, start)
	if err != nil {
		return nil, err
	}
	rep.Meta.Sampling = sampling
	rep.Meta.Scope = scopeView(scope)
	rep.Propagation = propagation
	return rep, nil
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
	var sampling *SamplingReport
	if opts.Live {
		dyn, err := buildDynamicClient(KubeOptions{Kubeconfig: opts.Kubeconfig, Context: opts.KubeContext})
		if err != nil {
			return nil, err
		}
		samples, sampling, err = FetchLiveSamplesCRDSampled(context.Background(), dyn, crd, cfg.Spec.HubVersion, opts.Sampling, opts.Namespace)
		if err != nil {
			return nil, fmt.Errorf("fetching live samples: %w", err)
		}
		if len(samples) == 0 {
			return nil, fmt.Errorf("no live objects of %s found at version %s", crdName(crd), cfg.Spec.HubVersion)
		}
	} else if opts.SamplesDir != "" {
		samples, err = LoadSamples(opts.SamplesDir)
		if err != nil {
			return nil, err
		}
		samples, err = filterSamplesByGVK(samples, crd.Spec.Group, crd.Spec.Names.Kind)
		if err != nil {
			return nil, err
		}
		if len(samples) == 0 {
			return nil, fmt.Errorf("no %s.%s objects under %s", crd.Spec.Names.Kind, crd.Spec.Group, opts.SamplesDir)
		}
	}

	report, versions, err := runAnalyzeCRD(crd, cfg)
	if err != nil {
		return nil, err
	}
	if report.HasErrors() {
		return nil, fmt.Errorf("configuration is invalid against the CRD schema, cannot test conversions:%s", summarizeSpokeErrors(report))
	}
	if opts.Fuzz > 0 {
		fuzzed, ferr := generateFuzzSamples(versions, cfg.Spec.HubVersion, crd.Spec.Group, crd.Spec.Names.Kind, opts.Fuzz, opts.effectiveSeed(), shapesFromRules(report))
		if ferr != nil {
			return nil, ferr
		}
		samples = append(samples, fuzzed...)
	}
	if len(samples) == 0 {
		return nil, errors.New("nothing to test: pass --samples, --live, or --fuzz")
	}
	router, err := buildRouterCRD(cfg, report)
	if err != nil {
		return nil, err
	}
	// A native CRD's authored schema is the whole schema — nothing is
	// injected behind the author's back — so there is nothing to strip.
	rep, err := runTestCommon(opts, "CRD", crdName(crd), cfg.Name, cfg.Spec.HubVersion, samples, versions, report, router, nil, start)
	if err != nil {
		return nil, err
	}
	rep.Meta.Sampling = sampling
	return rep, nil
}

// runTestCommon is runTestXRD/runTestCRD's shared tail: exercising every
// sample across every configured-version pair is entirely independent of
// whether the target is an XRD or a native CRD, once a Router and an
// AnalyzeReport already exist.
func runTestCommon(opts TestOptions, resourceKind, resourceName, configName, hubVersion string, samples []Sample, versions []engine.VersionSchema, report engine.AnalyzeReport, router *engine.Router, injected []engine.FieldPath, start time.Time) (*Report, error) {
	seed := opts.FuzzSeed
	var corp *corpus
	switch {
	case opts.RecordDir != "":
		corp = newCorpus(opts.RecordDir, "record")
	case opts.GoldenDir != "":
		corp = newCorpus(opts.GoldenDir, "golden")
	}

	var validator *outputValidator
	if opts.ValidateOutput {
		var err error
		validator, err = newOutputValidator(versions, injected)
		if err != nil {
			return nil, err
		}
	}
	served := servedVersions(versions)
	configured := configuredVersions(hubVersion, report, served)
	targets := configured
	if len(opts.RestrictVersionPairs) > 0 {
		targets = restrictTargets(targets, opts.RestrictVersionPairs)
	}

	lossyPaths := buildLossyPathIndex(report)
	ruleUsage := map[string]int{}

	rep := &Report{}
	rep.Meta.ResourceKind = resourceKind
	rep.Meta.Resource = resourceName
	rep.Meta.Config = configName
	rep.Meta.ConfigPath = opts.ConfigPath
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
				sr, counts, usage := testOneSample(opts, router, hubVersion, lossyPaths, report, samples[i], configured, targets, validator, corp)

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

	if opts.Fuzz > 0 {
		rep.Meta.Fuzz = &FuzzMeta{Objects: opts.Fuzz, Seed: seed}
	}
	if opts.RecordFailuresDir != "" {
		if err := recordFailingSamples(opts.RecordFailuresDir, samples, results); err != nil {
			return nil, err
		}
	}

	rep.Samples = results
	rep.Summary.Samples = len(samples)
	rep.Summary.SamplesByCRD = samplesByCRD(samples)

	for _, sr := range report.SpokeReports {
		for _, rr := range sr.RuleResults {
			id := ruleID(sr.Version, rr)
			rep.RuleCoverage = append(rep.RuleCoverage, RuleCoverage{RuleID: id, MatchedSamples: ruleUsage[id]})
		}
	}

	if corp != nil {
		if err := corp.finish(GoldenManifest{
			ConvctlVersion: Version,
			PlanHash:       hashPlan(report, hubVersion),
			SchemaHash:     hashSchemas(versions),
			Resource:       resourceName,
			Config:         configName,
			HubVersion:     hubVersion,
		}); err != nil {
			return nil, err
		}
		rep.Golden = &GoldenReport{
			Dir:     corp.dir,
			Mode:    corp.mode,
			Written: corp.written,
			Drifts:  corp.drifts,
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
func testOneSample(opts TestOptions, router *engine.Router, hubVersion string, lossyPaths map[string]map[string]bool, report engine.AnalyzeReport, s Sample, configured, targets []string, validator *outputValidator, corp *corpus) (SampleResult, sampleCounts, map[string]int) {
	sr := SampleResult{File: s.File, AssertedVersion: s.Version, CRD: s.CRD, CRDRole: s.CRDRole}
	var counts sampleCounts
	usage := map[string]int{}
	if !containsString(configured, s.Version) {
		pr := PathResult{From: s.Version, To: s.Version, Result: "error"}
		pr.Issues = append(pr.Issues, Issue{
			Field:  "(sample)",
			From:   s.Version,
			To:     s.Version,
			Type:   "error",
			Detail: fmt.Sprintf("sample is %s but the conversion config has no compiled plan for that version — move the object to a remaining spoke or the hub before dropping this version", s.Version),
			Sample: s.File,
		})
		sr.Paths = append(sr.Paths, pr)
		counts.pathsTested++
		counts.errors++
		return sr, counts, usage
	}
	for _, target := range targets {
		if opts.SkipIdentity && target == s.Version {
			continue
		}
		pr := testOnePath(router, hubVersion, lossyPaths, report, s, target, usage, validator, corp)
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

// configuredVersions is hub + every spoke with a compiled plan, keeping
// the XRD/CRD served-version order. Served versions the config does not
// declare are omitted — the same reason validate allows a still-served
// version that is no longer a spoke.
func configuredVersions(hub string, report engine.AnalyzeReport, served []string) []string {
	want := map[string]bool{}
	if hub != "" {
		want[hub] = true
	}
	for _, sr := range report.SpokeReports {
		if sr.CompiledPlan != nil {
			want[sr.Version] = true
		}
	}
	var out []string
	for _, v := range served {
		if want[v] {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return served
	}
	return out
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

// withDestAPIVersion returns a shallow copy of converted carrying the
// destination apiVersion, derived from the source object's own group.
func withDestAPIVersion(converted, source map[string]any, to string) map[string]any {
	av, _ := source["apiVersion"].(string)
	i := strings.LastIndex(av, "/")
	if i <= 0 {
		return converted
	}
	out := make(map[string]any, len(converted)+1)
	for k, v := range converted {
		out[k] = v
	}
	out["apiVersion"] = av[:i] + "/" + to
	return out
}

func testOnePath(router *engine.Router, hub string, lossyPaths map[string]map[string]bool, report engine.AnalyzeReport, s Sample, target string, ruleUsage map[string]int, validator *outputValidator, corp *corpus) PathResult {
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

	// The corpus records the forward result — what this conversion
	// actually produces — rather than the round-trip, which is the thing a
	// reviewer needs to see change.
	//
	// Stamped with the destination apiVersion, on a copy: engine.Convert
	// leaves apiVersion to its caller, so a golden without it could not
	// tell a conversion to the wrong version from a correct one. The copy
	// is what keeps that stamp out of the object the round-trip used.
	corp.observe(s.File, s.Version, target, withDestAPIVersion(forward, s.Object, target))

	// Validate the forward result against the destination version's own
	// schema before looking at round-trip fidelity. A round-trip diff says
	// the rules agree with each other; this says the apiserver will accept
	// what they produced, which is a different question and the one that
	// bites in production.
	if validator.knows(target) {
		spoke := target
		direction := "toSpoke"
		if target == hub {
			spoke = s.Version
			direction = "toHub"
		}
		for _, v := range attributeViolations(validator.validate(forward, target), report, spoke, direction) {
			pr.Issues = append(pr.Issues, Issue{
				Field:  v.Path,
				From:   s.Version,
				To:     target,
				Type:   "schema-violation",
				Detail: v.String(),
				Sample: s.File,
			})
		}
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

	schemaViolations := 0
	for _, is := range pr.Issues {
		if is.Type == "schema-violation" {
			schemaViolations++
		}
	}
	switch {
	// A violation outranks a loss: an object the apiserver rejects is not
	// a lossy conversion, it is a failed one.
	case schemaViolations > 0:
		pr.Result = "error"
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

// samplesByCRD breaks a --live run's sample count down per generated CRD,
// preserving the order FetchLiveSamples listed them in (composite first,
// then claim). It returns nil unless more than one CRD contributed —
// "1 CRD, N samples" is what every other run already reports.
func samplesByCRD(samples []Sample) []CRDSampleCount {
	var order []string
	byCRD := map[string]*CRDSampleCount{}
	for _, s := range samples {
		if s.CRD == "" {
			continue
		}
		c, ok := byCRD[s.CRD]
		if !ok {
			c = &CRDSampleCount{CRD: s.CRD, Role: s.CRDRole}
			byCRD[s.CRD] = c
			order = append(order, s.CRD)
		}
		c.Samples++
	}
	if len(order) < 2 {
		return nil
	}
	out := make([]CRDSampleCount, 0, len(order))
	for _, name := range order {
		out = append(out, *byCRD[name])
	}
	return out
}
