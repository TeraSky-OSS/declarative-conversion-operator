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
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// threeVersionRegistry serves a hub (v3) with two spokes, so one review can
// legitimately carry objects arriving from different versions.
func threeVersionRegistry(target string) *Registry {
	reg := NewRegistry()
	reg.Set(target, &CompiledEntry{
		Router: &engine.Router{Hub: "v3", Plans: map[string]*engine.Plan{
			"v1": {HubVersion: "v3", SpokeVersion: "v1"},
			"v2": {HubVersion: "v3", SpokeVersion: "v2"},
		}},
	})
	return reg
}

func reviewFrom(uid string, desired string, apiVersions ...string) []byte {
	objs := make([]runtime.RawExtension, 0, len(apiVersions))
	for i, av := range apiVersions {
		raw, _ := json.Marshal(map[string]any{
			"apiVersion": av, "kind": "Foo",
			"metadata": map[string]any{"name": string(rune('a' + i))},
		})
		objs = append(objs, runtime.RawExtension{Raw: raw})
	}
	review := extv1.ConversionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "apiextensions.k8s.io/v1", Kind: "ConversionReview"},
		Request: &extv1.ConversionRequest{
			UID: apitypes.UID(uid), DesiredAPIVersion: desired, Objects: objs,
		},
	}
	body, _ := json.Marshal(review)
	return body
}

// histogramLabels returns the label-value sets that have an observation on
// the named histogram, as "k=v,k=v" strings.
func histogramLabels(t *testing.T, m *Metrics, name string) map[string]uint64 {
	t.Helper()
	families, err := m.gatherer.Gather()
	if err != nil {
		t.Fatalf("gathering: %v", err)
	}
	out := map[string]uint64{}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, metric := range f.GetMetric() {
			key := ""
			for _, l := range metric.GetLabel() {
				if key != "" {
					key += ","
				}
				key += l.GetName() + "=" + l.GetValue()
			}
			out[key] = metric.GetHistogram().GetSampleCount()
		}
	}
	return out
}

func TestHandleConvert_MixedDirectionBatch(t *testing.T) {
	const target = "xfoos.example.org"
	m := newTestMetrics()
	s := &Server{Registry: threeVersionRegistry(target), Metrics: m}

	body := reviewFrom("uid-mixed", "example.org/v3", "example.org/v1", "example.org/v2")
	req := httptest.NewRequest(http.MethodPost, "/convert/"+target, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)

	var got extv1.ConversionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Response.Result.Status != metav1.StatusSuccess {
		t.Fatalf("conversion failed: %s", got.Response.Result.Message)
	}

	// The review-level histogram must not claim the whole batch converted
	// v2->v3 just because that object happened to be last.
	review := histogramLabels(t, m, "dco_webhook_conversion_review_duration_seconds")
	wantReview := "direction=mixed,result=success,target=" + target
	if review[wantReview] != 1 {
		t.Errorf("review histogram labels = %v, want one observation at %q", review, wantReview)
	}
	for k := range review {
		if k != wantReview {
			t.Errorf("a mixed batch also landed on %q", k)
		}
	}

	// The per-object histogram is where the exact directions live.
	objects := histogramLabels(t, m, "dco_webhook_conversion_object_duration_seconds")
	for _, want := range []string{
		"direction=v1->v3,result=success,target=" + target,
		"direction=v2->v3,result=success,target=" + target,
	} {
		if objects[want] != 1 {
			t.Errorf("object histogram labels = %v, want one observation at %q", objects, want)
		}
	}

	batch := histogramLabels(t, m, "dco_webhook_conversion_batch_size")
	if batch["target="+target] != 1 {
		t.Errorf("batch-size histogram labels = %v", batch)
	}
	if sum := batchSum(t, m, target); sum != 2 {
		t.Errorf("batch-size sum = %v, want 2 objects", sum)
	}
}

func batchSum(t *testing.T, m *Metrics, target string) float64 {
	t.Helper()
	families, err := m.gatherer.Gather()
	if err != nil {
		t.Fatalf("gathering: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "dco_webhook_conversion_batch_size" {
			continue
		}
		for _, metric := range f.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "target" && l.GetValue() == target {
					return metric.GetHistogram().GetSampleSum()
				}
			}
		}
	}
	return -1
}

// A single-direction batch must keep the exact direction it always had —
// this change is additive, and an existing dashboard querying
// direction="v1->v3" has to keep working.
func TestHandleConvert_SingleDirectionBatchKeepsItsLabel(t *testing.T) {
	const target = "xfoos.example.org"
	m := newTestMetrics()
	s := &Server{Registry: threeVersionRegistry(target), Metrics: m}

	body := reviewFrom("uid-single", "example.org/v3", "example.org/v1", "example.org/v1")
	req := httptest.NewRequest(http.MethodPost, "/convert/"+target, bytes.NewReader(body))
	s.handleConvert(httptest.NewRecorder(), req)

	review := histogramLabels(t, m, "dco_webhook_conversion_review_duration_seconds")
	want := "direction=v1->v3,result=success,target=" + target
	if review[want] != 1 {
		t.Errorf("review histogram labels = %v, want %q", review, want)
	}
}

func TestDirectionTracker(t *testing.T) {
	cases := []struct {
		name string
		adds []string
		want string
	}{
		{"nothing inspected", nil, "unknown"},
		{"one direction", []string{"v1->v2"}, "v1->v2"},
		{"same direction twice", []string{"v1->v2", "v1->v2"}, "v1->v2"},
		{"two directions", []string{"v1->v3", "v2->v3"}, "mixed"},
		// Once mixed, always mixed: a third object matching the first must
		// not un-mix the batch.
		{"back to the first", []string{"v1->v3", "v2->v3", "v1->v3"}, "mixed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &directionTracker{label: "unknown"}
			for _, a := range tc.adds {
				d.add(a)
			}
			if d.label != tc.want {
				t.Errorf("label = %q, want %q", d.label, tc.want)
			}
		})
	}
}
