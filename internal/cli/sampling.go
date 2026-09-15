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
	"math/rand"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Sampling strategies for --live on a large cluster.
const (
	// SampleFirst stops listing once the cap is reached. Cheapest, and
	// biased toward whatever the apiserver returns first.
	SampleFirst = "first"
	// SampleRandom reservoir-samples while paginating, so the whole
	// population is represented without ever holding it.
	SampleRandom = "random"
	// SampleNewest keeps the n most recently created objects, which is
	// where a schema change's effects show up first.
	SampleNewest = "newest"
)

// SamplingOptions bounds a --live run.
type SamplingOptions struct {
	// MaxSamples caps the objects tested. Zero means no cap.
	MaxSamples int
	// Strategy is one of the constants above. Empty means SampleFirst.
	Strategy string
	// Seed makes SampleRandom reproducible, so a CI failure can be
	// re-run. Zero means a fixed default rather than a random one: a fuzz
	// or sample failure nobody can reproduce is noise.
	Seed int64
}

func (o SamplingOptions) enabled() bool { return o.MaxSamples > 0 }

func (o SamplingOptions) strategy() string {
	if o.Strategy == "" {
		return SampleFirst
	}
	return o.Strategy
}

// ValidateSamplingOptions rejects a strategy the sampler does not implement,
// rather than silently falling back to one that samples differently.
func ValidateSamplingOptions(o SamplingOptions) error {
	if o.MaxSamples < 0 {
		return fmt.Errorf("--max-samples must not be negative, got %d", o.MaxSamples)
	}
	switch o.strategy() {
	case SampleFirst, SampleRandom, SampleNewest:
	default:
		return fmt.Errorf("invalid --sample-strategy %q (want first, random, or newest)", o.Strategy)
	}
	if o.Strategy != "" && o.MaxSamples == 0 {
		return fmt.Errorf("--sample-strategy %s has no effect without --max-samples", o.Strategy)
	}
	return nil
}

// SamplingReport records that a run was sampled, and from what.
//
// This is the part that matters: a sampled green result that looks like an
// exhaustive green result is worse than no result at all, because somebody
// upgrades on the strength of it.
type SamplingReport struct {
	Strategy string `json:"strategy"`
	// Cap is the requested maximum.
	Cap int `json:"cap"`
	// Population is how many objects exist, counted while paginating even
	// when most were never held.
	Population int `json:"population"`
	// Tested is how many were actually sampled.
	Tested int `json:"tested"`
	// Seed is present for the random strategy, so the run is repeatable.
	Seed int64 `json:"seed,omitempty"`
}

func (s *SamplingReport) String() string {
	if s == nil {
		return ""
	}
	seed := ""
	if s.Strategy == SampleRandom {
		seed = fmt.Sprintf(", seed %d", s.Seed)
	}
	return fmt.Sprintf("SAMPLED: %d of %d live object(s), strategy %s%s — this run did NOT cover every object",
		s.Tested, s.Population, s.Strategy, seed)
}

// sampler accumulates at most MaxSamples objects out of a stream, counting
// the whole population as it goes.
type sampler struct {
	opts SamplingOptions
	rnd  *rand.Rand

	seen int
	kept []Sample
	// order holds each kept sample's creation timestamp for SampleNewest,
	// parallel to kept.
	order []string
}

func newSampler(opts SamplingOptions) *sampler {
	seed := opts.Seed
	if seed == 0 {
		seed = 1
	}
	return &sampler{
		opts: opts,
		// #nosec G404 -- reproducibility from --seed is the point; this
		// selects which objects to test, not anything secret.
		rnd: rand.New(rand.NewSource(seed)),
	}
}

// full reports whether listing can stop early. Only the first strategy can
// stop: the other two need to see the whole population to be what they
// claim.
func (s *sampler) full() bool {
	return s.opts.enabled() && s.opts.strategy() == SampleFirst && len(s.kept) >= s.opts.MaxSamples
}

// add offers one object to the sample.
func (s *sampler) add(sample Sample, obj *unstructured.Unstructured) {
	s.seen++
	if !s.opts.enabled() {
		s.kept = append(s.kept, sample)
		return
	}
	switch s.opts.strategy() {
	case SampleFirst:
		if len(s.kept) < s.opts.MaxSamples {
			s.kept = append(s.kept, sample)
		}
	case SampleRandom:
		// Algorithm R. The i-th item (1-based) replaces a uniformly chosen
		// slot with probability n/i, which leaves every item of the
		// population equally likely to be kept — without ever holding more
		// than n of them.
		if len(s.kept) < s.opts.MaxSamples {
			s.kept = append(s.kept, sample)
			return
		}
		if j := s.rnd.Intn(s.seen); j < s.opts.MaxSamples {
			s.kept[j] = sample
		}
	case SampleNewest:
		ts := ""
		if obj != nil {
			ts = obj.GetCreationTimestamp().UTC().Format("2006-01-02T15:04:05Z")
		}
		s.kept = append(s.kept, sample)
		s.order = append(s.order, ts)
		if len(s.kept) > s.opts.MaxSamples {
			s.trimOldest()
		}
	}
}

// trimOldest keeps the window at the cap by dropping the oldest entry, so
// the newest strategy holds n rather than the population.
func (s *sampler) trimOldest() {
	oldest := 0
	for i := 1; i < len(s.order); i++ {
		if s.order[i] < s.order[oldest] {
			oldest = i
		}
	}
	s.kept = append(s.kept[:oldest], s.kept[oldest+1:]...)
	s.order = append(s.order[:oldest], s.order[oldest+1:]...)
}

// result returns the samples and, when the run was actually sampled, the
// report that says so.
func (s *sampler) result() ([]Sample, *SamplingReport) {
	kept := s.kept
	if s.opts.enabled() && s.opts.strategy() == SampleNewest {
		// Newest first, so a truncated report still reads in a meaningful
		// order, and deterministically for equal timestamps.
		idx := make([]int, len(kept))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			if s.order[idx[a]] != s.order[idx[b]] {
				return s.order[idx[a]] > s.order[idx[b]]
			}
			return kept[idx[a]].File < kept[idx[b]].File
		})
		sorted := make([]Sample, 0, len(kept))
		for _, i := range idx {
			sorted = append(sorted, kept[i])
		}
		kept = sorted
	}
	if !s.opts.enabled() || s.seen <= len(kept) {
		return kept, nil
	}
	return kept, &SamplingReport{
		Strategy:   s.opts.strategy(),
		Cap:        s.opts.MaxSamples,
		Population: s.seen,
		Tested:     len(kept),
		Seed:       s.opts.Seed,
	}
}
