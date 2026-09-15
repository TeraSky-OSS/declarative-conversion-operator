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
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/go-logr/logr"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

// Server is the webhook server's HTTP surface: the conversion endpoint on
// one (TLS) port, and health/metrics/debug on another (plain HTTP) port —
// matching the common pattern of not requiring the metrics scraper to
// present a client certificate.
type Server struct {
	Registry *Registry
	Metrics  *Metrics

	// MaxRequestBytes caps the ConversionReview body. Zero means
	// DefaultMaxRequestBytes; a negative value disables the cap, which is
	// only ever right in a test.
	MaxRequestBytes int64

	// RequestTimeout bounds how long one ConversionReview may occupy a
	// worker. Zero means DefaultRequestTimeout.
	RequestTimeout time.Duration

	ready atomic.Bool
}

// Timeouts and limits for the two HTTP servers this package is served by
// (cmd/webhook-server builds them; they live here so the handler and the
// listener cannot drift apart).
//
// The conversion endpoint sits directly in the apiserver's write path, so
// the failure mode being defended against is not "a slow client gets a bad
// experience" but "every write to every target this replica serves stops".
// A slow-loris client holding connections open, or one oversized body, is
// a denial of the write path.
const (
	// DefaultReadHeaderTimeout is the slow-loris defence: a client that
	// has not finished its headers by then is not going to.
	DefaultReadHeaderTimeout = 10 * time.Second

	// DefaultReadTimeout and DefaultWriteTimeout are deliberately longer
	// than the apiserver's own conversion timeout, which is fixed at 30s.
	// Being the one to give up first turns a slow conversion into a
	// connection error the apiserver cannot explain, instead of a timeout
	// it reports precisely.
	DefaultReadTimeout  = 35 * time.Second
	DefaultWriteTimeout = 35 * time.Second

	// DefaultIdleTimeout keeps the apiserver's keep-alive connections
	// alive across quiet periods — reconnecting on every write would add a
	// TLS handshake to the admission path — while still reaping abandoned
	// ones.
	DefaultIdleTimeout = 120 * time.Second

	// DefaultMaxHeaderBytes is net/http's own default, set explicitly so
	// that it is a decision rather than an accident.
	DefaultMaxHeaderBytes = 1 << 20 // 1 MiB

	// DefaultMaxRequestBytes bounds the ConversionReview body. A review
	// can legitimately carry a large batch of large objects — a LIST of
	// several thousand composites at a non-storage version is the usual
	// case — so the ceiling is generous. Operators with unusual objects
	// can raise it with --max-request-bytes.
	DefaultMaxRequestBytes int64 = 32 << 20 // 32 MiB

	// DefaultRequestTimeout bounds one review's time on a worker, just
	// inside DefaultWriteTimeout so the failure surfaces as a
	// ConversionReview the apiserver can report rather than a truncated
	// response.
	DefaultRequestTimeout = 30 * time.Second

	// uidSniffBytes is how much of an oversized body is retained in the
	// hope of recovering its request UID. The apiserver serializes
	// ConversionRequest with uid as its first field, so in practice the
	// UID is in the first hundred bytes; this is two orders of magnitude
	// of headroom and still a fixed, small allocation.
	uidSniffBytes = 8 << 10 // 8 KiB
)

// DefaultShutdownTimeout is how long in-flight ConversionReviews are given
// to finish after a termination signal. It must fit, together with the
// pod's preStop sleep, inside terminationGracePeriodSeconds — so it is
// defined once, in api/v1alpha1, where the ConversionWebhookServer
// validating webhook checks that inequality, and aliased here rather than
// restated.
const DefaultShutdownTimeout = teraskyv1alpha1.DefaultWebhookServerShutdownTimeout

func (s *Server) maxRequestBytes() int64 {
	if s.MaxRequestBytes == 0 {
		return DefaultMaxRequestBytes
	}
	return s.MaxRequestBytes
}

func (s *Server) requestTimeout() time.Duration {
	if s.RequestTimeout <= 0 {
		return DefaultRequestTimeout
	}
	return s.RequestTimeout
}

