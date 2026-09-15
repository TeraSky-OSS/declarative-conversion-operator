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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPackageManagedBy(t *testing.T) {
	cases := []struct {
		name      string
		refs      []metav1.OwnerReference
		want      bool
		wantKind  string
		wantOwner string
	}{
		{
			name: "ConfigurationRevision",
			refs: []metav1.OwnerReference{
				{APIVersion: "pkg.crossplane.io/v1", Kind: "ConfigurationRevision", Name: "platform-abc123"},
			},
			want: true, wantKind: "ConfigurationRevision", wantOwner: "platform-abc123",
		},
		{
			// A Provider package can establish objects too, and the same
			// non-SSA Update path applies — so match any package revision
			// kind rather than hard-coding the Configuration case.
			name: "ProviderRevision",
			refs: []metav1.OwnerReference{
				{APIVersion: "pkg.crossplane.io/v1", Kind: "ProviderRevision", Name: "provider-aws-def456"},
			},
			want: true, wantKind: "ProviderRevision", wantOwner: "provider-aws-def456",
		},
		{
			// The version in the group is deliberately not matched: what
			// matters is that the package manager owns this object.
			name: "a future package API version still matches",
			refs: []metav1.OwnerReference{
				{APIVersion: "pkg.crossplane.io/v2beta1", Kind: "ConfigurationRevision", Name: "platform-xyz"},
			},
			want: true, wantKind: "ConfigurationRevision", wantOwner: "platform-xyz",
		},
		{name: "no owners", refs: nil, want: false},
		{
			name: "owned by something that is not the package manager",
			refs: []metav1.OwnerReference{
				{APIVersion: "apiextensions.crossplane.io/v1", Kind: "CompositeResourceDefinition", Name: "other"},
			},
			want: false,
		},
		{
			name: "a non-revision kind in the package group does not count",
			refs: []metav1.OwnerReference{
				{APIVersion: "pkg.crossplane.io/v1", Kind: "Configuration", Name: "platform"},
			},
			want: false,
		},
		{
			name: "found among several owners",
			refs: []metav1.OwnerReference{
				{APIVersion: "v1", Kind: "ConfigMap", Name: "unrelated"},
				{APIVersion: "pkg.crossplane.io/v1", Kind: "ConfigurationRevision", Name: "platform-abc123"},
			},
			want: true, wantKind: "ConfigurationRevision", wantOwner: "platform-abc123",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			xrd := scopeXRD("Namespaced", false, false)
			xrd.SetOwnerReferences(tc.refs)

			got, ok := PackageManagedBy(xrd)
			if ok != tc.want {
				t.Fatalf("managed = %v, want %v (got %+v)", ok, tc.want, got)
			}
			if !tc.want {
				return
			}
			if got.Kind != tc.wantKind || got.Name != tc.wantOwner {
				t.Errorf("owner = %+v, want kind %q name %q", got, tc.wantKind, tc.wantOwner)
			}
		})
	}

	t.Run("nil object", func(t *testing.T) {
		if _, ok := PackageManagedBy(nil); ok {
			t.Error("a nil XRD is not package-managed")
		}
	})
}
