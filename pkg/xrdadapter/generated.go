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

// GeneratedCRDRole distinguishes the two CRDs one XRD can produce.
type GeneratedCRDRole string

const (
	// RoleComposite is the composite resource CRD, {plural}.{group}. Every
	// XRD generates exactly one.
	RoleComposite GeneratedCRDRole = "composite"
	// RoleClaim is the claim CRD, {claimPlural}.{group}, which only a
	// LegacyCluster XRD with spec.claimNames generates. It is
	// namespace-scoped, carries the *same* spec.conversion, and is built
	// from the *same* authored openAPIV3Schema — so claim objects land on
	// the same compiled plan and already convert correctly. It is the
	// tooling that has been blind to it.
	RoleClaim GeneratedCRDRole = "claim"
)

// GeneratedCRD is one CRD Crossplane renders from an XRD.
type GeneratedCRD struct {
	// Role is composite or claim.
	Role GeneratedCRDRole
	// Name is the CRD's metadata.name, "{plural}.{group}".
	Name string
	// Group, Plural and Kind address the generated type.
	Group  string
	Plural string
	Kind   string
	// Namespaced reports the generated CRD's scope. Claims are always
	// namespaced; the composite is namespaced only under scope: Namespaced.
	// Callers that list objects must not assume one scope covers both.
	Namespaced bool
}

// GeneratedCRDNames returns every CRD Crossplane generates from this XRD:
// always the composite, plus the claim when spec.claimNames is present.
//
// Every path in this repository that resolved a target by spec.names.plural
// alone was blind to the claim CRD, which produced two concrete bugs on a
// claim-offering XRD: `test --live` sampled roughly half the objects that
// actually go through the webhook, and `migrate-storage
// --prune-stored-versions` pruned one of the two CRDs, so the old version
// still could not be dropped — the entire point of running it.
//
// spec.claimNames is not read directly: its presence is one of the signals
// ResolveScope uses, and using the resolver keeps "is there a claim CRD"
// and "which fields does Crossplane inject" answering to the same source.
func GeneratedCRDNames(xrd *unstructured.Unstructured) ([]GeneratedCRD, error) {
	if xrd == nil {
		return nil, fmt.Errorf("xrdadapter: no XRD object")
	}
	group, found, err := unstructured.NestedString(xrd.Object, "spec", "group")
	if err != nil || !found || group == "" {
		return nil, fmt.Errorf("xrdadapter: XRD %q is missing spec.group", xrd.GetName())
	}
	plural, found, err := unstructured.NestedString(xrd.Object, "spec", "names", "plural")
	if err != nil || !found || plural == "" {
		return nil, fmt.Errorf("xrdadapter: XRD %q is missing spec.names.plural", xrd.GetName())
	}
	kind, found, err := unstructured.NestedString(xrd.Object, "spec", "names", "kind")
	if err != nil || !found || kind == "" {
		return nil, fmt.Errorf("xrdadapter: XRD %q is missing spec.names.kind", xrd.GetName())
	}

	scope := ResolveScope(xrd)
	out := []GeneratedCRD{{
		Role:   RoleComposite,
		Name:   plural + "." + group,
		Group:  group,
		Plural: plural,
		Kind:   kind,
		// Only scope: Namespaced produces a namespaced composite. Cluster
		// and LegacyCluster composites are cluster-scoped, and an
		// indeterminate scope is not a reason to guess "namespaced".
		Namespaced: scope.Scope == ScopeNamespaced,
	}}

	// Only an ABSENT spec.claimNames means "no claims". Anything else —
	// malformed, or present but missing a name — is an error, because
	// silently returning the composite alone leaves the claim CRD
	// unresolved everywhere: unsampled by test --live, unpruned by
	// migrate-storage, unchecked for propagation. That is precisely the
	// blind spot GeneratedCRDNames exists to close, so failing to resolve
	// must never look like having nothing to resolve.
	claimNames, found, err := unstructured.NestedMap(xrd.Object, "spec", "claimNames")
	if err != nil {
		return nil, fmt.Errorf("xrdadapter: XRD %q has a malformed spec.claimNames: %w", xrd.GetName(), err)
	}
	if !found {
		return out, nil
	}

	claimPlural, _, err := unstructured.NestedString(claimNames, "plural")
	if err != nil || claimPlural == "" {
		return nil, fmt.Errorf("xrdadapter: XRD %q declares spec.claimNames but no usable spec.claimNames.plural; it generates a claim CRD this operator cannot address", xrd.GetName())
	}
	claimKind, _, err := unstructured.NestedString(claimNames, "kind")
	if err != nil || claimKind == "" {
		return nil, fmt.Errorf("xrdadapter: XRD %q declares spec.claimNames but no usable spec.claimNames.kind", xrd.GetName())
	}
	out = append(out, GeneratedCRD{
		Role:       RoleClaim,
		Name:       claimPlural + "." + group,
		Group:      group,
		Plural:     claimPlural,
		Kind:       claimKind,
		Namespaced: true,
	})
	return out, nil
}
