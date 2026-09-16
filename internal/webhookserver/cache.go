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

import (
	"encoding/json"
	"fmt"

	coordinationv1 "k8s.io/api/coordination/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// LastAppliedAnnotation is kubectl's copy of the whole object, stored
// inside the object. On a CRD with a large OpenAPI schema it roughly
// doubles the object's size, and nothing in this process ever reads it.
const LastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// CacheOptionsFromSelectorJSON builds controller-runtime cache options for
// the webhook-server replica.
//
// Two independent things happen here:
//
//   - A label selector, when one is given, restricts the
//     XRDConversionConfig and CRDConversionConfig informers *and* the
//     CustomResourceDefinition and CompositeResourceDefinition informers
//     that hold the schemas. Scoping only the configs — which is what this
//     did before — left the objects that dominate a replica's footprint
//     unbounded: on a mature Crossplane cluster the CRD informer alone is
//     500–1500 objects whose OpenAPI schemas are the bulk of their bytes.
//     Targets must therefore carry the label too; see
//     docs/operations/capacity.md.
//
//   - A transform, applied unconditionally, strips the parts of every
//     cached schema object this process provably never reads. This helps
//     every deployment, including the majority that never set a selector,
//     and it is the larger of the two wins on a typical cluster.
//
// An empty selector string leaves the cache unscoped (watch everything),
// matching the historical default. The transform still applies.
//
// enableXRDSupport and enableCRDSupport mirror the flags of the same name and
// are not optional. controller-runtime resolves every ByObject key through the
// RESTMapper when the manager is constructed, so naming
// CompositeResourceDefinition here on a cluster without Crossplane is not a
// harmless unused entry — it is `no matches for kind
// "CompositeResourceDefinition"` and the process exits. That is the same
// startup hazard the reconciler's watches already guard against, arriving by a
// different route.
func CacheOptionsFromSelectorJSON(selJSON string, enableXRDSupport, enableCRDSupport bool) (cache.Options, error) {
	opts := cache.Options{}

	var schemaObjects, configObjects []client.Object
	if enableXRDSupport {
		xrdObj := &unstructured.Unstructured{}
		xrdObj.SetGroupVersionKind(xrdadapter.GroupVersionKind)
		schemaObjects = append(schemaObjects, xrdObj)
		configObjects = append(configObjects, &teraskyv1alpha1.XRDConversionConfig{})
	}
	if enableCRDSupport {
		schemaObjects = append(schemaObjects, &extv1.CustomResourceDefinition{})
		configObjects = append(configObjects, &teraskyv1alpha1.CRDConversionConfig{})
	}

	selector, err := selectorFromJSON(selJSON)
	if err != nil {
		return opts, err
	}

	opts.ByObject = map[client.Object]cache.ByObject{}
	for _, obj := range schemaObjects {
		opts.ByObject[obj] = cache.ByObject{Label: selector, Transform: StripForCache}
	}
	for _, obj := range configObjects {
		opts.ByObject[obj] = cache.ByObject{Label: selector}
	}
	return opts, nil
}

// selectorFromJSON returns nil (meaning "everything") for an empty or
// empty-bodied selector, which is what cache.ByObject.Label expects.
func selectorFromJSON(selJSON string) (labels.Selector, error) {
	if selJSON == "" {
		// nil is the value cache.ByObject.Label expects for "everything";
		// labels.Everything() would be equivalent but would make an unset
		// selector indistinguishable from an explicitly empty one in the
		// tests that assert on it.
		return nil, nil //nolint:nilnil // a nil selector is what "watch everything" is spelled as
	}
	var ls metav1.LabelSelector
	if err := json.Unmarshal([]byte(selJSON), &ls); err != nil {
		return nil, fmt.Errorf("parse --cache-label-selector: %w", err)
	}
	if len(ls.MatchLabels) == 0 && len(ls.MatchExpressions) == 0 {
		return nil, nil //nolint:nilnil // an empty selector means "watch everything", same as an absent one
	}
	selector, err := metav1.LabelSelectorAsSelector(&ls)
	if err != nil {
		return nil, fmt.Errorf("compile --cache-label-selector: %w", err)
	}
	return selector, nil
}

// StripForCache drops the parts of a cached CRD or XRD that this process
// never reads, before the object is committed to the informer's store. The
// saving is real resident memory, not just wire bytes.
//
// What is dropped:
//
//   - metadata.managedFields — a full field-ownership tree per manager. On
//     a CRD that has been through a few Helm upgrades this is routinely the
//     single largest part of the object.
//   - the kubectl last-applied-configuration annotation — a verbatim copy
//     of the object embedded in the object.
//
// What is deliberately NOT dropped, though the obvious next step would be
// to: the schemas of versions no config targets. The cache is constructed
// before any config has been read, and configs are added and retargeted at
// runtime while informers are not re-scopable. A version pruned at cache
// build time would then be silently missing when a config later names it —
// and a conversion that reads a truncated schema does not fail, it returns
// wrong data. That is precisely the failure mode this operator exists to
// prevent, so the memory is the cheaper side of the trade.
//
// The object is mutated in place. Since client-go v1.27 a TransformFunc
// "sees the object before any other actor, and it is now safe to mutate the
// object in place instead of making a copy" (k8s.io/client-go
// tools/cache/delta_fifo.go). Deep-copying instead is not merely wasteful:
// measured on a cluster with 300 large CRDs it made the replica's working
// set *larger* than doing nothing at all, because the copy doubles the
// live heap during the initial LIST and the Go heap target never comes
// back down. See docs/operations/capacity.md.
//
// The transform is idempotent — stripping an already-stripped object is a
// no-op — which is the other half of what in-place mutation requires.
func StripForCache(obj any) (any, error) {
	switch o := obj.(type) {
	case *extv1.CustomResourceDefinition:
		stripObjectMeta(o)
		return o, nil
	case *unstructured.Unstructured:
		stripObjectMeta(o)
		return o, nil
	default:
		// Anything else (including cache.DeletedFinalStateUnknown
		// tombstones, whose contained object has already been transformed)
		// passes through untouched. Returning an error here would drop the
		// event.
		return obj, nil
	}
}

// stripObjectMeta is the part that is identical for typed and unstructured
// objects, expressed against the interface they share.
func stripObjectMeta(obj metav1.Object) {
	obj.SetManagedFields(nil)
	if ann := obj.GetAnnotations(); ann != nil {
		if _, ok := ann[LastAppliedAnnotation]; ok {
			delete(ann, LastAppliedAnnotation)
			obj.SetAnnotations(ann)
		}
	}
}

// Compile-time assertion that StripForCache still matches the signature
// cache.ByObject.Transform expects, so a future signature change fails the
// build rather than silently going uninstalled.
var _ toolscache.TransformFunc = StripForCache

// CountMatchingLabels reports how many label maps match sel. Used to show
// that a cacheSelector shrinks the set an informer would hold versus the
// unscoped (every object matches) default.
func CountMatchingLabels(all []labels.Set, sel labels.Selector) int {
	if sel == nil || sel.Empty() {
		return len(all)
	}
	n := 0
	for _, set := range all {
		if sel.Matches(set) {
			n++
		}
	}
	return n
}

// ClientOptions keeps the served-target Lease out of the cache.
//
// A replica writes exactly one Lease, its own, and reads it back only to
// build the next update. Caching that would cost a Lease informer, and a
// Lease informer is not a small thing to add: on any cluster there is
// already one per node in kube-node-lease, plus every leader election in
// every namespace. Read-through is a single GET every thirty seconds
// against an object this process wrote itself.
func ClientOptions() client.Options {
	return client.Options{
		Cache: &client.CacheOptions{
			DisableFor: []client.Object{&coordinationv1.Lease{}},
		},
	}
}
