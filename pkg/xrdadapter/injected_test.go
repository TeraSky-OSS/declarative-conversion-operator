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

package xrdadapter

import (
	"strings"
	"testing"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

func pathSet(paths []engine.FieldPath) map[string]bool {
	out := map[string]bool{}
	for _, p := range paths {
		out[p.String()] = true
	}
	return out
}

func TestInjectedPathsForScope(t *testing.T) {
	cases := []struct {
		name        string
		scope       Scope
		offersClaim bool
		want        []string
		notWant     []string
	}{
		{
			// The modern scopes nest everything under one key, so a single
			// subtree root covers the machinery.
			name:    "Namespaced",
			scope:   ScopeNamespaced,
			want:    []string{"spec.crossplane", "status.conditions"},
			notWant: []string{"spec.compositionRef", "spec.claimRef", "status.connectionDetails", "status.claimConditionTypes"},
		},
		{
			name:    "Cluster",
			scope:   ScopeCluster,
			want:    []string{"spec.crossplane", "status.conditions"},
			notWant: []string{"spec.compositionRef", "spec.claimRef"},
		},
		{
			// LegacyCluster keeps the v1 layout — machinery directly under
			// spec — and its claim CRD shares the authored schema, so the
			// claim's own names (resourceRef singular,
			// compositeDeletePolicy) count too.
			name:        "LegacyCluster with claims",
			scope:       ScopeLegacyCluster,
			offersClaim: true,
			want: []string{
				"spec.compositionRef", "spec.compositionSelector", "spec.compositionRevisionRef",
				"spec.compositionRevisionSelector", "spec.compositionUpdatePolicy", "spec.resourceRefs",
				"spec.claimRef", "spec.writeConnectionSecretToRef",
				"spec.resourceRef", "spec.compositeDeletePolicy",
				"status.conditions", "status.connectionDetails", "status.claimConditionTypes",
			},
			// Crossplane injects no spec.crossplane on LegacyCluster, so an
			// author who declares one there is doing something legitimate.
			notWant: []string{"spec.crossplane"},
		},
		{
			// A LegacyCluster XRD with no spec.claimNames renders ONE CRD,
			// so the claim's own machinery names are not reserved on it —
			// reserving them would reject authored fields Crossplane is
			// never going to overwrite.
			name:  "LegacyCluster without claims",
			scope: ScopeLegacyCluster,
			want: []string{
				"spec.compositionRef", "spec.claimRef", "spec.resourceRefs",
				"status.conditions", "status.connectionDetails", "status.claimConditionTypes",
			},
			notWant: []string{"spec.crossplane", "spec.resourceRef", "spec.compositeDeletePolicy"},
		},
		{name: "Indeterminate", scope: ScopeIndeterminate, notWant: []string{"spec.crossplane", "spec.compositionRef"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pathSet(InjectedPathsForScope(tc.scope, tc.offersClaim))
			for _, w := range tc.want {
				if !got[w] {
					t.Errorf("missing %q", w)
				}
			}
			for _, nw := range tc.notWant {
				if got[nw] {
					t.Errorf("unexpectedly present: %q", nw)
				}
			}
		})
	}
}

func TestInjectedPathsForScope_NoStatusCrossplaneInAnyScope(t *testing.T) {
	for _, s := range []Scope{ScopeNamespaced, ScopeCluster, ScopeLegacyCluster} {
		if pathSet(InjectedPathsForScope(s, true))["status.crossplane"] {
			t.Errorf("scope %s: there is no status.crossplane in any scope", s)
		}
	}
}

func TestInjectedPathsUnion_IsASupersetAndDeduplicated(t *testing.T) {
	union := InjectedPathsUnion()
	set := pathSet(union)
	if len(set) != len(union) {
		t.Errorf("union contains duplicates: %d entries, %d distinct", len(union), len(set))
	}
	for _, s := range []Scope{ScopeNamespaced, ScopeCluster, ScopeLegacyCluster} {
		for _, p := range InjectedPathsForScope(s, true) {
			if !set[p.String()] {
				t.Errorf("union is missing %q from scope %s", p.String(), s)
			}
		}
	}
}

func TestSource_PlatformInjectedPaths(t *testing.T) {
	t.Run("determinate scope yields that scope's set as errors", func(t *testing.T) {
		got := New(scopeXRD("Namespaced", false, false)).PlatformInjectedPaths()
		if got.Platform != "Crossplane" {
			t.Errorf("Platform = %q", got.Platform)
		}
		if got.Uncertain {
			t.Error("an explicitly-scoped XRD is not uncertain")
		}
		if !pathSet(got.Paths)["spec.crossplane"] {
			t.Errorf("expected the Namespaced set, got %v", got.Paths)
		}
	})

	t.Run("claimNames pulls in the LegacyCluster set", func(t *testing.T) {
		got := New(scopeXRD("", true, false)).PlatformInjectedPaths()
		if got.Uncertain {
			t.Error("claimNames settles the scope, so this is not uncertain")
		}
		set := pathSet(got.Paths)
		if !set["spec.claimRef"] || !set["status.claimConditionTypes"] {
			t.Errorf("expected the LegacyCluster set, got %v", got.Paths)
		}
		if set["spec.crossplane"] {
			t.Error("LegacyCluster does not get spec.crossplane")
		}
	})

	t.Run("indeterminate scope yields the union, marked uncertain", func(t *testing.T) {
		// An unrecognized apiVersion is the case the resolver refuses to
		// guess at; a plain v2 manifest with no scope resolves to
		// Namespaced from its own apiVersion and is not uncertain at all.
		xrd := scopeXRD("", false, false)
		xrd.SetAPIVersion("apiextensions.crossplane.io/v99")
		got := New(xrd).PlatformInjectedPaths()
		if !got.Uncertain {
			t.Fatal("an XRD with no scope and no claim signal must degrade to warnings")
		}
		if !strings.Contains(got.UncertainReason, "scope could not be determined") {
			t.Errorf("UncertainReason should carry the resolver's own reason: %q", got.UncertainReason)
		}
		set := pathSet(got.Paths)
		if !set["spec.crossplane"] || !set["spec.compositionRef"] {
			t.Errorf("expected the union of every scope's set, got %v", got.Paths)
		}
	})
}

// TestSource_ImplementsPlatformAwareSource pins the opt-in seam: Analyze
// type asserts for this interface, so losing the method would silently
// disable both checks everywhere rather than failing to build.
func TestSource_ImplementsPlatformAwareSource(t *testing.T) {
	var _ engine.PlatformAwareSource = (*Source)(nil)
}
