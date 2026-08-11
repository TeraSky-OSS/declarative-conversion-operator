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

import "testing"

func TestRegistry_GetSet(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("missing"); ok {
		t.Fatalf("expected no entry in a fresh registry")
	}
	entry := &CompiledEntry{PlanHash: "abc"}
	r.Set("xfoos.example.org", entry)
	got, ok := r.Get("xfoos.example.org")
	if !ok || got.PlanHash != "abc" {
		t.Fatalf("unexpected Get result: ok=%v got=%+v", ok, got)
	}
	if r.Len() != 1 {
		t.Fatalf("expected Len()=1, got %d", r.Len())
	}
}

func TestRegistry_RecordError_CreatesPlaceholderWhenAbsent(t *testing.T) {
	r := NewRegistry()
	r.RecordError("xfoos.example.org", "boom")
	entry, ok := r.Get("xfoos.example.org")
	if !ok {
		t.Fatalf("expected a placeholder entry to be created")
	}
	if entry.Router != nil {
		t.Fatalf("expected a Router-less placeholder, got %+v", entry.Router)
	}
	if entry.LastError != "boom" {
		t.Fatalf("unexpected LastError: %q", entry.LastError)
	}
	if entry.LastErrorAt.IsZero() {
		t.Fatalf("expected LastErrorAt to be set")
	}
}

func TestRegistry_Remove(t *testing.T) {
	r := NewRegistry()
	r.Set("xfoos.example.org", &CompiledEntry{})
	r.Remove("xfoos.example.org")
	if _, ok := r.Get("xfoos.example.org"); ok {
		t.Fatalf("expected the entry to be gone after Remove")
	}
	if r.Len() != 0 {
		t.Fatalf("expected Len()=0 after removing the only entry, got %d", r.Len())
	}
	// Removing an absent key must be a harmless no-op.
	r.Remove("still-not-there")
}

func TestRegistry_Snapshot_IsACopy(t *testing.T) {
	r := NewRegistry()
	r.Set("xfoos.example.org", &CompiledEntry{PlanHash: "v1"})

	snap := r.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 entry in the snapshot, got %d", len(snap))
	}
	// Mutating the map returned by Snapshot must not affect the registry.
	delete(snap, "xfoos.example.org")
	if r.Len() != 1 {
		t.Fatalf("expected the registry to be unaffected by mutating a snapshot, got Len()=%d", r.Len())
	}
}
