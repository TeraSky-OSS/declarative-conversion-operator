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

package webhookserver

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
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

// BenchmarkRegistryRetained is the registry's own footprint at scale: what
// a replica holds once every assigned plan is compiled and nothing else is
// happening. This is the term that decides how many targets fit in a
// memory limit — together with the informer cache, which is measured
// end-to-end by hack/measure-cache-memory.sh rather than here, because it
// is a property of the cluster's CRDs and not of this code.
//
// The whole fleet is built inside one b.N iteration, so run it with
// -benchtime=1x; the reported B/target is what to read.
func BenchmarkRegistryRetained(b *testing.B) {
	const leaves = 50
	for _, targets := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("targets=%d", targets), func(b *testing.B) {
			objs := benchFleet(targets, leaves)
			c := newFakeClient(objs...).Build()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				before := retainedBytes()
				b.StartTimer()

				r := &Reconciler{
					Client: c, ServerName: "srv", Registry: NewRegistry(),
					EnableXRDSupport: true,
				}
				if _, err := r.InitialSync(context.Background()); err != nil {
					b.Fatal(err)
				}

				b.StopTimer()
				after := retainedBytes()
				if got := r.Registry.Len(); got != targets {
					b.Fatalf("registry holds %d entries, want %d", got, targets)
				}
				b.ReportMetric(float64(after-before)/float64(targets), "B/target")
				runtime.KeepAlive(r)
				b.StartTimer()
			}
		})
	}
}

// BenchmarkInitialSyncPeak is the transient side: how far above the steady
// registry footprint a replica goes while compiling everything at once. It
// is the number a memory limit has to clear, because the OOM killer does
// not wait for the GC.
//
// Sampling ReadMemStats stops the world, so the sampled peak is an
// approximation that also slows the run down slightly. That bias is
// conservative in the right direction — a slower run gives the GC more
// chances to run, so a sampled peak understates rather than overstates.
func BenchmarkInitialSyncPeak(b *testing.B) {
	const leaves = 50
	for _, targets := range []int{100, 1000} {
		b.Run(fmt.Sprintf("targets=%d", targets), func(b *testing.B) {
			objs := benchFleet(targets, leaves)
			c := newFakeClient(objs...).Build()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				base := retainedBytes()
				var peak atomic.Uint64
				done := make(chan struct{})
				go func() {
					var ms runtime.MemStats
					ticker := time.NewTicker(2 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-done:
							return
						case <-ticker.C:
							runtime.ReadMemStats(&ms)
							for {
								cur := peak.Load()
								if ms.HeapAlloc <= cur || peak.CompareAndSwap(cur, ms.HeapAlloc) {
									break
								}
							}
						}
					}
				}()
				b.StartTimer()

				r := &Reconciler{
					Client: c, ServerName: "srv", Registry: NewRegistry(),
					EnableXRDSupport: true,
				}
				if _, err := r.InitialSync(context.Background()); err != nil {
					b.Fatal(err)
				}

				b.StopTimer()
				close(done)
				steady := retainedBytes()
				b.ReportMetric(float64(peak.Load()-base), "B/peak")
				b.ReportMetric(float64(steady-base), "B/steady")
				runtime.KeepAlive(r)
				b.StartTimer()
			}
		})
	}
}
