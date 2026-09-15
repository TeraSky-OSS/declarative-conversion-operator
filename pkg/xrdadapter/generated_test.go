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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestGeneratedCRDNames_CompositeOnly(t *testing.T) {
	for _, scope := range []string{"Namespaced", "Cluster", "LegacyCluster"} {
		t.Run(scope, func(t *testing.T) {
			got, err := GeneratedCRDNames(scopeXRD(scope, false, false))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("an XRD with no claimNames generates one CRD, got %d: %+v", len(got), got)
			}
			c := got[0]
			if c.Role != RoleComposite || c.Name != "xwidgets.example.org" || c.Plural != "xwidgets" || c.Kind != "XWidget" {
				t.Errorf("unexpected composite CRD: %+v", c)
			}
			// Only scope: Namespaced produces a namespaced composite.
			if want := scope == "Namespaced"; c.Namespaced != want {
				t.Errorf("Namespaced = %v, want %v for scope %s", c.Namespaced, want, scope)
			}
		})
	}
}

func TestGeneratedCRDNames_ClaimOfferingXRD(t *testing.T) {
	got, err := GeneratedCRDNames(scopeXRD("LegacyCluster", true, false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("a claim-offering XRD generates two CRDs, got %d: %+v", len(got), got)
	}

	composite, claim := got[0], got[1]
	if composite.Role != RoleComposite || composite.Name != "xwidgets.example.org" {
		t.Errorf("unexpected composite: %+v", composite)
	}
	// The composite under LegacyCluster is cluster-scoped while its claim
	// is namespaced. Listing code that assumes one scope for both is wrong
	// for exactly this shape.
	if composite.Namespaced {
		t.Error("a LegacyCluster composite is cluster-scoped")
	}
	if claim.Role != RoleClaim || claim.Name != "widgets.example.org" || claim.Plural != "widgets" || claim.Kind != "Widget" {
		t.Errorf("unexpected claim: %+v", claim)
	}
	if !claim.Namespaced {
		t.Error("a claim CRD is always namespace-scoped")
	}
	if claim.Group != composite.Group {
		t.Errorf("both CRDs share the XRD's group, got %q and %q", composite.Group, claim.Group)
	}
}

func TestGeneratedCRDNames_ClaimNamesWithoutScopeStillResolves(t *testing.T) {
	// claimNames proves LegacyCluster on its own, so a manifest that omits
	// spec.scope still gets both CRDs and a cluster-scoped composite.
	got, err := GeneratedCRDNames(scopeXRD("", true, false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected two CRDs, got %+v", got)
	}
	if got[0].Namespaced {
		t.Error("claimNames implies LegacyCluster, whose composite is cluster-scoped")
	}
}

func TestGeneratedCRDNames_Errors(t *testing.T) {
	cases := []struct {
		name string
		xrd  *unstructured.Unstructured
	}{
		{"nil", nil},
		{"no group", &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"names": map[string]any{"kind": "XWidget", "plural": "xwidgets"}},
		}}},
		{"no plural", &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"group": "example.org", "names": map[string]any{"kind": "XWidget"}},
		}}},
		{"no kind", &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"group": "example.org", "names": map[string]any{"plural": "xwidgets"}},
		}}},
		// Every one of these DECLARES claims, so returning the composite
		// alone would leave the claim CRD unresolved everywhere — the exact
		// blind spot this helper closes. Failing to resolve must not look
		// like having nothing to resolve.
		{"claimNames.plural without claimNames.kind", &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{
				"group":      "example.org",
				"names":      map[string]any{"kind": "XWidget", "plural": "xwidgets"},
				"claimNames": map[string]any{"plural": "widgets"},
			},
		}}},
		{"claimNames with no plural at all", &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{
				"group":      "example.org",
				"names":      map[string]any{"kind": "XWidget", "plural": "xwidgets"},
				"claimNames": map[string]any{"kind": "Widget"},
			},
		}}},
		{"claimNames with an empty plural", &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{
				"group":      "example.org",
				"names":      map[string]any{"kind": "XWidget", "plural": "xwidgets"},
				"claimNames": map[string]any{"kind": "Widget", "plural": ""},
			},
		}}},
		{"claimNames with a non-string plural", &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{
				"group":      "example.org",
				"names":      map[string]any{"kind": "XWidget", "plural": "xwidgets"},
				"claimNames": map[string]any{"kind": "Widget", "plural": []any{"widgets"}},
			},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := GeneratedCRDNames(tc.xrd); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// TestWriteGroupVersion pins a constraint that is invisible until a real
// apiserver rejects a write: reads are version-agnostic, but the v2 XRD
// schema has no claimNames field and REFUSES any write to an XRD that has
// one —
//
//	spec: Invalid value: Claims aren't supported in apiextensions.crossplane.io/v2
//
// so a claim-offering XRD can only be patched at v1. Found by the
// LegacyCluster e2e leg, where every reconcile failed with that error and
// the config never reached Applied.
func TestWriteGroupVersion(t *testing.T) {
	cases := []struct {
		name string
		xrd  *unstructured.Unstructured
		want string
	}{
		{"claim-offering XRD must be written at v1", scopeXRD("LegacyCluster", true, false), "apiextensions.crossplane.io/v1"},
		{"claimNames with no explicit scope still means v1", scopeXRD("", true, false), "apiextensions.crossplane.io/v1"},
		// A LegacyCluster XRD without claims is expressible at v2's schema
		// (the value is outside v2's enum, but the apiserver does not
		// re-validate an unchanged immutable field), so it needs no special
		// case — and defaulting everything legacy to v1 would drag XRDs
		// onto a deprecated API for no reason.
		{"LegacyCluster without claims stays on v2", scopeXRD("LegacyCluster", false, false), "apiextensions.crossplane.io/v2"},
		{"Namespaced", scopeXRD("Namespaced", false, false), "apiextensions.crossplane.io/v2"},
		{"Cluster", scopeXRD("Cluster", false, false), "apiextensions.crossplane.io/v2"},
		{"nil defaults to the current API", nil, "apiextensions.crossplane.io/v2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WriteGroupVersion(tc.xrd).String(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGeneratedCRDNames_MalformedClaimNamesIsAnError guards the same
// distinction on the resolution path. Returning the composite alone for a
// mistyped spec.claimNames would leave the claim CRD unresolved everywhere
// — unsampled by test --live, unpruned by migrate-storage, unchecked for
// propagation — which is the exact blind spot this helper exists to close.
func TestGeneratedCRDNames_MalformedClaimNamesIsAnError(t *testing.T) {
	xrd := scopeXRD("LegacyCluster", false, false)
	_ = unstructured.SetNestedField(xrd.Object, "widgets", "spec", "claimNames")

	got, err := GeneratedCRDNames(xrd)
	if err == nil {
		t.Fatalf("expected an error, got %+v", got)
	}
	if !strings.Contains(err.Error(), "malformed spec.claimNames") {
		t.Errorf("error should name the problem, got %v", err)
	}
}

// TestOffersClaims_KeyedOnPresenceNotCompleteness pins the counterpart:
// an XRD that declares claimNames badly still declares claims, so the
// claim's machinery names stay reserved. Keying this on completeness
// instead would quietly re-open the reservation gap for exactly the
// objects GeneratedCRDNames is reporting as errors.
func TestOffersClaims_KeyedOnPresenceNotCompleteness(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		want bool
	}{
		{"absent", map[string]any{}, false},
		{"complete", map[string]any{"claimNames": map[string]any{"kind": "Widget", "plural": "widgets"}}, true},
		{"incomplete", map[string]any{"claimNames": map[string]any{"kind": "Widget"}}, true},
		{"empty object", map[string]any{"claimNames": map[string]any{}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			xrd := &unstructured.Unstructured{Object: map[string]any{"spec": tc.spec}}
			if got := offersClaims(xrd); got != tc.want {
				t.Errorf("offersClaims = %v, want %v", got, tc.want)
			}
		})
	}
	if offersClaims(nil) {
		t.Error("a nil XRD offers no claims")
	}
}
