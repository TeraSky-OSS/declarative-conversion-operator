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
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

func TestHandleConvert_NotRegistered_FailsClosed(t *testing.T) {
	s := &Server{Registry: NewRegistry()}
	review := extv1.ConversionReview{Request: &extv1.ConversionRequest{UID: "abc", DesiredAPIVersion: "example.org/v2"}}
	body, _ := json.Marshal(review)
	req := httptest.NewRequest("POST", "/convert/xfoos.example.org", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleConvert(rec, req)

	var got extv1.ConversionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Response.Result.Status != metav1.StatusFailure {
		t.Fatalf("expected a Failure result for an unregistered XRD, got %+v", got.Response.Result)
	}
	if got.Response.Result.Message == "" {
		t.Fatalf("expected a clear failure message")
	}
}

func TestHandleConvert_Success(t *testing.T) {
	hub := "v2"
	spoke := "v1"
	plan := &engine.Plan{
		HubVersion: hub, SpokeVersion: spoke,
		HubToSpoke: []engine.Op{}, // identity-only for this test: no ops means metadata passthrough only
		SpokeToHub: []engine.Op{},
	}
	registry := NewRegistry()
	registry.Set("xfoos.example.org", &CompiledEntry{
		Router: &engine.Router{Hub: hub, Plans: map[string]*engine.Plan{spoke: plan}},
	})
	s := &Server{Registry: registry, Metrics: newTestMetrics()}

	obj := map[string]any{"apiVersion": "example.org/v1", "kind": "Foo", "metadata": map[string]any{"name": "x"}}
	raw, _ := json.Marshal(obj)
	review := extv1.ConversionReview{Request: &extv1.ConversionRequest{
		UID: "abc", DesiredAPIVersion: "example.org/v2",
		Objects: []runtime.RawExtension{{Raw: raw}},
	}}
	body, _ := json.Marshal(review)
	req := httptest.NewRequest("POST", "/convert/xfoos.example.org", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleConvert(rec, req)

	var got extv1.ConversionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Response.Result.Status != metav1.StatusSuccess {
		t.Fatalf("expected Success, got %+v", got.Response.Result)
	}
	if len(got.Response.ConvertedObjects) != 1 {
		t.Fatalf("expected exactly one converted object, got %d", len(got.Response.ConvertedObjects))
	}
	var converted map[string]any
	_ = json.Unmarshal(got.Response.ConvertedObjects[0].Raw, &converted)
	if converted["apiVersion"] != "example.org/v2" {
		t.Fatalf("expected converted object's apiVersion to be rewritten to the desired version, got %v", converted["apiVersion"])
	}
	if converted["kind"] != "Foo" {
		t.Fatalf("expected converted object's kind to be preserved, got %v", converted["kind"])
	}
}

func TestHandleConvert_PartialObjectStillHasMetadata(t *testing.T) {
	hub := "v3"
	spoke := "v2"
	plan := &engine.Plan{
		HubVersion: hub, SpokeVersion: spoke,
		HubToSpoke: []engine.Op{}, SpokeToHub: []engine.Op{},
	}
	registry := NewRegistry()
	registry.Set("xwidgets.example.org", &CompiledEntry{
		Router: &engine.Router{Hub: hub, Plans: map[string]*engine.Plan{spoke: plan}},
	})
	s := &Server{Registry: registry, Metrics: newTestMetrics()}

	// Flux SSA prune sends a field-set fragment — often no metadata.
	obj := map[string]any{"apiVersion": "example.org/v2", "kind": "XWidget", "spec": map[string]any{"widgetName": "demo"}}
	raw, _ := json.Marshal(obj)
	review := extv1.ConversionReview{Request: &extv1.ConversionRequest{
		UID: "ssa", DesiredAPIVersion: "example.org/v3",
		Objects: []runtime.RawExtension{{Raw: raw}},
	}}
	body, _ := json.Marshal(review)
	req := httptest.NewRequest("POST", "/convert/xwidgets.example.org", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)

	var got extv1.ConversionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Response.Result.Status != metav1.StatusSuccess {
		t.Fatalf("expected Success, got %+v", got.Response.Result)
	}
	var converted map[string]any
	_ = json.Unmarshal(got.Response.ConvertedObjects[0].Raw, &converted)
	if converted["metadata"] == nil {
		t.Fatalf("apiserver rejects converted objects with no metadata, got %v", converted)
	}
}

func TestRegistry_RecordErrorPreservesRouter(t *testing.T) {
	r := NewRegistry()
	router := &engine.Router{Hub: "v2"}
	r.Set("xfoos.example.org", &CompiledEntry{Router: router, PlanHash: "good"})

	r.RecordError("xfoos.example.org", "schema drift detected")

	entry, ok := r.Get("xfoos.example.org")
	if !ok {
		t.Fatalf("expected entry to still exist")
	}
	if entry.Router != router {
		t.Fatalf("expected the last known-good Router to survive a recorded error, got a different/nil router")
	}
	if entry.LastError == "" {
		t.Fatalf("expected LastError to be recorded")
	}
	if entry.PlanHash != "good" {
		t.Fatalf("expected PlanHash to be preserved from the last good compile")
	}
}

func TestHandleConvert_WrongMethod_Rejected(t *testing.T) {
	s := &Server{Registry: NewRegistry()}
	req := httptest.NewRequest("GET", "/convert/xfoos.example.org", nil)
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleConvert_MalformedBody_BadRequest(t *testing.T) {
	s := &Server{Registry: NewRegistry()}
	req := httptest.NewRequest("POST", "/convert/xfoos.example.org", bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed JSON, got %d", rec.Code)
	}
}

func TestHandleConvert_MissingRequest_BadRequest(t *testing.T) {
	s := &Server{Registry: NewRegistry()}
	body, _ := json.Marshal(extv1.ConversionReview{}) // no .Request
	req := httptest.NewRequest("POST", "/convert/xfoos.example.org", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when .Request is nil, got %d", rec.Code)
	}
}

func TestHandleConvert_NoCompiledPlanForRequestedVersion_FailsClosed(t *testing.T) {
	hub, spoke := "v2", "v1"
	plan := &engine.Plan{HubVersion: hub, SpokeVersion: spoke, HubToSpoke: []engine.Op{}, SpokeToHub: []engine.Op{}}
	registry := NewRegistry()
	registry.Set("xfoos.example.org", &CompiledEntry{Router: &engine.Router{Hub: hub, Plans: map[string]*engine.Plan{spoke: plan}}})
	s := &Server{Registry: registry, Metrics: newTestMetrics()}

	obj := map[string]any{"apiVersion": "example.org/v1", "kind": "Foo", "metadata": map[string]any{"name": "x"}}
	raw, _ := json.Marshal(obj)
	review := extv1.ConversionReview{Request: &extv1.ConversionRequest{
		UID: "abc", DesiredAPIVersion: "example.org/v3", // v3 was never compiled
		Objects: []runtime.RawExtension{{Raw: raw}},
	}}
	body, _ := json.Marshal(review)
	req := httptest.NewRequest("POST", "/convert/xfoos.example.org", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)

	var got extv1.ConversionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Response.Result.Status != metav1.StatusFailure {
		t.Fatalf("expected a Failure result when the router has no plan for the requested version, got %+v", got.Response.Result)
	}
}

func TestHandleConvert_LossyConversion_IncrementsMetric(t *testing.T) {
	hub, spoke := "v2", "v1"
	plan := &engine.Plan{HubVersion: hub, SpokeVersion: spoke, HubToSpoke: []engine.Op{}, SpokeToHub: []engine.Op{}}
	registry := NewRegistry()
	registry.Set("xfoos.example.org", &CompiledEntry{
		Router:   &engine.Router{Hub: hub, Plans: map[string]*engine.Plan{spoke: plan}},
		Lossless: map[string]engine.LosslessVerdict{spoke: {HubToSpoke: false, SpokeToHub: true}},
	})
	metrics := newTestMetrics()
	s := &Server{Registry: registry, Metrics: metrics}

	obj := map[string]any{"apiVersion": "example.org/v2", "kind": "Foo", "metadata": map[string]any{"name": "x"}}
	raw, _ := json.Marshal(obj)
	review := extv1.ConversionReview{Request: &extv1.ConversionRequest{
		UID: "abc", DesiredAPIVersion: "example.org/v1", // hub -> spoke, the direction marked lossy above
		Objects: []runtime.RawExtension{{Raw: raw}},
	}}
	body, _ := json.Marshal(review)
	req := httptest.NewRequest("POST", "/convert/xfoos.example.org", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected the HTTP call itself to succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := testutil.ToFloat64(metrics.LossyTotal.WithLabelValues("xfoos.example.org", "hub_to_spoke")); got != 1 {
		t.Fatalf("expected the lossy-conversion counter to be incremented once, got %v", got)
	}
}

func TestHandleReadyz(t *testing.T) {
	s := &Server{Registry: NewRegistry(), Metrics: newTestMetrics()}
	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	s.handleReadyz(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 before SetReady(true), got %d", rec.Code)
	}

	s.SetReady(true)
	rec = httptest.NewRecorder()
	s.handleReadyz(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after SetReady(true), got %d", rec.Code)
	}
	if got := testutil.ToFloat64(s.Metrics.Ready); got != 1 {
		t.Fatalf("expected the Ready gauge to be 1, got %v", got)
	}

	s.SetReady(false)
	if got := testutil.ToFloat64(s.Metrics.Ready); got != 0 {
		t.Fatalf("expected the Ready gauge to be 0 after SetReady(false), got %v", got)
	}
}

func TestHandleDebugRegistry(t *testing.T) {
	registry := NewRegistry()
	registry.Set("compiled.example.org", &CompiledEntry{
		Router:   &engine.Router{Hub: "v2", Plans: map[string]*engine.Plan{"v1": {}}},
		PlanHash: "sha256:abc",
	})
	registry.RecordError("broken.example.org", "schema drift")
	s := &Server{Registry: registry}

	req := httptest.NewRequest("GET", "/debug/registry", nil)
	rec := httptest.NewRecorder()
	s.handleDebugRegistry(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var entries []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(entries), entries)
	}
	byXRD := map[string]map[string]any{}
	for _, e := range entries {
		byXRD[e["xrd"].(string)] = e
	}
	if !byXRD["compiled.example.org"]["ready"].(bool) {
		t.Fatalf("expected compiled.example.org to be ready")
	}
	if byXRD["broken.example.org"]["ready"].(bool) {
		t.Fatalf("expected broken.example.org (error-only placeholder) to be not ready")
	}
	if byXRD["broken.example.org"]["lastError"] != "schema drift" {
		t.Fatalf("unexpected lastError: %v", byXRD["broken.example.org"]["lastError"])
	}
}

func TestPlainMux_ExposesDedicatedRegistryMetrics(t *testing.T) {
	metrics := newTestMetrics()
	metrics.Ready.Set(1)
	metrics.RegistrySize.Set(3)
	s := &Server{Registry: NewRegistry(), Metrics: metrics}

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	s.PlainMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"dco_webhook_ready 1",
		"dco_webhook_registry_size 3",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected /metrics to contain %q; body starts with:\n%.500s", want, body)
		}
	}
}

func TestMetrics_HandlerWithoutGatherer(t *testing.T) {
	reg := prometheus.NewRegistry()
	wrapped := prometheus.WrapRegistererWith(prometheus.Labels{"pod": "a"}, reg)
	metrics := NewMetrics(wrapped, nil)
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	(&Server{Registry: NewRegistry(), Metrics: metrics}).PlainMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when gatherer is nil, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestMetrics_HandlerWithWrappedRegisterer(t *testing.T) {
	reg := prometheus.NewRegistry()
	wrapped := prometheus.WrapRegistererWith(prometheus.Labels{"pod": "a"}, reg)
	metrics := NewMetrics(wrapped, reg)
	metrics.Ready.Set(1)
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	(&Server{Registry: NewRegistry(), Metrics: metrics}).PlainMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "dco_webhook_ready") {
		t.Fatalf("expected dedicated metrics in body, got:\n%.500s", rec.Body.String())
	}
}
