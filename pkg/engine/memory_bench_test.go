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

package engine

import (
	"fmt"
	"runtime"
	"testing"
)

// retainedBytes reports live heap bytes after a settling GC. Two cycles,
// because the first can leave objects that only became unreachable during
// it still counted.
func retainedBytes() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// BenchmarkCompiledPlanRetained answers "how much memory does one compiled
// plan cost?", which -benchmem cannot: B/op is bytes *allocated* per
// operation, most of which is garbage from the compile itself. What sizes a
// replica is what survives — the Ops, their pre-split paths and pre-decoded
// tables — so this holds every plan it builds and reads the live heap
// either side.
//
// Reported as B/plan rather than B/op. Run it with -benchtime=<N>x so the
// denominator is a number you chose; the default time-based mode makes b.N
// depend on machine speed, which is fine for the average but makes a single
// run harder to reason about. The published numbers are in
// docs/operations/capacity.md.
func BenchmarkCompiledPlanRetained(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("leaves=%d", n), func(b *testing.B) {
			hub := nLeafSchema(n)
			spoke := nLeafSchema(n)
			rs := RuleSet{HubVersion: "v2", SpokeVersion: "v1", Rules: nRenameRules(n)}

			// Allocated before the baseline so the slice's own backing
			// array is not counted as plan memory.
			plans := make([]*Plan, 0, b.N)
			before := retainedBytes()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				plan, _, err := Compile(rs, &hub, &spoke)
				if err != nil {
					b.Fatal(err)
				}
				plans = append(plans, plan)
			}
			b.StopTimer()

			after := retainedBytes()
			runtime.KeepAlive(plans)
			b.ReportMetric(float64(after-before)/float64(b.N), "B/plan")
		})
	}
}

// BenchmarkCompilePeakAlloc is the transient side of the same question.
// Compile allocates heavily and most of it is immediately garbage, so a
// replica compiling many plans at once can peak well above the steady
// footprint the benchmark above measures — and the peak is what an OOM kill
// is decided against, not the steady state.
func BenchmarkCompilePeakAlloc(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("leaves=%d", n), func(b *testing.B) {
			hub := nLeafSchema(n)
			spoke := nLeafSchema(n)
			rs := RuleSet{HubVersion: "v2", SpokeVersion: "v1", Rules: nRenameRules(n)}

			var ms runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&ms)
			startTotal := ms.TotalAlloc

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := Compile(rs, &hub, &spoke); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()

			runtime.ReadMemStats(&ms)
			// Churn per compile: everything allocated, live or not. The
			// gap between this and B/plan above is what the GC has to keep
			// up with during a cold start.
			b.ReportMetric(float64(ms.TotalAlloc-startTotal)/float64(b.N), "B/churn")
		})
	}
}
