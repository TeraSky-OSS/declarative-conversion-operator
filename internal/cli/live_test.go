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

package cli

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func testXRD() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.crossplane.io/v2",
		"kind":       "CompositeResourceDefinition",
		"metadata":   map[string]any{"name": "xwidgets.e2e.example.org"},
		"spec": map[string]any{
			"group": "e2e.example.org",
			"names": map[string]any{"kind": "XWidget", "plural": "xwidgets"},
		},
	}}
}

func widget(namespace, name, version string, spec map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "e2e.example.org/" + version,
		"kind":       "XWidget",
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}}
	if namespace != "" {
		obj.SetNamespace(namespace)
	}
	return obj
}

// newFakeXWidgetClient builds a fake dynamic client with the GVR->ListKind
// mapping the fake client requires registered up front for every resource
// it will be asked to LIST — it panics otherwise, even for an empty list,
// since (unlike a real apiserver) it has no CRD/discovery info to infer
// the mapping from.
func newFakeXWidgetClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	gvr := schema.GroupVersionResource{Group: "e2e.example.org", Version: "v2", Resource: "xwidgets"}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "XWidgetList"}, objs...)
}

func TestFetchLiveSamples(t *testing.T) {
	xrd := testXRD()
	dyn := newFakeXWidgetClient(
		widget("default", "a", "v2", map[string]any{"storageGB": "1"}),
		widget("prod", "b", "v2", map[string]any{"storageGB": "2"}),
	)

	samples, err := FetchLiveSamples(context.Background(), dyn, xrd, "v2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("expected 2 samples, got %d: %+v", len(samples), samples)
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i].File < samples[j].File })
	if samples[0].File != "cluster:default/a" || samples[0].Version != "v2" {
		t.Fatalf("unexpected sample 0: %+v", samples[0])
	}
	if samples[1].File != "cluster:prod/b" || samples[1].Version != "v2" {
		t.Fatalf("unexpected sample 1: %+v", samples[1])
	}

	spec, ok := samples[0].Object["spec"].(map[string]any)
	if !ok || spec["storageGB"] != "1" {
		t.Fatalf("expected sample 0's spec to be preserved verbatim, got %#v", samples[0].Object["spec"])
	}
}

func TestFetchLiveSamples_ListsAtRequestedVersion(t *testing.T) {
	xrd := testXRD()
	dyn := newFakeXWidgetClient(widget("default", "a", "v2", nil))

	var requestedGVR schema.GroupVersionResource
	dyn.PrependReactor("list", "xwidgets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		requestedGVR = action.GetResource()
		return false, nil, nil // let the default reactor still handle it
	})

	if _, err := FetchLiveSamples(context.Background(), dyn, xrd, "v2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := schema.GroupVersionResource{Group: "e2e.example.org", Version: "v2", Resource: "xwidgets"}
	if requestedGVR != want {
		t.Fatalf("expected list against %v, got %v", want, requestedGVR)
	}
}

func TestFetchLiveSamples_EmptyCluster(t *testing.T) {
	xrd := testXRD()
	dyn := newFakeXWidgetClient()

	samples, err := FetchLiveSamples(context.Background(), dyn, xrd, "v2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(samples) != 0 {
		t.Fatalf("expected 0 samples, got %d", len(samples))
	}
}

func TestFetchLiveSamples_ListError(t *testing.T) {
	xrd := testXRD()
	dyn := newFakeXWidgetClient()
	dyn.PrependReactor("list", "xwidgets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("boom: RBAC forbidden")
	})

	if _, err := FetchLiveSamples(context.Background(), dyn, xrd, "v2"); err == nil {
		t.Fatalf("expected an error to propagate from a failed list")
	}
}

func TestXRDResourceInfo_MissingFields(t *testing.T) {
	cases := map[string]*unstructured.Unstructured{
		"missing group": {Object: map[string]any{
			"spec": map[string]any{"names": map[string]any{"plural": "xwidgets"}},
		}},
		"missing plural": {Object: map[string]any{
			"spec": map[string]any{"group": "e2e.example.org"},
		}},
		"empty spec": {Object: map[string]any{}},
	}
	for name, xrd := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := xrdResourceInfo(xrd); err == nil {
				t.Fatalf("expected an error for %s", name)
			}
		})
	}
}

func TestVersionFromAPIVersion(t *testing.T) {
	cases := map[string]string{
		"e2e.example.org/v2": "v2",
		"v1":                 "v1",
		"":                   "",
	}
	for in, want := range cases {
		if got := versionFromAPIVersion(in); got != want {
			t.Errorf("versionFromAPIVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestObjectLabel(t *testing.T) {
	if got := objectLabel(widget("", "a", "v2", nil)); got != "a" {
		t.Errorf("expected cluster-scoped label 'a', got %q", got)
	}
	if got := objectLabel(widget("default", "a", "v2", nil)); got != "default/a" {
		t.Errorf("expected namespaced label 'default/a', got %q", got)
	}
}
