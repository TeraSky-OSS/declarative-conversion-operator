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

package enqueue

import (
	"testing"
	"time"
)

func TestFanoutSpread_NoUnboundedBurst(t *testing.T) {
	const n = 200
	spread := FanoutSpread(n, CWSConfigEnqueueQPS)
	// At 50 QPS, 200 configs take (199/50)s ≈ 3.98s — must not be an
	// instantaneous burst (spread == 0) and must stay well under a minute.
	if spread == 0 {
		t.Fatalf("expected non-zero spread for %d configs at %.0f QPS", n, CWSConfigEnqueueQPS)
	}
	want := time.Duration(float64(n-1) / CWSConfigEnqueueQPS * float64(time.Second))
	if spread != want {
		t.Fatalf("spread=%v, want %v", spread, want)
	}
	if spread > 10*time.Second {
		t.Fatalf("spread %v is unexpectedly large for Phase 9's ~200-config case", spread)
	}
}
