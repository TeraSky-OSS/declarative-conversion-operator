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
		{"claimNames.plural without claimNames.kind", &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{
				"group":      "example.org",
				"names":      map[string]any{"kind": "XWidget", "plural": "xwidgets"},
				"claimNames": map[string]any{"plural": "widgets"},
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
