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
	"strings"
	"testing"
	"time"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

func bigManagedFields(n int) []metav1.ManagedFieldsEntry {
	out := make([]metav1.ManagedFieldsEntry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, metav1.ManagedFieldsEntry{
			Manager:    "manager",
			Operation:  metav1.ManagedFieldsOperationApply,
			APIVersion: "apiextensions.k8s.io/v1",
			Time:       &metav1.Time{Time: time.Unix(0, 0)},
			FieldsType: "FieldsV1",
			FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:versions":{}}}`)},
		})
	}
	return out
}

func TestStripForCache_TypedCRD(t *testing.T) {
	in := &extv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:          "widgets.example.org",
			ManagedFields: bigManagedFields(4),
			Annotations: map[string]string{
				LastAppliedAnnotation: strings.Repeat("x", 4096),
				"keep-me":             "yes",
			},
		},
		Spec: extv1.CustomResourceDefinitionSpec{Group: "example.org"},
	}

	got, err := StripForCache(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out, ok := got.(*extv1.CustomResourceDefinition)
	if !ok {
		t.Fatalf("transform returned %T, not a CRD", got)
	}
	if len(out.ManagedFields) != 0 {
		t.Errorf("managedFields survived: %d entries", len(out.ManagedFields))
	}
	if _, present := out.Annotations[LastAppliedAnnotation]; present {
		t.Error("last-applied-configuration survived")
	}
	// Everything the engine reads has to be untouched — a transform that
	// drops a schema does not fail, it converts wrongly.
	if out.Annotations["keep-me"] != "yes" {
		t.Error("an unrelated annotation was dropped")
	}
	if out.Spec.Group != "example.org" {
		t.Error("spec was modified")
	}

	// Mutating in place is the point, not an accident: the transform is the
	// first actor to see the object, and copying instead measurably
	// increased the replica's working set.
	if out != in {
		t.Error("transform returned a copy; it is documented and measured to mutate in place")
	}

	// Idempotent, which is what in-place mutation requires: Replace() can
	// hand back an object that has already been through here.
	again, err := StripForCache(out)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	second, ok := again.(*extv1.CustomResourceDefinition)
	if !ok || len(second.ManagedFields) != 0 || second.Annotations["keep-me"] != "yes" {
		t.Errorf("transform is not idempotent: %+v", second)
	}
}

func TestStripForCache_UnstructuredXRD(t *testing.T) {
	in := &unstructured.Unstructured{}
	in.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	in.SetName("xwidgets.example.org")
	in.SetManagedFields(bigManagedFields(3))
	in.SetAnnotations(map[string]string{LastAppliedAnnotation: "{}", "keep-me": "yes"})
	_ = unstructured.SetNestedField(in.Object, "example.org", "spec", "group")

	got, err := StripForCache(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out, ok := got.(*unstructured.Unstructured)
	if !ok {
		t.Fatalf("transform returned %T", got)
	}
	if len(out.GetManagedFields()) != 0 {
		t.Error("managedFields survived on the XRD")
	}
	if _, present := out.GetAnnotations()[LastAppliedAnnotation]; present {
		t.Error("last-applied-configuration survived on the XRD")
	}
	if out.GetAnnotations()["keep-me"] != "yes" {
		t.Error("an unrelated annotation was dropped")
	}
	if group, _, _ := unstructured.NestedString(out.Object, "spec", "group"); group != "example.org" {
		t.Errorf("spec.group = %q, want example.org", group)
	}
	if out != in {
		t.Error("transform returned a copy; it is documented and measured to mutate in place")
	}
}

// A tombstone (or anything else) must pass through rather than be dropped:
// returning an error here loses a delete event, which would leave a stale
// conversion plan serving.
func TestStripForCache_PassesThroughUnknownTypes(t *testing.T) {
	type odd struct{ n int }
	in := &odd{n: 7}
	got, err := StripForCache(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != any(in) {
		t.Errorf("expected the input back unchanged, got %#v", got)
	}
}

// The regression this whole issue is about: the flag documented as the
// answer to webhook-server memory did not cover the objects that dominate
// it.
func TestCacheOptions_SchemaInformersAreScopedAndTransformed(t *testing.T) {
	xrdObj := &unstructured.Unstructured{}
	xrdObj.SetGroupVersionKind(xrdadapter.GroupVersionKind)

	opts, err := CacheOptionsFromSelectorJSON(`{"matchLabels":{"tenant":"a"}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range []client.Object{&extv1.CustomResourceDefinition{}, xrdObj} {
		by, ok := byObjectFor(opts, obj)
		if !ok {
			t.Errorf("%T has no cache config at all, so its informer is unscoped", obj)
			continue
		}
		if by.Label == nil {
			t.Errorf("%T informer is not label-scoped", obj)
		}
		if by.Transform == nil {
			t.Errorf("%T informer has no transform, so it caches managedFields", obj)
		}
	}
}

// The transform is the half that helps the majority of deployments, which
// never set a selector at all.
func TestCacheOptions_TransformAppliesWithoutASelector(t *testing.T) {
	opts, err := CacheOptionsFromSelectorJSON("")
	if err != nil {
		t.Fatal(err)
	}
	by, ok := byObjectFor(opts, &extv1.CustomResourceDefinition{})
	if !ok || by.Transform == nil {
		t.Fatal("CRD informer has no transform when no selector is set")
	}
}
