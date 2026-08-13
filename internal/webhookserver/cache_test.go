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
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func byObjectFor(opts cache.Options, example client.Object) (cache.ByObject, bool) {
	want := reflect.TypeOf(example)
	for obj, by := range opts.ByObject {
		if reflect.TypeOf(obj) == want {
			return by, true
		}
	}
	return cache.ByObject{}, false
}

func TestCacheOptionsFromSelectorJSON_EmptyIsUnscoped(t *testing.T) {
	t.Parallel()
	opts, err := CacheOptionsFromSelectorJSON("")
	if err != nil {
		t.Fatal(err)
	}
	if opts.ByObject != nil {
		t.Fatalf("empty selector must leave ByObject unset, got %#v", opts.ByObject)
	}
}

func TestCacheOptionsFromSelectorJSON_EmptyObjectIsUnscoped(t *testing.T) {
	t.Parallel()
	opts, err := CacheOptionsFromSelectorJSON("{}")
	if err != nil {
		t.Fatal(err)
	}
	if opts.ByObject != nil {
		t.Fatalf("empty object must leave ByObject unset, got %#v", opts.ByObject)
	}
}

func TestCacheOptionsFromSelectorJSON_InvalidJSON(t *testing.T) {
	t.Parallel()
	if _, err := CacheOptionsFromSelectorJSON("{"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestCacheSelector_ReducesCachedObjectCount(t *testing.T) {
	t.Parallel()
	opts, err := CacheOptionsFromSelectorJSON(`{"matchLabels":{"tenant":"a"}}`)
	if err != nil {
		t.Fatal(err)
	}
	by, ok := byObjectFor(opts, &teraskyv1alpha1.XRDConversionConfig{})
	if !ok || by.Label == nil {
		t.Fatal("expected XRDConversionConfig ByObject label selector")
	}
	if _, ok := byObjectFor(opts, &teraskyv1alpha1.CRDConversionConfig{}); !ok {
		t.Fatal("expected CRDConversionConfig ByObject label selector")
	}

	all := make([]labels.Set, 0, 100)
	for i := 0; i < 95; i++ {
		all = append(all, labels.Set{"tenant": "other"})
	}
	for i := 0; i < 5; i++ {
		all = append(all, labels.Set{"tenant": "a"})
	}
	unscoped := CountMatchingLabels(all, labels.Everything())
	scoped := CountMatchingLabels(all, by.Label)
	if unscoped != 100 {
		t.Fatalf("unscoped count=%d, want 100", unscoped)
	}
	if scoped != 5 {
		t.Fatalf("scoped count=%d, want 5", scoped)
	}
}

func TestCacheSelector_LargeSyntheticReduction(t *testing.T) {
	t.Parallel()
	opts, err := CacheOptionsFromSelectorJSON(`{"matchLabels":{"tenant":"a"}}`)
	if err != nil {
		t.Fatal(err)
	}
	by, ok := byObjectFor(opts, &teraskyv1alpha1.XRDConversionConfig{})
	if !ok || by.Label == nil {
		t.Fatal("expected label selector")
	}

	const total, matching = 10000, 100
	all := make([]labels.Set, 0, total)
	for i := 0; i < total-matching; i++ {
		all = append(all, labels.Set{"tenant": "other"})
	}
	for i := 0; i < matching; i++ {
		all = append(all, labels.Set{"tenant": "a"})
	}
	unscoped := CountMatchingLabels(all, labels.Everything())
	scoped := CountMatchingLabels(all, by.Label)
	if unscoped != total || scoped != matching {
		t.Fatalf("unscoped=%d scoped=%d, want %d/%d", unscoped, scoped, total, matching)
	}
	// ByObject label selectors shrink the informer *store* (objects held),
	// not the number of watches (still one list/watch per GVK). Memory then
	// scales with matched objects; this test only asserts the match ratio.
}

func BenchmarkCountMatchingLabels(b *testing.B) {
	const total, matching = 10000, 100
	all := make([]labels.Set, 0, total)
	for i := 0; i < total-matching; i++ {
		all = append(all, labels.Set{"tenant": "other"})
	}
	for i := 0; i < matching; i++ {
		all = append(all, labels.Set{"tenant": "a"})
	}
	sel, err := labels.Parse("tenant=a")
	if err != nil {
		b.Fatal(err)
	}
	b.Run("unscoped", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if CountMatchingLabels(all, labels.Everything()) != total {
				b.Fatal("unexpected count")
			}
		}
	})
	b.Run("scoped", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if CountMatchingLabels(all, sel) != matching {
				b.Fatal("unexpected count")
			}
		}
	})
}

func TestCacheSelector_MatchExpressions(t *testing.T) {
	t.Parallel()
	raw := `{"matchExpressions":[{"key":"tenant","operator":"In","values":["a","b"]}]}`
	opts, err := CacheOptionsFromSelectorJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	by, ok := byObjectFor(opts, &teraskyv1alpha1.XRDConversionConfig{})
	if !ok || by.Label == nil {
		t.Fatal("expected XRDConversionConfig ByObject label selector")
	}
	sel := by.Label
	if !sel.Matches(labels.Set{"tenant": "b"}) {
		t.Fatal("expected tenant=b to match")
	}
	if sel.Matches(labels.Set{"tenant": "c"}) {
		t.Fatal("expected tenant=c not to match")
	}
}
