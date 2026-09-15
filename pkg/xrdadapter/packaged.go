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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// packageAPIGroup is the API group Crossplane's package manager types live
// in. The version is deliberately not matched: what matters is that
// *something* in pkg.crossplane.io owns this XRD, not which revision of
// that API it was written at.
const packageAPIGroup = "pkg.crossplane.io"

// PackageOwner identifies the ConfigurationRevision that established an
// XRD, when one did.
type PackageOwner struct {
	// Kind is the owning package-revision kind, normally
	// ConfigurationRevision.
	Kind string
	// Name is the revision's object name.
	Name string
	// APIVersion is the owner reference's apiVersion, kept verbatim for
	// the condition message.
	APIVersion string
}

// PackageManagedBy reports the ConfigurationRevision owning this XRD, if
// any. Ownership is the signal that Crossplane's package establisher
// re-writes this object on every revision reconcile with a full
// client.Update from the package contents — which strips spec.conversion
// and the operator's annotations outright, roughly hourly, silently.
//
// Owner references are the right signal because the establisher is what
// sets them: an XRD applied by hand has none, and one shipped in a
// Configuration always does.
func PackageManagedBy(xrd *unstructured.Unstructured) (PackageOwner, bool) {
	if xrd == nil {
		return PackageOwner{}, false
	}
	for _, ref := range xrd.GetOwnerReferences() {
		group := ref.APIVersion
		if i := strings.Index(group, "/"); i >= 0 {
			group = group[:i]
		}
		if group != packageAPIGroup {
			continue
		}
		if !strings.HasSuffix(ref.Kind, "Revision") {
			continue
		}
		return PackageOwner{Kind: ref.Kind, Name: ref.Name, APIVersion: ref.APIVersion}, true
	}
	return PackageOwner{}, false
}
