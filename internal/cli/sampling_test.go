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
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// feed offers n objects to a sampler, oldest first, the way a paginated
// list would.
func feed(s *sampler, n int) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		obj := &unstructured.Unstructured{Object: map[string]any{}}
		obj.SetName(fmt.Sprintf("obj-%04d", i))
		obj.SetCreationTimestamp(metav1.NewTime(base.Add(time.Duration(i) * time.Minute)))
		s.add(Sample{File: "cluster:" + obj.GetName()}, obj)
	}
}

// No cap means the behaviour nobody asked to change: every object, and no
// claim that the run was sampled.
func TestSampler_UncappedKeepsEverythingAndReportsNothing(t *testing.T) {
	s := newSampler(SamplingOptions{})
	feed(s, 500)
	kept, rep := s.result()
	if len(kept) != 500 {
		t.Errorf("kept %d, want all 500", len(kept))
	}
	if rep != nil {
		t.Errorf("an uncapped run claimed to be sampled: %+v", rep)
	}
}

// "first" is the one strategy that can stop listing early, which is the
// whole reason it is the cheapest.
func TestSampler_FirstStopsEarly(t *testing.T) {
	s := newSampler(SamplingOptions{MaxSamples: 10, Strategy: SampleFirst})
	for i := 0; i < 10; i++ {
		if s.full() {
			t.Fatalf("full at %d, before the cap", i)
		}
		s.add(Sample{File: fmt.Sprintf("o%d", i)}, nil)
	}
	if !s.full() {
		t.Fatal("not full at the cap, so listing would continue pointlessly")
	}
	kept, _ := s.result()
	if len(kept) != 10 || kept[0].File != "o0" {
		t.Errorf("kept %d starting at %q, want the first 10", len(kept), kept[0].File)
	}
}

// The other two need the whole population to be what they claim, so they
// must never stop listing early.
func TestSampler_RandomAndNewestNeverStopEarly(t *testing.T) {
	for _, strategy := range []string{SampleRandom, SampleNewest} {
		s := newSampler(SamplingOptions{MaxSamples: 5, Strategy: strategy})
		feed(s, 50)
		if s.full() {
			t.Errorf("%s reported full, which would truncate the population it samples from", strategy)
		}
	}
}

// Reservoir sampling has to be uniform, or "random" is a word rather than a
// property. Every item should appear at roughly cap/population frequency.
func TestSampler_RandomIsUniform(t *testing.T) {
	const population, keep, runs = 100, 10, 4000
	counts := make([]int, population)
	for r := 0; r < runs; r++ {
		s := newSampler(SamplingOptions{MaxSamples: keep, Strategy: SampleRandom, Seed: int64(r + 1)})
		feed(s, population)
		kept, _ := s.result()
		if len(kept) != keep {
			t.Fatalf("kept %d, want %d", len(kept), keep)
		}
		for _, k := range kept {
			var idx int
			if _, err := fmt.Sscanf(k.File, "cluster:obj-%04d", &idx); err == nil {
				counts[idx]++
			}
		}
	}
	// Expected selections per item: runs * keep / population = 400.
	expected := runs * keep / population
	for i, c := range counts {
		if c < expected/2 || c > expected*2 {
			t.Errorf("item %d selected %d times, expected around %d — the reservoir is biased", i, c, expected)
		}
	}
}

// A CI failure nobody can reproduce is noise, so the same seed has to
// produce the same sample.
func TestSampler_RandomIsReproducibleUnderASeed(t *testing.T) {
	sample := func() []string {
		s := newSampler(SamplingOptions{MaxSamples: 8, Strategy: SampleRandom, Seed: 42})
		feed(s, 200)
		kept, _ := s.result()
		var names []string
		for _, k := range kept {
			names = append(names, k.File)
		}
		return names
	}
	a, b := sample(), sample()
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Errorf("same seed produced different samples:\n%v\n%v", a, b)
	}
	s := newSampler(SamplingOptions{MaxSamples: 8, Strategy: SampleRandom, Seed: 43})
	feed(s, 200)
	other, _ := s.result()
	var names []string
	for _, k := range other {
		names = append(names, k.File)
	}
	if strings.Join(a, ",") == strings.Join(names, ",") {
		t.Error("two different seeds produced identical samples")
	}
}