// SetReady flips the readiness gate. Call this only once InitialSync has
// completed — before that, the registry may not reflect every existing
// XRDConversionConfig yet, and a premature "ready" would let traffic in
// through a Service endpoint that isn't actually able to serve everything.
func (s *Server) SetReady(ready bool) {
	s.ready.Store(ready)
	if s.Metrics != nil {
		if ready {
			s.Metrics.Ready.Set(1)
		} else {
			s.Metrics.Ready.Set(0)
		}
	}
}

// ConversionMux returns the TLS-facing mux: just the conversion endpoint.
// Keeping this separate from the plain-HTTP mux means a misconfigured or
// slow metrics scrape can never affect the admission-critical path, and
// vice versa.
func (s *Server) ConversionMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/convert/", s.handleConvert)
	return mux
}

// PlainMux returns the plain-HTTP mux: health probes, metrics, and debug.
func (s *Server) PlainMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/debug/registry", s.handleDebugRegistry)
	if s.Metrics != nil {
		mux.Handle("/metrics", s.Metrics.Handler())
	} else {
		mux.Handle("/metrics", promhttp.Handler())
	}
	return mux
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.ready.Load() {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("registry not yet fully synced"))
}

func (s *Server) handleDebugRegistry(w http.ResponseWriter, r *http.Request) {
	type entryView struct {
		XRD         string   `json:"xrd"`
		Ready       bool     `json:"ready"`
		SpokeCount  int      `json:"spokeCount"`
		PlanHash    string   `json:"planHash,omitempty"`
		CompiledAt  string   `json:"compiledAt,omitempty"`
		LastError   string   `json:"lastError,omitempty"`
		LastErrorAt string   `json:"lastErrorAt,omitempty"`
		Versions    []string `json:"versions,omitempty"`
	}
	snap := s.Registry.Snapshot()
	out := make([]entryView, 0, len(snap))
	for xrd, e := range snap {
		v := entryView{XRD: xrd, Ready: e.Router != nil, LastError: e.LastError}
		if e.Router != nil {
			v.SpokeCount = len(e.Router.Plans)
			for spoke := range e.Router.Plans {
				v.Versions = append(v.Versions, spoke)
			}
			v.PlanHash = e.PlanHash
			v.CompiledAt = e.CompiledAt.Format(time.RFC3339)
		}
		if !e.LastErrorAt.IsZero() {
			v.LastErrorAt = e.LastErrorAt.Format(time.RFC3339)
		}
		out = append(out, v)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleConvert is the hot path: it's invoked by the Kubernetes API server
// for every request that needs an object converted between versions of an
// XRD this replica currently serves.
func (s *Server) handleConvert(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	xrdName := strings.TrimPrefix(r.URL.Path, "/convert/")
	// batchDirection accumulates the per-object directions so the
	// request-level metrics can say "mixed" instead of silently
	// attributing a whole batch to whichever object happened to be last.
	batchDirection := &directionTracker{label: "unknown"}
	direction := batchDirection.label

	ctx, span := Tracer.Start(r.Context(), "ConversionReview",
		trace.WithAttributes(attribute.String("target", xrdName)))
	defer span.End()

	// reqUID is captured by the recover closure below. The defer has to be
	// registered before the body is decoded — a panic during decode must
	// still be caught — which is why the UID cannot simply be read from
	// the decoded review: at the time the closure is created there is no
	// review yet. Assigning into a variable the closure has already closed
	// over is what makes the UID available on the one path where it
	// matters most.
	//
	// It matters because the apiserver validates that a conversion
	// response's uid matches the request's and discards the response
	// otherwise. A panic that reported an empty UID produced a generic
	// UID-mismatch error instead of the "internal error: …" message, so
	// the single situation where the operator most wants to say what
	// happened was the one guaranteed not to arrive.
	var reqUID types.UID
	defer func() {
		if rec := recover(); rec != nil {
			s.writeReview(w, reqUID, nil, fmt.Sprintf("internal error: %v", rec))
			s.observe(xrdName, direction, "panic", start)
			if s.Metrics != nil {
				s.Metrics.PanicsTotal.WithLabelValues(xrdName).Inc()
			}
			err := fmt.Errorf("panic: %v", rec)
			// The stack is the only thing that makes a panic actionable,
			// and it was previously recorded on the span and nowhere else
			// — invisible to anyone without a tracing backend.
			logger().Error(err, "recovered from a panic while serving a ConversionReview",
				"target", xrdName, "uid", string(reqUID), "stack", string(debug.Stack()))
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Bound the body before reading a byte of it. Without this a single
	// client can make the process allocate without limit on the apiserver's
	// write path.
	body := r.Body
	if limit := s.maxRequestBytes(); limit > 0 {
		body = http.MaxBytesReader(w, r.Body, limit)
	}
	// Retain the leading bytes so an oversized body can still be answered
	// with a ConversionReview the apiserver will accept — which requires
	// echoing back the request UID it sent.
	sniff := &prefixRecorder{limit: uidSniffBytes}

	var review extv1.ConversionReview
	if err := json.NewDecoder(io.TeeReader(body, sniff)).Decode(&review); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// Deliberately a ConversionReview failure rather than a bare
			// HTTP error: the apiserver discards a non-ConversionReview
			// response body, so an HTTP 413 reaches the user as an opaque
			// "conversion webhook returned invalid response" with nothing
			// about size in it.
			s.writeReview(w, sniffRequestUID(sniff.Bytes()), nil, fmt.Sprintf(
				"ConversionReview body exceeds the %d-byte limit; raise --max-request-bytes on the webhook-server if this batch size is expected",
				tooLarge.Limit))
			s.observe(xrdName, direction, "too_large", start)
			span.SetStatus(codes.Error, "request body too large")
			return
		}
		http.Error(w, fmt.Sprintf("decoding ConversionReview: %v", err), http.StatusBadRequest)
		s.observe(xrdName, direction, "bad_request", start)
		return
	}
	if review.Request == nil {
		http.Error(w, "missing request", http.StatusBadRequest)
		s.observe(xrdName, direction, "bad_request", start)
		return
	}
	reqUID = review.Request.UID

	// Bound the conversion itself, separately from the body read: a
	// pathological object (a deeply nested forEach over a huge array) is
	// slow after the body is fully in hand, so a read timeout does not
	// catch it. Without this the worker is occupied until the apiserver
	// gives up, and the apiserver's own timeout does not free it.
	ctx, cancel := context.WithTimeout(ctx, s.requestTimeout())
	defer cancel()

	entry, ok := s.Registry.Get(xrdName)
	if !ok || entry.Router == nil {
		msg := fmt.Sprintf("no conversion plan registered for XRD %q on this replica; check the XRDConversionConfig's status, or retry shortly if it was just created", xrdName)
		s.writeReview(w, review.Request.UID, nil, msg)
		s.observe(xrdName, direction, "not_registered", start)
		return
	}

	toVersion := versionOf(review.Request.DesiredAPIVersion)
	converted := make([]runtime.RawExtension, 0, len(review.Request.Objects))
	if s.Metrics != nil {
		// Batch size is the missing input for sizing --max-request-bytes:
		// without it an operator raising the limit is guessing.
		s.Metrics.BatchSize.WithLabelValues(xrdName).Observe(float64(len(review.Request.Objects)))
	}
	for _, raw := range review.Request.Objects {
		if err := ctx.Err(); err != nil {
			s.writeReview(w, review.Request.UID, nil, fmt.Sprintf(
				"conversion exceeded the %s per-request budget after %d of %d objects; raise --request-timeout or send smaller batches",
				s.requestTimeout(), len(converted), len(review.Request.Objects)))
			s.observe(xrdName, direction, "timeout", start)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return
		}
		var obj map[string]any
		if err := json.Unmarshal(raw.Raw, &obj); err != nil {
			s.writeReview(w, review.Request.UID, nil, fmt.Sprintf("decoding object: %v", err))
			s.observe(xrdName, direction, "bad_request", start)
			return
		}
		fromVersion := versionOf(stringField(obj, "apiVersion"))
		objDirection := fromVersion + "->" + toVersion
		batchDirection.add(objDirection)
		direction = batchDirection.label
		objStart := time.Now()
		_, objSpan := Tracer.Start(ctx, "Convert",
			trace.WithAttributes(
				attribute.String("target", xrdName),
				attribute.String("from_version", fromVersion),
				attribute.String("to_version", toVersion),
			))
		if fromVersion == entry.Router.Hub {
			s.recordLossy(entry, xrdName, "hub_to_spoke", toVersion)
		} else if toVersion == entry.Router.Hub {
			s.recordLossy(entry, xrdName, "spoke_to_hub", fromVersion)
		}

		out, err := entry.Router.Convert(obj, fromVersion, toVersion)
		if err != nil {
			objSpan.RecordError(err)
			objSpan.SetStatus(codes.Error, err.Error())
			objSpan.End()
			s.writeReview(w, review.Request.UID, nil, fmt.Sprintf("converting %s -> %s: %v", fromVersion, toVersion, err))
			s.observe(xrdName, direction, "error", start)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			if s.Metrics != nil {
				s.Metrics.ObjectsTotal.WithLabelValues(xrdName, fromVersion, toVersion, "error").Inc()
				s.Metrics.ObjectDuration.WithLabelValues(xrdName, objDirection, "error").Observe(time.Since(objStart).Seconds())
			}
			return
		}
		objSpan.End()
		out["apiVersion"] = review.Request.DesiredAPIVersion
		ensureConvertedMetadata(out, obj)
		b, err := json.Marshal(out)
		if err != nil {
			s.writeReview(w, review.Request.UID, nil, fmt.Sprintf("marshaling converted object: %v", err))
			s.observe(xrdName, direction, "error", start)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return
		}
		converted = append(converted, runtime.RawExtension{Raw: b})
		if s.Metrics != nil {
			s.Metrics.ObjectsTotal.WithLabelValues(xrdName, fromVersion, toVersion, "success").Inc()
			s.Metrics.ObjectDuration.WithLabelValues(xrdName, objDirection, "success").Observe(time.Since(objStart).Seconds())
		}
	}

	s.writeReview(w, review.Request.UID, converted, "")
	s.observe(xrdName, direction, "success", start)
}

func (s *Server) recordLossy(entry *CompiledEntry, xrdName, direction, spokeVersion string) {
	if s.Metrics == nil || entry.Lossless == nil {
		return
	}
	v, ok := entry.Lossless[spokeVersion]
	if !ok {
		return
	}
	lossy := (direction == "hub_to_spoke" && !v.HubToSpoke) || (direction == "spoke_to_hub" && !v.SpokeToHub)
	if lossy {
		s.Metrics.LossyTotal.WithLabelValues(xrdName, direction).Inc()
	}
}

func (s *Server) observe(xrd, direction, result string, start time.Time) {
	if s.Metrics == nil {
		return
	}
	s.Metrics.ReviewDuration.WithLabelValues(xrd, direction, result).Observe(time.Since(start).Seconds())
	s.Metrics.ReviewRequestsTotal.WithLabelValues(xrd, result).Inc()
}

func (s *Server) writeReview(w http.ResponseWriter, uid types.UID, converted []runtime.RawExtension, failureMessage string) {
	resp := extv1.ConversionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "apiextensions.k8s.io/v1", Kind: "ConversionReview"},
		Response: &extv1.ConversionResponse{
			UID:              uid,
			ConvertedObjects: converted,
			Result:           metav1.Status{Status: metav1.StatusSuccess},
		},
	}
	if failureMessage != "" {
		resp.Response.ConvertedObjects = nil
		resp.Response.Result = metav1.Status{Status: metav1.StatusFailure, Message: failureMessage}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ensureConvertedMetadata keeps the ConversionReview contract: the
// apiserver treats a missing or null metadata field as
// "invalid metadata: missing metadata in converted object". Flux SSA
// prune converts a field-set fragment that often has no metadata.
func ensureConvertedMetadata(out, original map[string]any) {
	if md, ok := out["metadata"]; ok && md != nil {
		return
	}
	if md, ok := original["metadata"]; ok && md != nil {
		out["metadata"] = md
		return
	}
	out["metadata"] = map[string]any{}
}

func versionOf(apiVersion string) string {
	parts := strings.SplitN(apiVersion, "/", 2)
	return parts[len(parts)-1]
}

func stringField(obj map[string]any, key string) string {
	s, _ := obj[key].(string)
	return s
}

// prefixRecorder keeps the first `limit` bytes written through it and
// silently discards the rest. Used to retain enough of an oversized
// ConversionReview to recover its UID without buffering the whole thing —
// buffering the body to answer "your body was too big" would be its own
// denial of service.
type prefixRecorder struct {
	limit int
	buf   bytes.Buffer
}

func (p *prefixRecorder) Write(b []byte) (int, error) {
	if remaining := p.limit - p.buf.Len(); remaining > 0 {
		if len(b) < remaining {
			remaining = len(b)
		}
		p.buf.Write(b[:remaining])
	}
	// Always report the full length: this is a TeeReader sink, and a short
	// write would surface as an error on the read side.
	return len(b), nil
}

func (p *prefixRecorder) Bytes() []byte { return p.buf.Bytes() }

// sniffRequestUID recovers request.uid from the leading, possibly truncated
// bytes of a ConversionReview body.
//
// It exists because the apiserver rejects a conversion response whose uid
// does not match the request's, so a size-limit failure with an empty uid
// reaches the user as a generic UID-mismatch error and the message
// explaining the limit is thrown away. Best-effort by construction: the
// body is truncated, so a full JSON parse is not possible.
//
// ConversionRequest serializes uid first (it is the first field of the Go
// struct), so in practice the value is in the first hundred bytes and well
// inside the retained prefix. A miss returns the empty UID, which is the
// same as not trying.
func sniffRequestUID(prefix []byte) types.UID {
	req := bytes.Index(prefix, []byte(`"request"`))
	if req < 0 {
		return ""
	}
	rest := prefix[req+len(`"request"`):]
	key := bytes.Index(rest, []byte(`"uid"`))
	if key < 0 {
		return ""
	}
	rest = rest[key+len(`"uid"`):]
	// Skip the colon and any whitespace, then require a complete quoted
	// string: a UID cut in half by the truncation is worse than none, since
	// it would still mismatch but would look deliberate.
	open := bytes.IndexByte(rest, '"')
	if open < 0 {
		return ""
	}
	if colon := bytes.IndexByte(rest, ':'); colon < 0 || colon > open {
		return ""
	}
	rest = rest[open+1:]
	end := bytes.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return types.UID(rest[:end])
}

// logger is resolved per call rather than stored on Server: the process
// installs its logger in main after this package is already constructed in
// tests, and a panic path is not hot enough for the lookup to matter.
func logger() logr.Logger {
	return ctrllog.Log.WithName("webhook-server")
}

// directionTracker resolves the request-level `direction` label for a
// ConversionReview.
//
// A review may carry objects converting in different directions — the
// apiserver batches by desired version, not by source version, so a LIST
// spanning two stored versions arrives as one request. The per-request
// histogram previously took whichever direction the last object happened to
// have, which quietly skewed exactly the per-direction latency numbers that
// capacity planning and the HPA-on-QPS guidance are read from.
//
// "mixed" is deliberately not a real direction: it cannot collide with
// "v1->v2" and it is self-explanatory on a dashboard. Per-object accuracy
// lives in the separate ObjectDuration metric, so nothing is lost by
// collapsing here.
// The zero value is not useful: construct it with label "unknown", which
// is what a failure before any object was inspected is labelled as.
type directionTracker struct {
	label string
	seen  string
}

func (d *directionTracker) add(dir string) {
	switch {
	case d.seen == "":
		d.seen, d.label = dir, dir
	case d.seen != dir:
		d.label = "mixed"
	}
}
