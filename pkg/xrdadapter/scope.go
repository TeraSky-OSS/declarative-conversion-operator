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
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Scope is a Crossplane XRD's composite scope. It decides which fields
// Crossplane injects into the generated CRD and where they sit, and
// whether a claim CRD exists at all — so everything downstream of it is
// wrong if the scope is wrong.
type Scope string

const (
	// ScopeNamespaced and ScopeCluster put Crossplane's machinery fields
	// under spec.crossplane and generate no claim CRD.
	ScopeNamespaced Scope = "Namespaced"
	ScopeCluster    Scope = "Cluster"
	// ScopeLegacyCluster is the v1 compatibility layer inside Crossplane
	// 2.x: machinery fields sit directly under spec, and an XRD with
	// spec.claimNames generates a second, namespace-scoped claim CRD.
	ScopeLegacyCluster Scope = "LegacyCluster"
	// ScopeIndeterminate means the object carried no usable signal.
	// Callers must degrade rather than pick one — see Confidence.
	ScopeIndeterminate Scope = "Indeterminate"
)

// Confidence qualifies a resolved Scope.
//
// The worry this type was built for — that the XRD CRD serves v1 (storage,
// spec.scope defaulting to LegacyCluster) and v2 (defaulting to
// Namespaced) with strategy: None, so a stored XRD could read back with a
// different scope depending on the version asked — was checked against a
// real cluster and does NOT happen. Structural-schema defaulting is
// applied on the WRITE path and the defaulted value is persisted, so a
// stored XRD always carries an explicit spec.scope and both reads return
// it verbatim. See the test file for the measurement.
//
// What remains is the offline case, and it is the common one: a
// hand-written XRD YAML that simply omits spec.scope has never been
// through admission, so nothing has defaulted it and `convctl analyze
// --xrd ./xrd.yaml` genuinely cannot tell which scope the cluster will
// pick. That is what Indeterminate is for.
type Confidence string

const (
	// ConfidenceHigh: spec.scope carries an explicit value. On a live
	// object this is authoritative (defaulting happens at write time); in
	// a file it is the author's own declaration.
	ConfidenceHigh Confidence = "High"
	// ConfidenceInferred: spec.scope was absent, or disagreed with a
	// signal that can only mean LegacyCluster.
	ConfidenceInferred Confidence = "Inferred"
	// ConfidenceNone accompanies ScopeIndeterminate.
	ConfidenceNone Confidence = "None"
)

// ScopeResolution is the full answer, including the human-readable reason
// callers surface as a condition message or a convctl warning.
type ScopeResolution struct {
	Scope      Scope
	Confidence Confidence
	// Reason explains how the scope was arrived at, or why it could not
	// be. Always non-empty.
	Reason string
}

// Indeterminate reports whether the resolution failed to settle on a
// scope, which is the signal for callers to degrade (warn instead of
// error, check the union of every scope's injected set, and so on).
func (r ScopeResolution) Indeterminate() bool { return r.Scope == ScopeIndeterminate }