// "newest" has to hold the cap, not the population — that is the point —
// and still end up with the newest ones.
func TestSampler_NewestKeepsTheLatestAndBoundsWhatItHolds(t *testing.T) {
	s := newSampler(SamplingOptions{MaxSamples: 5, Strategy: SampleNewest})
	feed(s, 100)
	if len(s.kept) > 5 {
		t.Errorf("held %d objects, want at most the cap of 5", len(s.kept))
	}
	kept, rep := s.result()
	if len(kept) != 5 {
		t.Fatalf("kept %d, want 5", len(kept))
	}
	// Newest first: obj-0099 down to obj-0095.
	for i, want := range []string{"cluster:obj-0099", "cluster:obj-0098", "cluster:obj-0097", "cluster:obj-0096", "cluster:obj-0095"} {
		if kept[i].File != want {
			t.Errorf("kept[%d] = %q, want %q", i, kept[i].File, want)
		}
	}
	if rep == nil || rep.Population != 100 || rep.Tested != 5 {
		t.Errorf("report = %+v, want population 100 and 5 tested", rep)
	}
}

// A sampled green result that reads like an exhaustive green result is
// worse than no result, so the report has to say so in words.
func TestSamplingReport_SaysItDidNotCoverEverything(t *testing.T) {
	s := newSampler(SamplingOptions{MaxSamples: 3, Strategy: SampleRandom, Seed: 7})
	feed(s, 40)
	_, rep := s.result()
	if rep == nil {
		t.Fatal("a capped run that saw more than the cap reported nothing")
	}
	msg := rep.String()
	for _, want := range []string{"SAMPLED", "3 of 40", "random", "seed 7", "did NOT cover every object"} {
		if !strings.Contains(msg, want) {
			t.Errorf("report line is missing %q: %s", want, msg)
		}
	}
}

// A population that fits under the cap was not sampled, and saying it was
// would be its own kind of wrong.
func TestSampler_NoReportWhenThePopulationFitsUnderTheCap(t *testing.T) {
	s := newSampler(SamplingOptions{MaxSamples: 100, Strategy: SampleRandom, Seed: 1})
	feed(s, 10)
	kept, rep := s.result()
	if len(kept) != 10 {
		t.Errorf("kept %d, want 10", len(kept))
	}
	if rep != nil {
		t.Errorf("claimed to have sampled a population that fit: %+v", rep)
	}
}

// A strategy the sampler does not implement must be rejected, not silently
// replaced by one that samples differently.
func TestValidateSamplingOptions(t *testing.T) {
	if err := ValidateSamplingOptions(SamplingOptions{MaxSamples: 10, Strategy: SampleNewest}); err != nil {
		t.Errorf("a valid combination was rejected: %v", err)
	}
	if err := ValidateSamplingOptions(SamplingOptions{MaxSamples: 10, Strategy: "latest"}); err == nil {
		t.Error("an unimplemented strategy was accepted")
	}
	if err := ValidateSamplingOptions(SamplingOptions{Strategy: SampleRandom}); err == nil {
		t.Error("a strategy with no cap was accepted, where it would do nothing")
	}
	if err := ValidateSamplingOptions(SamplingOptions{MaxSamples: -1}); err == nil {
		t.Error("a negative cap was accepted")
	}
}

// The JUnit reporter is where a sampled run is most likely to be mistaken
// for an exhaustive one: a wall of green test cases with no other context.
func TestReport_JUnitRecordsThatARunWasSampled(t *testing.T) {
	rep := &Report{}
	rep.Meta.ResourceKind = "XRD"
	rep.Meta.Resource = "xthings.example.org"
	rep.Meta.Sampling = &SamplingReport{Strategy: SampleRandom, Cap: 50, Population: 40000, Tested: 50, Seed: 9}
	suite := rep.junitSuite()
	if suite.Props == nil {
		t.Fatal("JUnit suite carries no sampling properties")
	}
	got := map[string]string{}
	for _, p := range suite.Props.Properties {
		got[p.Name] = p.Value
	}
	if got["sampled"] != "true" || got["samplePopulation"] != "40000" || got["sampleTested"] != "50" {
		t.Errorf("properties = %v, want the population and tested counts", got)
	}
}

