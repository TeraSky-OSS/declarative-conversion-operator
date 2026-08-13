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
	"encoding/json"
	"fmt"
	"net/http"
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
)

// Server is the webhook server's HTTP surface: the conversion endpoint on
// one (TLS) port, and health/metrics/debug on another (plain HTTP) port —
// matching the common pattern of not requiring the metrics scraper to
// present a client certificate.
type Server struct {
	Registry *Registry
	Metrics  *Metrics

	ready atomic.Bool
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
	direction := "unknown"

	ctx, span := Tracer.Start(r.Context(), "ConversionReview",
		trace.WithAttributes(attribute.String("target", xrdName)))
	defer span.End()
	_ = ctx

	defer func() {
		if rec := recover(); rec != nil {
			s.writeReview(w, "", nil, fmt.Sprintf("internal error: %v", rec))
			s.observe(xrdName, direction, "panic", start)
			err := fmt.Errorf("panic: %v", rec)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var review extv1.ConversionReview
	if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
		http.Error(w, fmt.Sprintf("decoding ConversionReview: %v", err), http.StatusBadRequest)
		s.observe(xrdName, direction, "bad_request", start)
		return
	}
	if review.Request == nil {
		http.Error(w, "missing request", http.StatusBadRequest)
		s.observe(xrdName, direction, "bad_request", start)
		return
	}

	entry, ok := s.Registry.Get(xrdName)
	if !ok || entry.Router == nil {
		msg := fmt.Sprintf("no conversion plan registered for XRD %q on this replica; check the XRDConversionConfig's status, or retry shortly if it was just created", xrdName)
		s.writeReview(w, review.Request.UID, nil, msg)
		s.observe(xrdName, direction, "not_registered", start)
		return
	}

	toVersion := versionOf(review.Request.DesiredAPIVersion)
	converted := make([]runtime.RawExtension, 0, len(review.Request.Objects))
	for _, raw := range review.Request.Objects {
		var obj map[string]any
		if err := json.Unmarshal(raw.Raw, &obj); err != nil {
			s.writeReview(w, review.Request.UID, nil, fmt.Sprintf("decoding object: %v", err))
			s.observe(xrdName, direction, "bad_request", start)
			return
		}
		fromVersion := versionOf(stringField(obj, "apiVersion"))
		direction = fromVersion + "->" + toVersion
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
			}
			return
		}
		objSpan.End()
		out["apiVersion"] = review.Request.DesiredAPIVersion
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

func versionOf(apiVersion string) string {
	parts := strings.SplitN(apiVersion, "/", 2)
	return parts[len(parts)-1]
}

func stringField(obj map[string]any, key string) string {
	s, _ := obj[key].(string)
	return s
}