// ResolveScope derives an XRD's scope. It is the only place in this
// repository that reads spec.scope, so the cross-check below cannot be
// bypassed by a caller that reaches for the raw field.
//
// spec.claimNames is the signal that outranks spec.scope: Crossplane's own
// CEL rule on the v1 XRD schema is
//
//	self.scope == 'LegacyCluster' || !has(self.claimNames)
//
// and the v2 schema has no claimNames field at all, so its presence can
// only mean LegacyCluster. spec.connectionSecretKeys is gated the same
// way. Neither is ever produced by defaulting, so neither can be a
// leftover the way a scope value could be — and an author who writes
// claimNames into a file with no scope has said which scope they mean.
func ResolveScope(xrd *unstructured.Unstructured) ScopeResolution {
	if xrd == nil {
		return ScopeResolution{Scope: ScopeIndeterminate, Confidence: ConfidenceNone, Reason: "no XRD object to read scope from"}
	}

	declared, _, _ := unstructured.NestedString(xrd.Object, "spec", "scope")

	var legacySignals []string
	if _, found, _ := unstructured.NestedMap(xrd.Object, "spec", "claimNames"); found {
		legacySignals = append(legacySignals, "spec.claimNames")
	}
	if _, found, _ := unstructured.NestedSlice(xrd.Object, "spec", "connectionSecretKeys"); found {
		legacySignals = append(legacySignals, "spec.connectionSecretKeys")
	}
	signals := joinSignals(legacySignals)

	if len(legacySignals) > 0 {
		switch Scope(declared) {
		case ScopeLegacyCluster:
			return ScopeResolution{Scope: ScopeLegacyCluster, Confidence: ConfidenceHigh,
				Reason: fmt.Sprintf("spec.scope is LegacyCluster, corroborated by %s", signals)}
		case "":
			return ScopeResolution{Scope: ScopeLegacyCluster, Confidence: ConfidenceInferred,
				Reason: fmt.Sprintf("spec.scope is absent, but %s can only exist on a LegacyCluster XRD", signals)}
		default:
			// Not a state a live cluster can hold — Crossplane's CEL rule
			// rejects it — so this is a hand-written file whose two halves
			// disagree. claimNames is the one that cannot be a default.
			return ScopeResolution{Scope: ScopeLegacyCluster, Confidence: ConfidenceInferred,
				Reason: fmt.Sprintf("spec.scope says %q, but %s can only exist on a LegacyCluster XRD and Crossplane's own CEL rule would reject this combination; treating it as LegacyCluster", declared, signals)}
		}
	}

	switch Scope(declared) {
	case ScopeNamespaced, ScopeCluster, ScopeLegacyCluster:
		return ScopeResolution{Scope: Scope(declared), Confidence: ConfidenceHigh,
			Reason: fmt.Sprintf("spec.scope is explicitly %s", declared)}
	case "":
		// Offline only: anything that has been through admission carries a
		// persisted scope. v1 defaults an absent scope to LegacyCluster and
		// v2 to Namespaced — and which applies is decided by the version
		// the manifest is applied at, which a manifest *does* state, in its
		// own apiVersion. Use it.
		if defaulted, ok := scopeDefaultForAPIVersion(xrd.GetAPIVersion()); ok {
			return ScopeResolution{Scope: defaulted, Confidence: ConfidenceInferred,
				Reason: fmt.Sprintf("spec.scope is absent; %s defaults it to %s. Declare spec.scope explicitly to remove the dependence on which API version this is applied at",
					xrd.GetAPIVersion(), defaulted)}
		}
		return ScopeResolution{Scope: ScopeIndeterminate, Confidence: ConfidenceNone,
			Reason: fmt.Sprintf("spec.scope is absent, no claim signal is present, and apiVersion %q is not a recognized XRD API version; the apiserver defaults an absent scope to LegacyCluster at apiextensions.crossplane.io/v1 and to Namespaced at /v2, so the scope cannot be determined. Declare spec.scope explicitly", xrd.GetAPIVersion())}
	default:
		return ScopeResolution{Scope: ScopeIndeterminate, Confidence: ConfidenceNone,
			Reason: fmt.Sprintf("spec.scope has the unrecognized value %q", declared)}
	}
}

// scopeDefaultForAPIVersion returns what the apiserver would default an
// absent spec.scope to for a manifest written at this apiVersion. The two
// served XRD schemas differ here — v1 defaults LegacyCluster (its enum
// includes it), v2 defaults Namespaced (its enum does not) — so a manifest
// that omits spec.scope is only ambiguous if you ignore what it says it is.
//
// Anything else, including an empty apiVersion, is unrecognized: guessing
// from a version this code does not know about would be exactly the kind of
// silent assumption ResolveScope exists to avoid.
func scopeDefaultForAPIVersion(apiVersion string) (Scope, bool) {
	switch apiVersion {
	case LegacyGroupVersion.String():
		return ScopeLegacyCluster, true
	case GroupVersionKind.GroupVersion().String():
		return ScopeNamespaced, true
	default:
		return "", false
	}
}

func joinSignals(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	default:
		out := s[0]
		for _, x := range s[1 : len(s)-1] {
			out += ", " + x
		}
		return out + " and " + s[len(s)-1]
	}
}