// And in the human-readable report.
func TestReport_TableSaysARunWasSampled(t *testing.T) {
	rep := &Report{}
	rep.Meta.ResourceKind = "XRD"
	rep.Meta.Sampling = &SamplingReport{Strategy: SampleFirst, Cap: 10, Population: 900, Tested: 10}
	var sb strings.Builder
	rep.WriteTable(&sb)
	if !strings.Contains(sb.String(), "SAMPLED: 10 of 900") {
		t.Errorf("table does not report the sampling:\n%s", sb.String())
	}
}

// The cheapest strategy stops listing at the cap, so seen == kept — and a
// report keyed only on that comparison said nothing, which meant the one
// strategy that cannot know the population was the one that silently
// claimed to have covered it.
func TestSampler_FirstReportsSamplingEvenThoughItStoppedEarly(t *testing.T) {
	s := newSampler(SamplingOptions{MaxSamples: 5, Strategy: SampleFirst})
	for i := 0; i < 100; i++ {
		if s.full() {
			break
		}
		s.add(Sample{File: fmt.Sprintf("o%d", i)}, nil)
	}
	kept, rep := s.result()
	if len(kept) != 5 {
		t.Fatalf("kept %d, want the cap of 5", len(kept))
	}
	if rep == nil {
		t.Fatal("stopping early reported no sampling at all, so the run reads as exhaustive")
	}
	if !rep.Truncated {
		t.Error("the report does not record that listing stopped early")
	}
	if rep.Population != 0 {
		t.Errorf("Population = %d; it was never counted and must not be implied", rep.Population)
	}
	msg := rep.String()
	if !strings.Contains(msg, "total is unknown") || !strings.Contains(msg, "did NOT cover every object") {
		t.Errorf("the line does not say the total is unknown: %s", msg)
	}
	if strings.Contains(msg, "5 of 5") {
		t.Errorf("the line implies the population equals the sample: %s", msg)
	}
}

// And the JUnit properties must not imply it either.
func TestReport_JUnitSaysThePopulationIsUnknownWhenTruncated(t *testing.T) {
	rep := &Report{}
	rep.Meta.Sampling = &SamplingReport{Strategy: SampleFirst, Cap: 5, Tested: 5, Truncated: true}
	got := map[string]string{}
	for _, p := range rep.junitSuite().Props.Properties {
		got[p.Name] = p.Value
	}
	if got["samplePopulation"] != "unknown" {
		t.Errorf("samplePopulation = %q, want unknown", got["samplePopulation"])
	}
}

// A population that genuinely fit under the cap still reports nothing.
func TestSampler_FirstUnderTheCapIsNotSampling(t *testing.T) {
	s := newSampler(SamplingOptions{MaxSamples: 50, Strategy: SampleFirst})
	feed(s, 10)
	if _, rep := s.result(); rep != nil {
		t.Errorf("a population that fit was reported as sampled: %+v", rep)
	}
}

// RunTest is exported, so the cobra command is not the only way in. Invalid
// sampling options that reach the sampler do the opposite of what they say:
// a negative cap disables the bound and paginates everything into memory,
// and an unknown strategy keeps nothing and then fails for having no
// samples.
func TestRunTest_ValidatesSamplingOptions(t *testing.T) {
	base := TestOptions{
		XRDPath: "testdata/full/xrd.yaml", ConfigPath: "testdata/full/config.yaml",
		SamplesDir: "testdata/full/samples", Quiet: true,
	}

	bad := base
	bad.Sampling = SamplingOptions{MaxSamples: -1}
	if _, err := RunTest(bad); err == nil {
		t.Error("a negative cap was accepted, which disables the bound entirely")
	}

	bad = base
	bad.Sampling = SamplingOptions{MaxSamples: 10, Strategy: "newestish"}
	if _, err := RunTest(bad); err == nil {
		t.Error("an unknown strategy was accepted")
	}

	// A fixture run with no sampling options is unaffected.
	if _, err := RunTest(base); err != nil {
		t.Errorf("an ordinary run was rejected: %v", err)
	}
}
