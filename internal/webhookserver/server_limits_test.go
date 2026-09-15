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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// oversizeReview builds a real ConversionReview whose encoded form exceeds
// n bytes, with the UID in its natural position.
func oversizeReview(uid types.UID, n int) []byte {
	obj := map[string]any{
		"apiVersion": "example.org/v1",
		"kind":       "Foo",
		"metadata":   map[string]any{"name": "big"},
		"spec":       map[string]any{"blob": strings.Repeat("x", n)},
	}
	raw, _ := json.Marshal(obj)
	review := extv1.ConversionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "apiextensions.k8s.io/v1", Kind: "ConversionReview"},
		Request: &extv1.ConversionRequest{
			UID: uid, DesiredAPIVersion: "example.org/v2",
			Objects: []runtime.RawExtension{{Raw: raw}},
		},
	}
	body, _ := json.Marshal(review)
	return body
}

// An oversized body must come back as a ConversionReview failure carrying
// the request's own UID. A bare HTTP 413 reaches the user as "conversion
// webhook returned invalid response" with nothing about size in it, and a
// ConversionReview with the wrong UID is discarded by the apiserver for the
// same effect.
func TestHandleConvert_OversizeBodyIsAWellFormedFailure(t *testing.T) {
	s := &Server{Registry: NewRegistry(), Metrics: newTestMetrics(), MaxRequestBytes: 4096}
	body := oversizeReview("uid-oversize", 32*1024)
	if int64(len(body)) <= s.MaxRequestBytes {
		t.Fatalf("fixture is not actually oversized: %d bytes", len(body))
	}

	req := httptest.NewRequest(http.MethodPost, "/convert/xfoos.example.org", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)

	var got extv1.ConversionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not a ConversionReview (%v): %s", err, rec.Body.String())
	}
	if got.Response == nil {
		t.Fatal("no response in the ConversionReview")
	}
	if got.Response.UID != "uid-oversize" {
		t.Errorf("UID = %q, want uid-oversize — the apiserver discards a mismatched response, message and all", got.Response.UID)
	}
	if got.Response.Result.Status != metav1.StatusFailure {
		t.Errorf("status = %q, want Failure", got.Response.Result.Status)
	}
	if !strings.Contains(got.Response.Result.Message, "--max-request-bytes") {
		t.Errorf("message must say which knob to turn: %q", got.Response.Result.Message)
	}
}

