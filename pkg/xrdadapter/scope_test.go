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

// scopeXRD builds a minimal XRD. A scope of "" omits spec.scope entirely,
// which is the shape a Crossplane 1.x cluster leaves behind after an
// upgrade and the whole reason this resolver exists.
func scopeXRD(scope string, claimNames bool, connectionSecretKeys bool) *unstructured.Unstructured {
	spec := map[string]any{
		"group": "example.org",
		"names": map[string]any{"kind": "XWidget", "plural": "xwidgets"},
	}
	if scope != "" {
		spec["scope"] = scope
	}
	if claimNames {
		spec["claimNames"] = map[string]any{"kind": "Widget", "plural": "widgets"}
	}
	if connectionSecretKeys {
		spec["connectionSecretKeys"] = []any{"username", "password"}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.crossplane.io/v2",
		"kind":       "CompositeResourceDefinition",
		"metadata":   map[string]any{"name": "xwidgets.example.org"},
		"spec":       spec,
	}}
}

// The defaulting concern this resolver was built for was checked against
// a real cluster before any of it was written — kind v1.35.0, Crossplane
// v2.4.0 (xpkg.crossplane.io/crossplane/crossplane:v2.4.0):
//
//   - The XRD CRD serves v1 (storage) and v2, with spec.conversion.strategy
//     None, exactly as the proposal described.
//   - v1 declares spec.scope default LegacyCluster, enum
//     [LegacyCluster, Namespaced, Cluster]; v2 declares default Namespaced,
//     enum [Namespaced, Cluster]. The defaults do differ.
//   - BUT: applying an XRD at v1 with spec.scope omitted reads back as
//     LegacyCluster at BOTH v1 and v2. Structural-schema defaulting runs on
//     the WRITE path and the defaulted value is persisted, so the stored
//     object carries an explicit scope and neither read has an absent field
//     left to default. (Reading at v2 returns "LegacyCluster" even though
//     it is outside v2's enum, which confirms the value is passed straight
//     through rather than re-derived.)
//
// So a live XRD's spec.scope is authoritative, and the mis-defaulting
// hazard does not exist on the read path. What remains is the offline
// case: a hand-written XRD YAML that omits spec.scope has never been
// through admission, and which default it will get depends on the API
// version it is applied at. That is the case these tests pin.

func TestResolveScope(t *testing.T) {
	cases := []struct {
		name      string
		xrd       *unstructured.Unstructured
		wantScope Scope
		wantConf  Confidence
		reasonHas string
	}{
		{
			name:      "explicit LegacyCluster corroborated by claimNames",
			xrd:       scopeXRD("LegacyCluster", true, false),
			wantScope: ScopeLegacyCluster, wantConf: ConfidenceHigh,
			reasonHas: "spec.claimNames",
		},
		{
			name:      "explicit LegacyCluster without claims",
			xrd:       scopeXRD("LegacyCluster", false, false),
			wantScope: ScopeLegacyCluster, wantConf: ConfidenceHigh,
		},
		{
			// A live read is authoritative: whatever is stored was either
			// authored or defaulted at write time, and both are answers.
			name:      "explicit Namespaced",
			xrd:       scopeXRD("Namespaced", false, false),
			wantScope: ScopeNamespaced, wantConf: ConfidenceHigh,
		},
		{
			name:      "explicit Cluster",
			xrd:       scopeXRD("Cluster", false, false),
			wantScope: ScopeCluster, wantConf: ConfidenceHigh,
		},
		{
			// Not a state a cluster can hold — Crossplane's CEL rule
			// rejects it — so it is a hand-written file whose halves
			// disagree, and claimNames is the half that cannot be a
			// default.
			name:      "claimNames outranks a contradicting scope",
			xrd:       scopeXRD("Namespaced", true, false),
			wantScope: ScopeLegacyCluster, wantConf: ConfidenceInferred,
			reasonHas: "can only exist on a LegacyCluster XRD",
		},
		{
			name:      "claimNames outranks Cluster too",
			xrd:       scopeXRD("Cluster", true, false),
			wantScope: ScopeLegacyCluster, wantConf: ConfidenceInferred,
		},
		{
			name:      "connectionSecretKeys is a legacy signal in its own right",
			xrd:       scopeXRD("", false, true),
			wantScope: ScopeLegacyCluster, wantConf: ConfidenceInferred,
			reasonHas: "spec.connectionSecretKeys",
		},
		{
			name:      "both legacy signals are named in the reason",
			xrd:       scopeXRD("", true, true),
			wantScope: ScopeLegacyCluster, wantConf: ConfidenceInferred,
			reasonHas: "spec.claimNames and spec.connectionSecretKeys",
		},
		{
			// The offline case this resolver exists for.
			name:      "absent scope with no signal is indeterminate",
			xrd:       scopeXRD("", false, false),
			wantScope: ScopeIndeterminate, wantConf: ConfidenceNone,
			reasonHas: "which version this manifest is applied at",
		},
		{
			name:      "unrecognized scope with no signal is indeterminate",
			xrd:       scopeXRD("Galactic", false, false),
			wantScope: ScopeIndeterminate, wantConf: ConfidenceNone,
		},
		{
			name:      "nil object",
			xrd:       nil,
			wantScope: ScopeIndeterminate, wantConf: ConfidenceNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveScope(tc.xrd)
			if got.Scope != tc.wantScope {
				t.Errorf("scope = %q, want %q (reason: %s)", got.Scope, tc.wantScope, got.Reason)
			}
			if got.Confidence != tc.wantConf {
				t.Errorf("confidence = %q, want %q (reason: %s)", got.Confidence, tc.wantConf, got.Reason)
			}
			if got.Reason == "" {
				t.Error("Reason must never be empty — callers surface it verbatim")
			}
			if tc.reasonHas != "" && !strings.Contains(got.Reason, tc.reasonHas) {
				t.Errorf("reason %q does not mention %q", got.Reason, tc.reasonHas)
			}
			if got.Indeterminate() != (tc.wantScope == ScopeIndeterminate) {
				t.Errorf("Indeterminate() = %v for scope %q", got.Indeterminate(), got.Scope)
			}
		})
	}
}
