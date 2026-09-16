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

	"github.com/prometheus/client_golang/prometheus/testutil"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

func TestRouteLabel(t *testing.T) {
	for _, tc := range []struct{ from, to, want string }{
		{"v3", "v3", routeIdentity},
		{"v3", "v1", routeHubToSpoke},
		{"v1", "v3", routeSpokeToHub},
		{"v1", "v2", routeSpokeToSpoke},
	} {
		if got := routeLabel("v3", tc.from, tc.to); got != tc.want {
			t.Errorf("routeLabel(v3, %s, %s) = %q, want %q", tc.from, tc.to, got, tc.want)
		}
	}
}

// threeVersionServer is a hub plus two spokes, which is the smallest shape
// that can route spoke-to-spoke at all.
func threeVersionServer(t *testing.T, lossless map[string]engine.LosslessVerdict) (*Server, *Metrics) {
	t.Helper()
	const hub = "v3"
	plans := map[string]*engine.Plan{}
	for _, spoke := range []string{"v1", "v2"} {
		plans[spoke] = &engine.Plan{HubVersion: hub, SpokeVersion: spoke, HubToSpoke: []engine.Op{}, SpokeToHub: []engine.Op{}}
	}
	registry := NewRegistry()
	registry.Set("xfoos.example.org", &CompiledEntry{
		Router:   &engine.Router{Hub: hub, Plans: plans},
		Lossless: lossless,
	})
	metrics := newTestMetrics()
	return &Server{Registry: registry, Metrics: metrics}, metrics
}

func convertOne(t *testing.T, s *Server, from, to string) {
	t.Helper()
	obj := map[string]any{"apiVersion": "example.org/" + from, "kind": "Foo", "metadata": map[string]any{"name": "x"}}
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshaling the sample object: %v", err)
	}
	body, err := json.Marshal(extv1.ConversionReview{Request: &extv1.ConversionRequest{
		UID: "abc", DesiredAPIVersion: "example.org/" + to,
		Objects: []runtime.RawExtension{{Raw: raw}},
	}})
	if err != nil {
		t.Fatalf("marshaling the review: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/convert/xfoos.example.org", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s->%s: expected 200, got %d: %s", from, to, rec.Code, rec.Body.String())
	}
}

// The point of the label: "how much of this traffic is spoke-to-spoke?" is
// not answerable from from_version and to_version, because which version
// is the hub is a per-target fact that is not a label.
func TestHandleConvert_RouteLabelClassifiesEveryShape(t *testing.T) {
	s, metrics := threeVersionServer(t, nil)
	convertOne(t, s, "v3", "v1")
	convertOne(t, s, "v1", "v3")
	convertOne(t, s, "v1", "v2")
	convertOne(t, s, "v2", "v2")

	for _, tc := range []struct{ from, to, route string }{
		{"v3", "v1", routeHubToSpoke},
		{"v1", "v3", routeSpokeToHub},
		{"v1", "v2", routeSpokeToSpoke},
		{"v2", "v2", routeIdentity},
	} {
		got := testutil.ToFloat64(metrics.ObjectsTotal.WithLabelValues("xfoos.example.org", tc.from, tc.to, tc.route, "success"))
		if got != 1 {
			t.Errorf("%s->%s: route=%s counter = %v, want 1", tc.from, tc.to, tc.route, got)
		}
	}
}

// A spoke-to-spoke conversion is two hops through the hub, and each one
// can be lossy on its own. Counting neither — which the old hub-or-nothing
// test did — makes the lossy counter silently under-report exactly the
// traffic class that carries the most loss.
func TestHandleConvert_SpokeToSpokeCountsBothLossyHops(t *testing.T) {
	s, metrics := threeVersionServer(t, map[string]engine.LosslessVerdict{
		"v1": {HubToSpoke: true, SpokeToHub: false}, // v1->hub loses something
		"v2": {HubToSpoke: false, SpokeToHub: true}, // hub->v2 loses something
	})
	convertOne(t, s, "v1", "v2")

	if got := testutil.ToFloat64(metrics.LossyTotal.WithLabelValues("xfoos.example.org", "spoke_to_hub")); got != 1 {
		t.Errorf("inbound v1->hub hop: spoke_to_hub counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.LossyTotal.WithLabelValues("xfoos.example.org", "hub_to_spoke")); got != 1 {
		t.Errorf("outbound hub->v2 hop: hub_to_spoke counter = %v, want 1", got)
	}
}

// A conversion that failed delivered nothing: the object is never returned
// and never stored, so nothing observable lost anything. Counting it as a
// lossy conversion would double-signal a failure the objects and reviews
// counters already record as an error.
func TestHandleConvert_FailedConversionRecordsNoLoss(t *testing.T) {
	const hub = "v3"
	registry := NewRegistry()
	registry.Set("xfoos.example.org", &CompiledEntry{
		// v2 has no compiled plan, so routing v1 -> v2 fails on the
		// second hop, after the first one has already run.
		Router: &engine.Router{Hub: hub, Plans: map[string]*engine.Plan{
			"v1": {HubVersion: hub, SpokeVersion: "v1", HubToSpoke: []engine.Op{}, SpokeToHub: []engine.Op{}},
		}},
		Lossless: map[string]engine.LosslessVerdict{
			"v1": {HubToSpoke: true, SpokeToHub: false},
			"v2": {HubToSpoke: false, SpokeToHub: true},
		},
	})
	metrics := newTestMetrics()
	s := &Server{Registry: registry, Metrics: metrics}

	obj := map[string]any{"apiVersion": "example.org/v1", "kind": "Foo", "metadata": map[string]any{"name": "x"}}
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshaling the sample object: %v", err)
	}
	body, err := json.Marshal(extv1.ConversionReview{Request: &extv1.ConversionRequest{
		UID: "abc", DesiredAPIVersion: "example.org/v2",
		Objects: []runtime.RawExtension{{Raw: raw}},
	}})
	if err != nil {
		t.Fatalf("marshaling the review: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/convert/xfoos.example.org", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)

	var got extv1.ConversionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Response.Result.Status != metav1.StatusFailure {
		t.Fatalf("expected the conversion to fail, got %+v", got.Response.Result)
	}
	for _, direction := range []string{"spoke_to_hub", "hub_to_spoke"} {
		if n := testutil.ToFloat64(metrics.LossyTotal.WithLabelValues("xfoos.example.org", direction)); n != 0 {
			t.Errorf("%s counter = %v after a failed conversion, want 0", direction, n)
		}
	}
	if n := testutil.ToFloat64(metrics.ObjectsTotal.WithLabelValues("xfoos.example.org", "v1", "v2", routeSpokeToSpoke, "error")); n != 1 {
		t.Errorf("the failure should be counted as an error on the objects counter, got %v", n)
	}
}

// The mirror image: a spoke-to-spoke route whose two hops are both
// lossless must not be counted at all, or the fix above would just be a
// louder wrong answer.
func TestHandleConvert_SpokeToSpokeCountsNoLossWhenBothHopsAreLossless(t *testing.T) {
	s, metrics := threeVersionServer(t, map[string]engine.LosslessVerdict{
		"v1": {HubToSpoke: true, SpokeToHub: true},
		"v2": {HubToSpoke: true, SpokeToHub: true},
	})
	convertOne(t, s, "v1", "v2")

	for _, direction := range []string{"spoke_to_hub", "hub_to_spoke"} {
		if got := testutil.ToFloat64(metrics.LossyTotal.WithLabelValues("xfoos.example.org", direction)); got != 0 {
			t.Errorf("%s counter = %v, want 0", direction, got)
		}
	}
}