func TestSniffRequestUID(t *testing.T) {
	full := oversizeReview("uid-1234", 64)
	cases := []struct {
		name  string
		input []byte
		want  types.UID
	}{
		{"whole body", full, "uid-1234"},
		// The realistic case: the apiserver emits uid first, so a short
		// prefix still contains it.
		{"truncated after the uid", full[:min(len(full), 200)], "uid-1234"},
		{"truncated before the uid", full[:20], ""},
		{"not a conversion review", []byte(`{"foo":"bar"}`), ""},
		{"empty", nil, ""},
		// A UID cut in half would still mismatch, but would look
		// deliberate — better to send none.
		{"uid value truncated mid-string", []byte(`{"request":{"uid":"uid-12`), ""},
		// "uid" must belong to the request, not to some earlier object.
		{"uid before request", []byte(`{"uid":"decoy","request":{}}`), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sniffRequestUID(tc.input); got != tc.want {
				t.Errorf("sniffRequestUID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrefixRecorder_KeepsOnlyThePrefix(t *testing.T) {
	p := &prefixRecorder{limit: 10}
	n, err := p.Write([]byte("abcdefghijklmnop"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A short write would surface as an error on the TeeReader's read side
	// and turn an oversize body into a decode error instead.
	if n != 16 {
		t.Errorf("Write reported %d bytes, must report the full %d", n, 16)
	}
	if string(p.Bytes()) != "abcdefghij" {
		t.Errorf("kept %q", p.Bytes())
	}
	if _, err := p.Write([]byte("more")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(p.Bytes()) != "abcdefghij" {
		t.Errorf("wrote past the limit: %q", p.Bytes())
	}
}

// A body under the limit must be unaffected — the MaxBytesReader wrapping
// is on the hot path of every conversion.
func TestHandleConvert_UnderTheLimitIsUntouched(t *testing.T) {
	s := &Server{Registry: NewRegistry(), Metrics: newTestMetrics(), MaxRequestBytes: 1 << 20}
	body := oversizeReview("uid-small", 16)
	req := httptest.NewRequest(http.MethodPost, "/convert/xfoos.example.org", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)

	var got extv1.ConversionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	// Not registered, so it fails — but it must fail for that reason, with
	// the decoded UID, not for being too large.
	if got.Response.UID != "uid-small" {
		t.Errorf("UID = %q", got.Response.UID)
	}
	if strings.Contains(got.Response.Result.Message, "max-request-bytes") {
		t.Errorf("a small body was rejected as oversized: %q", got.Response.Result.Message)
	}
}

func TestHandleConvert_RequestDeadlineIsHonoured(t *testing.T) {
	// The point is that the loop checks the deadline at all, not how fast a
	// conversion is. An already-cancelled request context makes that
	// deterministic: the timeout context is created inside handleConvert, so
	// a one-nanosecond timer would be racing the first object and could lose,
	// passing the test for the wrong reason. Cancellation is inherited.
	s := &Server{Registry: NewRegistry(), Metrics: newTestMetrics()}
	s.Registry.Set("xfoos.example.org", &CompiledEntry{
		Router: &engine.Router{Hub: "v2", Plans: map[string]*engine.Plan{
			"v1": {HubVersion: "v2", SpokeVersion: "v1"},
		}},
	})

	body := oversizeReview("uid-slow", 16)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/convert/xfoos.example.org", bytes.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.handleConvert(rec, req)

	var got extv1.ConversionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Response.UID != "uid-slow" {
		t.Errorf("UID = %q", got.Response.UID)
	}
	if got.Response.Result.Status != metav1.StatusFailure {
		t.Fatalf("expected a failure, got %+v", got.Response.Result)
	}
	if !strings.Contains(got.Response.Result.Message, "per-request budget") {
		t.Errorf("message should name the budget: %q", got.Response.Result.Message)
	}
}

func TestServerDefaults(t *testing.T) {
	s := &Server{}
	if s.maxRequestBytes() != DefaultMaxRequestBytes {
		t.Errorf("zero MaxRequestBytes must default, got %d", s.maxRequestBytes())
	}
	if s.requestTimeout() != DefaultRequestTimeout {
		t.Errorf("zero RequestTimeout must default, got %s", s.requestTimeout())
	}
	// The apiserver's conversion timeout is fixed at 30s. Giving up before
	// it does turns a precise timeout report into an opaque connection
	// error.
	if DefaultReadTimeout <= 30*time.Second || DefaultWriteTimeout <= 30*time.Second {
		t.Errorf("read/write timeouts (%s/%s) must exceed the apiserver's fixed 30s conversion timeout", DefaultReadTimeout, DefaultWriteTimeout)
	}
	if DefaultRequestTimeout > DefaultWriteTimeout {
		t.Errorf("the per-request budget (%s) must fit inside the write timeout (%s), or the response is truncated instead of delivered", DefaultRequestTimeout, DefaultWriteTimeout)
	}
	if (&Server{MaxRequestBytes: -1}).maxRequestBytes() != -1 {
		t.Error("a negative MaxRequestBytes must disable the cap, not default it")
	}
}

// A client that opens a connection and dribbles headers must be
// disconnected rather than holding a worker forever. This exercises the
// real net/http server with the real constants, because ReadHeaderTimeout
// is a property of the listener, not of the handler.
func TestReadHeaderTimeout_DisconnectsASlowClient(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Scaled down from DefaultReadHeaderTimeout so the test is fast; the
	// property under test is that the field is honoured at all.
	srv.Config.ReadHeaderTimeout = 200 * time.Millisecond
	srv.Config.MaxHeaderBytes = DefaultMaxHeaderBytes
	srv.Start()
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// A request line and one header, then nothing — headers never end.
	if _, err := fmt.Fprint(conn, "POST /convert/x HTTP/1.1\r\nHost: x\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	// Either a 408 or a close is a pass; hanging until the test deadline is
	// the failure.
	if err != nil && !errors.Is(err, io.EOF) {
		var ne net.Error
		if ok := asNetError(err, &ne); ok && ne.Timeout() {
			t.Fatal("the server never disconnected a client that stopped mid-headers")
		}
		return
	}
	if n > 0 && !strings.Contains(string(buf[:n]), "408") {
		t.Errorf("unexpected response to a stalled client: %q", buf[:n])
	}
}

func asNetError(err error, target *net.Error) bool {
	var ne net.Error
	ok := errors.As(err, &ne)
	if ok {
		*target = ne
	}
	return ok
}
