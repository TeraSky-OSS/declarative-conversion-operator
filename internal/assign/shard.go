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

package assign

import (
	"hash/fnv"
	"math"
	"sort"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

// ShardPool is the ordered set of ConversionWebhookServer instances that
// unpinned conversion configs are distributed across — every instance with
// spec.sharding.enabled. Sorted by name so every party computing an
// assignment walks the same list in the same order.
//
// Empty means sharding is not in use anywhere, and assignment falls back
// to the single default instance exactly as it did before.
func ShardPool(allServers []teraskyv1alpha1.ConversionWebhookServer) []teraskyv1alpha1.ConversionWebhookServer {
	var pool []teraskyv1alpha1.ConversionWebhookServer
	for _, s := range allServers {
		if s.Spec.ShardingEnabled() {
			pool = append(pool, s)
		}
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].Name < pool[j].Name })
	return pool
}

// PickShard returns the pool member that owns key, by weighted rendezvous
// (highest-random-weight) hashing. An empty pool yields "".
//
// Rendezvous rather than a hash ring, for three reasons that matter here:
//
//   - **Minimal, bounded movement.** Adding an instance moves only the keys
//     that instance now wins — in expectation 1/(N+1) of them — and moves
//     nothing between the instances that were already there. Removing one
//     moves only its own keys. That is the optimal disruption property, and
//     a ring only approximates it, with a quality that depends on how many
//     virtual nodes you remembered to configure.
//   - **Even distribution with no tuning.** A ring with too few virtual
//     nodes distributes badly; there is no equivalent knob to get wrong
//     here.
//   - **No shared state.** The answer is a pure function of (key, pool), so
//     the operator and every webhook-server replica compute it
//     independently and always agree — which is the property the whole
//     assign package exists to preserve.
//
// The cost is O(N) per lookup instead of O(log N). N is the number of
// webhook-server instances, which is single digits.
func PickShard(pool []teraskyv1alpha1.ConversionWebhookServer, key string) string {
	best, bestScore := "", math.Inf(-1)
	for _, s := range pool {
		score := shardScore(s.Name, key, s.Spec.ShardWeight())
		// Ties broken by name so the result never depends on pool order.
		// Reachable only on a hash collision, but "never depends on the
		// order" has to hold unconditionally for every party to agree.
		if score > bestScore || (score == bestScore && s.Name < best) {
			best, bestScore = s.Name, score
		}
	}
	return best
}

// shardScore is the weighted rendezvous score for one (server, key) pair.
//
// The weighting is the standard one: with h uniform in (0, 1),
// -weight / ln(h) is distributed such that the probability of a given
// server holding the maximum is exactly its share of the total weight.
// Using ln(h) directly rather than a linear multiplier is what makes
// weights behave proportionally instead of merely monotonically.
func shardScore(serverName, key string, weight uint32) float64 {
	h := fnv.New64a()
	// Length-delimited rather than concatenated, so that ("ab", "c") and
	// ("a", "bc") cannot hash to the same value and quietly correlate two
	// unrelated assignments.
	_, _ = h.Write([]byte(serverName))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(key))
	sum := h.Sum64()

	// Map to (0, 1): 2^53 divisor keeps the result exactly representable,
	// and the +1 keeps it strictly positive so the logarithm is finite.
	const mantissa = 1 << 53
	u := float64(sum%mantissa+1) / float64(mantissa+1)
	return -float64(weight) / math.Log(u)
}
