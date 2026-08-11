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
	"net/http/httptest"
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/vrabbi/declarative-conversion-operator/pkg/engine"
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
	s := &Server{Registry: registry, Metrics: NewMetrics(newTestRegisterer())}

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
