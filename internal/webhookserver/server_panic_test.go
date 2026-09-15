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

	"github.com/prometheus/client_golang/prometheus/testutil"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// panickingRegistry returns a registry whose entry panics during
// conversion: a compiled plan holding a nil Op, so executing it is a nil
// interface call.
//
// A nil *Plan will not do — the engine checks for that and returns an
// ordinary error, which is correct of it and useless here. The state below
// is only reachable through a bug in Compile, which is exactly the class of
// event this path exists to report.
func panickingRegistry(target string) *Registry {
	reg := NewRegistry()
	reg.Set(target, &CompiledEntry{
		Router: &engine.Router{Hub: "v2", Plans: map[string]*engine.Plan{
			"v1": {HubVersion: "v2", SpokeVersion: "v1", SpokeToHub: []engine.Op{nil}},
		}},
	})
	return reg
}

func conversionBody(uid string) []byte {
	obj := map[string]any{"apiVersion": "example.org/v1", "kind": "Foo", "metadata": map[string]any{"name": "x"}}
	raw, _ := json.Marshal(obj)
	review := extv1.ConversionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "apiextensions.k8s.io/v1", Kind: "ConversionReview"},
		Request: &extv1.ConversionRequest{
			UID: apitypes.UID(uid), DesiredAPIVersion: "example.org/v2",
			Objects: []runtime.RawExtension{{Raw: raw}},
		},
	}
	body, _ := json.Marshal(review)
	return body
}

func TestHandleConvert_PanicPreservesTheRequestUID(t *testing.T) {
	const target = "xfoos.example.org"
	m := newTestMetrics()
	s := &Server{Registry: panickingRegistry(target), Metrics: m}

	req := httptest.NewRequest(http.MethodPost, "/convert/"+target, bytes.NewReader(conversionBody("uid-panic")))
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)

	var got extv1.ConversionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not a ConversionReview (%v): %s", err, rec.Body.String())
	}
	if got.Response == nil {
		t.Fatal("no response")
	}
	// Without the UID the apiserver discards the whole response and
	// replaces it with a generic mismatch error, so the "internal error"
	// message below never reaches anyone.
	if got.Response.UID != "uid-panic" {
		t.Errorf("UID = %q, want uid-panic", got.Response.UID)
	}
	if got.Response.Result.Status != metav1.StatusFailure {
		t.Errorf("status = %q, want Failure", got.Response.Result.Status)
	}
	if !strings.Contains(got.Response.Result.Message, "internal error") {
		t.Errorf("message = %q", got.Response.Result.Message)
	}
	if n := testutil.ToFloat64(m.PanicsTotal.WithLabelValues(target)); n != 1 {
		t.Errorf("panic counter = %v, want 1 — a panic must be alertable, not just a histogram label", n)
	}
}

// A panic before the body is decoded has no request to match, so an empty
// UID is the correct answer there — and the response must still be
// well-formed rather than a torn one.
func TestHandleConvert_PanicBeforeDecodeStillAnswers(t *testing.T) {
	s := &Server{Registry: NewRegistry(), Metrics: newTestMetrics()}
	// A body that is not JSON at all never reaches the decode of
	// review.Request, so reqUID stays empty by design.
	req := httptest.NewRequest(http.MethodPost, "/convert/x", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)

	if rec.Code != 400 {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}
