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
	"reflect"
	"sort"
	"testing"
)

// normalizeForComparison strips the fields that legitimately differ between
// two runs of the same input — wall-clock timings and the generation
// timestamp — so everything left over must match exactly.
func normalizeForComparison(rep *Report) {
	rep.Meta.DurationMs = 0
	rep.Meta.GeneratedAt = ""
	for i := range rep.Samples {
		for j := range rep.Samples[i].Paths {
			rep.Samples[i].Paths[j].TimingMicros = 0
		}
	}
	sort.Slice(rep.RuleCoverage, func(i, j int) bool { return rep.RuleCoverage[i].RuleID < rep.RuleCoverage[j].RuleID })
}

// TestRunTest_ConcurrencyIsFunctionallyEquivalent is the whole contract of
// the worker pool: more workers must change nothing about the report,
// including the order samples appear in.
func TestRunTest_ConcurrencyIsFunctionallyEquivalent(t *testing.T) {
	base := TestOptions{
		XRDPath:    "testdata/full/xrd.yaml",
		ConfigPath: "testdata/full/config.yaml",
		SamplesDir: "testdata/full/samples",
		Quiet:      true,
	}

	serialOpts := base
	serialOpts.Concurrency = 1
	serial, err := RunTest(serialOpts)
	if err != nil {
		t.Fatalf("serial run: %v", err)
	}
	normalizeForComparison(serial)

	for _, n := range []int{2, 4, 16} {
		opts := base
		opts.Concurrency = n
		parallel, err := RunTest(opts)
		if err != nil {
			t.Fatalf("run with concurrency %d: %v", n, err)
		}
		normalizeForComparison(parallel)
		if !reflect.DeepEqual(serial, parallel) {
			t.Fatalf("concurrency %d produced a different report:\nserial:   %+v\nparallel: %+v", n, serial, parallel)
		}
	}
}

// TestRunTest_SampleOrderIsDeterministic pins the ordering guarantee
// separately, since a DeepEqual failure alone wouldn't say whether the
// contents or just the order drifted.
func TestRunTest_SampleOrderIsDeterministic(t *testing.T) {
	want, err := LoadSamples("testdata/full/samples")
	if err != nil {
		t.Fatalf("load samples: %v", err)
	}

	for _, n := range []int{1, 3, 8} {
		rep, err := RunTest(TestOptions{
			XRDPath:     "testdata/full/xrd.yaml",
			ConfigPath:  "testdata/full/config.yaml",
			SamplesDir:  "testdata/full/samples",
			Quiet:       true,
			Concurrency: n,
		})
		if err != nil {
			t.Fatalf("run with concurrency %d: %v", n, err)
		}
		if len(rep.Samples) != len(want) {
			t.Fatalf("concurrency %d: got %d sample results, want %d", n, len(rep.Samples), len(want))
		}
		for i := range want {
			if rep.Samples[i].File != want[i].File {
				t.Fatalf("concurrency %d: sample %d is %q, want %q (load order)", n, i, rep.Samples[i].File, want[i].File)
			}
		}
	}
}

func TestEffectiveConcurrency(t *testing.T) {
	cases := []struct {
		requested, samples, want int
	}{
		{requested: 4, samples: 10, want: 4},
		{requested: 4, samples: 2, want: 2},
		{requested: -1, samples: 1, want: 1},
		{requested: 0, samples: 1, want: 1},
		{requested: 1, samples: 0, want: 1},
	}
	for _, c := range cases {
		if got := (TestOptions{Concurrency: c.requested}).effectiveConcurrency(c.samples); got != c.want {
			t.Errorf("effectiveConcurrency(requested=%d, samples=%d) = %d, want %d", c.requested, c.samples, got, c.want)
		}
	}
	// An unset Concurrency scales with the machine, but never below one
	// worker even on a single-CPU runner.
	if got := (TestOptions{}).effectiveConcurrency(1000); got < 1 {
		t.Errorf("default concurrency = %d, want at least 1", got)
	}
}
