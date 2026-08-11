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

// Package webhookserver implements the shared, horizontally-scalable
// conversion webhook runtime. Every replica is symmetric and
// self-sufficient: it runs its own lightweight controller-runtime manager
// watching XRDConversionConfig, ConversionWebhookServer, and the relevant
// XRDs directly, and maintains its own in-memory registry of compiled
// conversion plans — there is no push mechanism from the main operator and
// no shared cache between replicas, which keeps the admission-critical
// hot path free of any network dependency at request time.
package webhookserver

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// CompiledEntry is one XRD's currently-servable conversion configuration.
type CompiledEntry struct {
	// Router performs hub-and-spoke conversion for this XRD. Nil if this
	// XRD has never successfully compiled (see LastError).
	Router *engine.Router
	// ConversionReviewVersions is the set of ConversionReview API versions
	// this config declared support for.
	ConversionReviewVersions []string
	// Lossless records, per spoke version, whether that direction is
	// statically known to be lossy — used to increment the runtime
	// lossy-conversion-triggered metric distinctly from static analysis.
	Lossless map[string]engine.LosslessVerdict

	PlanHash   string
	CompiledAt time.Time

	// LastError is set when the most recent reconcile attempt failed to
	// (re)compile this XRD's plan. Router, if non-nil, still reflects the
	// last known-good plan — a failed recompile never removes a working
	// entry, it only annotates it with why the latest attempt didn't
	// replace it.
	LastError   string
	LastErrorAt time.Time
}

// Registry is the lock-free-on-read, copy-on-write map of compiled
// conversion plans this replica currently serves. The HTTP hot path only
// ever calls Get, which is a single atomic load — no locks, no map
// mutation contention with the background reconciler.
type Registry struct {
	m atomic.Pointer[map[string]*CompiledEntry]
	// mu serializes writers (the reconciler is effectively
	// single-threaded via controller-runtime's workqueue, but this
	// guards against any future concurrent writer).
	mu sync.Mutex
}

// NewRegistry returns an empty, ready-to-use Registry.
func NewRegistry() *Registry {
	r := &Registry{}
	empty := map[string]*CompiledEntry{}
	r.m.Store(&empty)
	return r
}

// Get returns the entry for xrdName, if any.
func (r *Registry) Get(xrdName string) (*CompiledEntry, bool) {
	m := *r.m.Load()
	e, ok := m[xrdName]
	return e, ok
}

// Set installs a freshly compiled entry for xrdName, replacing whatever
// was there before (including any recorded error).
func (r *Registry) Set(xrdName string, entry *CompiledEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := *r.m.Load()
	next := make(map[string]*CompiledEntry, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	next[xrdName] = entry
	r.m.Store(&next)
}

// RecordError annotates the existing entry for xrdName with a compile
// failure, preserving its Router (if any) untouched — a single bad config
// must never remove a previously-working plan. If there is no existing
// entry, a Router-less placeholder is recorded so /debug/registry and
// metrics can surface the failure even before anything has ever compiled.
func (r *Registry) RecordError(xrdName string, errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := *r.m.Load()
	next := make(map[string]*CompiledEntry, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	existing := next[xrdName]
	var updated CompiledEntry
	if existing != nil {
		updated = *existing
	}
	updated.LastError = errMsg
	updated.LastErrorAt = time.Now()
	next[xrdName] = &updated
	r.m.Store(&next)
}

// Remove drops xrdName from the registry entirely — used when a config is
// deleted or no longer resolves to this replica's server.
func (r *Registry) Remove(xrdName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := *r.m.Load()
	if _, ok := old[xrdName]; !ok {
		return
	}
	next := make(map[string]*CompiledEntry, len(old))
	for k, v := range old {
		if k != xrdName {
			next[k] = v
		}
	}
	r.m.Store(&next)
}

// Snapshot returns a shallow copy of the current registry contents, for
// /debug/registry and the registry-size metric.
func (r *Registry) Snapshot() map[string]*CompiledEntry {
	m := *r.m.Load()
	out := make(map[string]*CompiledEntry, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Len returns the current number of registered XRDs (including
// error-only, Router-less placeholders).
func (r *Registry) Len() int {
	return len(*r.m.Load())
}
