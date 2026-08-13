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
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the webhook server's Prometheus metric set, registered on a
// dedicated registry (not the global default) so cmd/webhook-server has
// full control over exactly what /metrics exposes.
type Metrics struct {
	ReviewDuration      *prometheus.HistogramVec
	ReviewRequestsTotal *prometheus.CounterVec
	ObjectsTotal        *prometheus.CounterVec
	LossyTotal          *prometheus.CounterVec
	RegistrySize        prometheus.Gauge
	RegistryEntryLoaded *prometheus.GaugeVec
	RegistryLastReload  *prometheus.GaugeVec
	RegistryReloadTotal *prometheus.CounterVec
	RegistryCompileErr  *prometheus.CounterVec
	Ready               prometheus.Gauge

	// gatherer is the registry metrics were registered on, used by
	// PlainMux's /metrics handler. Nil when NewMetrics was given a
	// Registerer that is not also a Gatherer.
	gatherer prometheus.Gatherer
}

// NewMetrics constructs and registers the full metric set on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		ReviewDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "dco_webhook_conversion_review_duration_seconds",
			Help:    "Latency of ConversionReview requests, end to end.",
			Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
		}, []string{"target", "direction", "result"}),
		ReviewRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dco_webhook_conversion_review_requests_total",
			Help: "Total ConversionReview requests handled.",
		}, []string{"target", "result"}),
		ObjectsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dco_webhook_conversion_objects_total",
			Help: "Total individual objects converted.",
		}, []string{"target", "from_version", "to_version", "result"}),
		LossyTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dco_webhook_lossy_conversion_total",
			Help: "Total conversions performed in a direction statically known to be lossy.",
		}, []string{"target", "direction"}),
		RegistrySize: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dco_webhook_registry_size",
			Help: "Number of target resources (XRD/CRD names) currently present in this replica's registry, including error-only placeholders.",
		}),
		RegistryEntryLoaded: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dco_webhook_registry_entry_loaded",
			Help: "1 if this replica has a compiled, servable conversion plan for the target; 0 if the registry entry is error-only / not ready. Scraped per pod, so replica identity comes from the scrape target.",
		}, []string{"target"}),
		RegistryLastReload: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dco_webhook_registry_last_reload_timestamp_seconds",
			Help: "Unix timestamp of the last successful compile, per target.",
		}, []string{"target"}),
		RegistryReloadTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dco_webhook_registry_reload_total",
			Help: "Total attempted (re)compiles, per target and result.",
		}, []string{"target", "result"}),
		RegistryCompileErr: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dco_webhook_registry_compile_errors_total",
			Help: "Total compile failures that left a stale-but-serving (or absent) plan in place.",
		}, []string{"target", "reason"}),
		Ready: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dco_webhook_ready",
			Help: "1 if this replica's registry has completed its initial sync and is serving traffic.",
		}),
	}
	if g, ok := reg.(prometheus.Gatherer); ok {
		m.gatherer = g
	}
	reg.MustRegister(m.ReviewDuration, m.ReviewRequestsTotal, m.ObjectsTotal, m.LossyTotal, m.RegistrySize, m.RegistryEntryLoaded, m.RegistryLastReload, m.RegistryReloadTotal, m.RegistryCompileErr, m.Ready)
	return m
}

// Handler returns an HTTP handler that exposes this metric set. Prefer this
// over promhttp.Handler(), which serves the process-wide default registry
// and would hide every series registered on the dedicated registry.
func (m *Metrics) Handler() http.Handler {
	if m == nil || m.gatherer == nil {
		return promhttp.Handler()
	}
	return promhttp.HandlerFor(m.gatherer, promhttp.HandlerOpts{})
}

// SyncRegistryMetrics refreshes RegistrySize and per-target
// RegistryEntryLoaded gauges from the live registry snapshot. Call after
// any Set / Remove / RecordError so operators can scrape "is config X
// loaded on this replica?" without exec'ing into the pod.
func (m *Metrics) SyncRegistryMetrics(reg *Registry) {
	if m == nil || reg == nil {
		return
	}
	snap := reg.Snapshot()
	m.RegistrySize.Set(float64(len(snap)))
	m.RegistryEntryLoaded.Reset()
	for name, entry := range snap {
		loaded := 0.0
		if entry != nil && entry.Router != nil {
			loaded = 1
		}
		m.RegistryEntryLoaded.WithLabelValues(name).Set(loaded)
	}
}
