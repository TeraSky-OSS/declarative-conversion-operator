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
	"fmt"
	"math"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func shardedServer(name string, weight int32, isDefault bool) teraskyv1alpha1.ConversionWebhookServer {
	s := teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: name}}
	s.Spec.Default = isDefault
	s.Spec.Sharding = &teraskyv1alpha1.ShardingSpec{}
	if weight > 0 {
		s.Spec.Sharding.Weight = &weight
	}
	return s
}

func targets(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("xthings%d.example.org", i)
	}
	return out
}

func assignAll(pool []teraskyv1alpha1.ConversionWebhookServer, keys []string) map[string]string {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = PickShard(pool, k)
	}
	return out
}

func TestPickShard_EmptyPool(t *testing.T) {
	if got := PickShard(nil, "xfoos.example.org"); got != "" {
		t.Fatalf("PickShard on an empty pool = %q, want the empty string", got)
	}
}

func TestPickShard_IsDeterministicAndOrderIndependent(t *testing.T) {
	forward := []teraskyv1alpha1.ConversionWebhookServer{shardedServer("a", 0, false), shardedServer("b", 0, false), shardedServer("c", 0, false)}
	reversed := []teraskyv1alpha1.ConversionWebhookServer{forward[2], forward[1], forward[0]}

	// Every party — the operator and every webhook-server replica —
	// computes this independently. If the answer depended on list order,
	// two of them could disagree about who serves a target, which is the
	// one thing the assign package exists to prevent.
	for _, key := range targets(200) {
		if a, b := PickShard(forward, key), PickShard(reversed, key); a != b {
			t.Fatalf("PickShard(%q) = %q forwards but %q reversed", key, a, b)
		}
	}
}

func TestPickShard_DistributesEvenly(t *testing.T) {
	pool := []teraskyv1alpha1.ConversionWebhookServer{shardedServer("a", 0, false), shardedServer("b", 0, false), shardedServer("c", 0, false), shardedServer("d", 0, false)}
	keys := targets(4000)
	counts := map[string]int{}
	for _, k := range keys {
		counts[PickShard(pool, k)]++
	}
	want := len(keys) / len(pool)
	for _, s := range pool {
		got := counts[s.Name]
		// ±20% of the fair share. Wide enough not to be flaky on a
		// different hash, tight enough to catch a scoring bug that
		// collapses the distribution onto one or two members.
		if math.Abs(float64(got-want)) > float64(want)*0.2 {
			t.Errorf("server %q got %d of %d keys, want roughly %d (±20%%)", s.Name, got, len(keys), want)
		}
	}
}

// The whole reason for rendezvous hashing: adding an instance must move
// only the keys that instance wins, and must not reshuffle the rest.
func TestPickShard_AddingAServerMovesABoundedFraction(t *testing.T) {
	before := []teraskyv1alpha1.ConversionWebhookServer{shardedServer("a", 0, false), shardedServer("b", 0, false), shardedServer("c", 0, false)}
	after := append(append([]teraskyv1alpha1.ConversionWebhookServer{}, before...), shardedServer("d", 0, false))

	keys := targets(4000)
	was, now := assignAll(before, keys), assignAll(after, keys)

	moved, movedToNew := 0, 0
	for _, k := range keys {
		if was[k] == now[k] {
			continue
		}
		moved++
		if now[k] == "d" {
			movedToNew++
		}
	}
	// Every move must be onto the new server. A key moving from a to b
	// when neither changed is the failure mode a hash ring with too few
	// virtual nodes exhibits, and it is invisible in the aggregate count.
	if moved != movedToNew {
		t.Errorf("%d keys moved but only %d moved onto the new server; the rest were reshuffled between servers that did not change", moved, movedToNew)
	}
	// Expected share is 1/4 = 25%.
	fraction := float64(moved) / float64(len(keys))
	if fraction < 0.2 || fraction > 0.3 {
		t.Errorf("adding a fourth server moved %.1f%% of keys, want roughly 25%%", fraction*100)
	}
}

func TestPickShard_RemovingAServerMovesOnlyItsOwn(t *testing.T) {
	before := []teraskyv1alpha1.ConversionWebhookServer{shardedServer("a", 0, false), shardedServer("b", 0, false), shardedServer("c", 0, false)}
	after := before[:2]

	keys := targets(3000)
	was, now := assignAll(before, keys), assignAll(after, keys)
	for _, k := range keys {
		if was[k] != "c" && was[k] != now[k] {
			t.Fatalf("key %q moved from %q to %q although neither server was removed", k, was[k], now[k])
		}
	}
}

func TestPickShard_WeightBiasesTheShare(t *testing.T) {
	pool := []teraskyv1alpha1.ConversionWebhookServer{shardedServer("heavy", 3, false), shardedServer("light", 1, false)}
	keys := targets(4000)
	counts := map[string]int{}
	for _, k := range keys {
		counts[PickShard(pool, k)]++
	}
	ratio := float64(counts["heavy"]) / float64(counts["light"])
	if ratio < 2.5 || ratio > 3.5 {
		t.Errorf("weight 3 against weight 1 produced a %.2f:1 split (%d vs %d), want roughly 3:1",
			ratio, counts["heavy"], counts["light"])
	}
}

func TestShardPool_OnlyOptedInMembers(t *testing.T) {
	plain := teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "plain"}}
	off := teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "off"}}
	disabled := false
	off.Spec.Sharding = &teraskyv1alpha1.ShardingSpec{Enabled: &disabled}

	pool := ShardPool([]teraskyv1alpha1.ConversionWebhookServer{plain, off, shardedServer("in", 0, true)})
	if len(pool) != 1 || pool[0].Name != "in" {
		t.Fatalf("pool = %v, want only the instance that opted in", names(pool))
	}
}

