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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// The properties Crossplane merges into every generated CRD, per scope.
//
// SOURCE OF TRUTH: crossplane-runtime/pkg/xcrd — schemas.go
// (CompositeResourceSpecProps, CompositeResourceClaimSpecProps,
// CompositeResourceStatusProps) and crd.go (genCrdVersion,
// ForCompositeResource, ForCompositeResourceClaim), read at commit 5b9c969.
//
// genCrdVersion copies the author's properties in first, then
// ForCompositeResource copies the injected ones over the top
// (maps.Copy(crdv.Schema.OpenAPIV3Schema.Properties["spec"].Properties,
// props)). So an authored declaration at any of these paths is silently
// replaced in the CRD the apiserver actually enforces, while the engine —
// which reads the *authored* schema — happily compiles rules against it.
//
// The same set is pinned against the engine's passthrough behaviour by
// pkg/engine/crossplane_injected_test.go. There is no status.crossplane in
// any scope.
var (
	// modernSpecInjected: Namespaced and Cluster nest every machinery
	// field under one spec.crossplane key, so a single subtree root covers
	// the lot.
	modernSpecInjected = []engine.FieldPath{
		{"spec", "crossplane"},
	}

	// legacySpecInjected: LegacyCluster keeps the v1 layout, with the
	// machinery fields directly under spec as siblings of the author's own
	// — plus claimRef and writeConnectionSecretToRef, which the modern
	// scopes do not have at all.
	legacySpecInjected = []engine.FieldPath{
		{"spec", "compositionRef"},
		{"spec", "compositionSelector"},
		{"spec", "compositionRevisionRef"},
		{"spec", "compositionRevisionSelector"},
		{"spec", "compositionUpdatePolicy"},
		{"spec", "resourceRefs"},
		{"spec", "claimRef"},
		{"spec", "writeConnectionSecretToRef"},
	}

	// claimSpecInjected is the claim CRD's machinery. It shares the
	// authored schema with the composite, so a rule written against
	// spec.foo is correct for claims — but its own injected names differ:
	// resourceRef is singular rather than the resourceRefs array, and
	// compositeDeletePolicy exists only here.
	claimSpecInjected = []engine.FieldPath{
		{"spec", "compositionRef"},
		{"spec", "compositionSelector"},
		{"spec", "compositionRevisionRef"},
		{"spec", "compositionRevisionSelector"},
		{"spec", "compositionUpdatePolicy"},
		{"spec", "compositeDeletePolicy"},
		{"spec", "resourceRef"},
		{"spec", "writeConnectionSecretToRef"},
	}

	// Every scope gets status.conditions. LegacyCluster (and its claims)
	// additionally get connectionDetails and claimConditionTypes.
	commonStatusInjected = []engine.FieldPath{
		{"status", "conditions"},
	}
	legacyStatusInjected = []engine.FieldPath{
		{"status", "conditions"},
		{"status", "connectionDetails"},
		{"status", "claimConditionTypes"},
	}
)

// InjectedPathsForScope returns the paths Crossplane overwrites for one
// scope. An unrecognized scope returns nil; callers that cannot determine
// the scope should use InjectedPathsUnion instead of guessing.
//
// offersClaims decides whether the claim CRD's own machinery names count.
// They only exist if the XRD actually generates a claim CRD: a
// LegacyCluster XRD with no spec.claimNames renders one CRD, so reserving
// spec.resourceRef and spec.compositeDeletePolicy on it would reject
// authored fields Crossplane is never going to overwrite.
func InjectedPathsForScope(s Scope, offersClaims bool) []engine.FieldPath {
	switch s {
	case ScopeNamespaced, ScopeCluster:
		return concatPaths(modernSpecInjected, commonStatusInjected)
	case ScopeLegacyCluster:
		if !offersClaims {
			return concatPaths(legacySpecInjected, legacyStatusInjected)
		}
		// The claim CRD carries the same authored schema and the same
		// conversion webhook, so one config covers both — and a name that
		// collides on either one is a problem. Union them.
		return concatPaths(legacySpecInjected, claimSpecInjected, legacyStatusInjected)
	default:
		return nil
	}
}

// InjectedPathsUnion is every scope's set at once — what to check when the
// scope could not be determined. It is deliberately a superset, which is
// why a caller using it must mark the result Uncertain so the diagnostics
// come out as warnings: a false error on an XRD that is in fact fine is
// worse than a warning on one that is not.
func InjectedPathsUnion() []engine.FieldPath {
	return concatPaths(modernSpecInjected, legacySpecInjected, claimSpecInjected, legacyStatusInjected)
}

// PlatformInjectedPaths implements engine.PlatformAwareSource, which is
// how pkg/engine learns about Crossplane's injected fields without knowing
// Crossplane exists: it type asserts for this method on the SchemaSource
// it was handed, so every existing caller picks the check up unchanged.
func (s *Source) PlatformInjectedPaths() engine.PlatformInjectedPaths {
	res := ResolveScope(s.XRD)
	if res.Indeterminate() {
		return engine.PlatformInjectedPaths{
			Platform:        "Crossplane",
			Paths:           InjectedPathsUnion(),
			Uncertain:       true,
			UncertainReason: "the XRD's scope could not be determined (" + res.Reason + ")",
		}
	}
	return engine.PlatformInjectedPaths{
		Platform: "Crossplane",
		Paths:    InjectedPathsForScope(res.Scope, offersClaims(s.XRD)),
	}
}

// offersClaims reports whether this XRD generates a claim CRD. Only then do
// the claim's own machinery names (spec.resourceRef,
// spec.compositeDeletePolicy) exist to be overwritten.
//
// Keyed on spec.claimNames being present at all, not on it being complete:
// an XRD that declares claimNames badly still declares claims, and treating
// it as claim-free here would quietly re-open the reservation gap while
// GeneratedCRDNames is busy reporting the same object as an error.
func offersClaims(xrd *unstructured.Unstructured) bool {
	if xrd == nil {
		return false
	}
	_, found, _ := unstructured.NestedMap(xrd.Object, "spec", "claimNames")
	return found
}

func concatPaths(sets ...[]engine.FieldPath) []engine.FieldPath {
	seen := map[string]bool{}
	var out []engine.FieldPath
	for _, set := range sets {
		for _, p := range set {
			key := p.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, p)
		}
	}
	return out
}