func names(servers []teraskyv1alpha1.ConversionWebhookServer) []string {
	out := make([]string, len(servers))
	for i, s := range servers {
		out[i] = s.Name
	}
	return out
}

// Precedence: an explicit ref beats the pool, the pool beats the default,
// and the default still answers when no pool exists.
func TestResolveAssignment_ShardPrecedence(t *testing.T) {
	cfg := &teraskyv1alpha1.XRDConversionConfig{ObjectMeta: metav1.ObjectMeta{Name: "cfg"}}
	cfg.Spec.TargetXRD.Name = "xfoos.example.org"

	plainDefault := teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	plainDefault.Spec.Default = true
	pinned := teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "tenant-a"}}

	// No pool: the default answers, exactly as before sharding existed.
	got, err := ResolveAssignment(cfg, []teraskyv1alpha1.ConversionWebhookServer{plainDefault, pinned})
	if err != nil || got != "default" {
		t.Fatalf("without a pool: got %q, err %v; want %q", got, err, "default")
	}

	// A pool exists: it answers instead of the default.
	pool := []teraskyv1alpha1.ConversionWebhookServer{shardedServer("default", 0, true), shardedServer("shard-b", 0, false)}
	got, err = ResolveAssignment(cfg, pool)
	if err != nil {
		t.Fatalf("with a pool: %v", err)
	}
	if got != PickShard(ShardPool(pool), cfg.Spec.TargetXRD.Name) {
		t.Fatalf("with a pool: got %q, want the rendezvous winner", got)
	}

	// An explicit ref wins over everything. This is what tenant isolation
	// is built on; sharding must not be able to move a pinned config.
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "tenant-a"}
	withPinned := append(append([]teraskyv1alpha1.ConversionWebhookServer{}, pool...), pinned)
	got, err = ResolveAssignment(cfg, withPinned)
	if err != nil || got != "tenant-a" {
		t.Fatalf("with an explicit ref: got %q, err %v; want %q", got, err, "tenant-a")
	}
}

// Sharding removes the need for a default instance entirely: a pool is a
// complete answer for an unpinned config, where before an absent default
// was a hard error.
func TestResolveAssignment_PoolWithoutADefault(t *testing.T) {
	cfg := &teraskyv1alpha1.CRDConversionConfig{ObjectMeta: metav1.ObjectMeta{Name: "cfg"}}
	cfg.Spec.TargetCRD.Name = "foos.example.org"

	pool := []teraskyv1alpha1.ConversionWebhookServer{shardedServer("a", 0, false), shardedServer("b", 0, false)}
	got, err := ResolveAssignment(cfg, pool)
	if err != nil {
		t.Fatalf("unexpected error with a pool and no default: %v", err)
	}
	if got != "a" && got != "b" {
		t.Fatalf("got %q, want a pool member", got)
	}
}

// Deletion safety cannot be judged by assignment alone. Mid-handover a
// config resolves to its destination while its target still points at the
// source — and the source is still answering every ConversionReview for
// it, because that is what makes waiting for the handover safe. An
// instance in that state must not be deletable.
func TestServedBy_CoversTheMidHandoverSource(t *testing.T) {
	srvA := teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "srv-a"}}
	srvB := teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "srv-b"}}
	servers := []teraskyv1alpha1.ConversionWebhookServer{srvA, srvB}

	cfg := &teraskyv1alpha1.XRDConversionConfig{ObjectMeta: metav1.ObjectMeta{Name: "cfg"}}
	cfg.Spec.TargetXRD.Name = "xfoos.example.org"
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-b"}
	cfg.Status.WebhookURL = "https://srv-a-webhook-server.dco-system.svc/convert/xfoos.example.org"

	if !ServedBy(cfg, servers, "srv-a") {
		t.Error("srv-a is the instance the target still names, so it is still serving it")
	}
	if !ServedBy(cfg, servers, "srv-b") {
		t.Error("srv-b is the assigned destination, so it is on the hook too")
	}
	if IsAssignedTo(cfg, servers, "srv-a") {
		t.Error("fixture is wrong: srv-a should no longer be the assigned server")
	}
}

// The prefix match must not be satisfied by a name that merely starts the
// same way, or deleting "srv" would be blocked by a config pointed at
// "srv-a".
func TestServedBy_DoesNotMatchAPrefixOfAnotherService(t *testing.T) {
	servers := []teraskyv1alpha1.ConversionWebhookServer{
		{ObjectMeta: metav1.ObjectMeta{Name: "srv"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "srv-a"}},
	}
	cfg := &teraskyv1alpha1.CRDConversionConfig{ObjectMeta: metav1.ObjectMeta{Name: "cfg"}}
	cfg.Spec.TargetCRD.Name = "foos.example.org"
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv-a"}
	cfg.Status.WebhookURL = "https://srv-a-webhook-server.dco-system.svc/convert/foos.example.org"

	if ServedBy(cfg, servers, "srv") {
		t.Error(`a target pointed at "srv-a" must not read as pointing at "srv"`)
	}
}

func TestServedBy_NeverAppliedIsNotServed(t *testing.T) {
	servers := []teraskyv1alpha1.ConversionWebhookServer{{ObjectMeta: metav1.ObjectMeta{Name: "srv-a"}}}
	cfg := &teraskyv1alpha1.XRDConversionConfig{ObjectMeta: metav1.ObjectMeta{Name: "cfg"}}
	cfg.Spec.TargetXRD.Name = "xfoos.example.org"
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "other"}
	if ServedBy(cfg, servers, "srv-a") {
		t.Error("a config that has never been applied points at nobody")
	}
}
