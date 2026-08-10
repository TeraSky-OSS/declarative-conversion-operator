/*
Copyright 2026 The xrd-conversion-operator Authors.

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
	"github.com/prometheus/client_golang/prometheus"
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
	RegistryLastReload  *prometheus.GaugeVec
	RegistryReloadTotal *prometheus.CounterVec
	RegistryCompileErr  *prometheus.CounterVec
	Ready               prometheus.Gauge
}

// NewMetrics constructs and registers the full metric set on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		ReviewDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "xrdconv_webhook_conversion_review_duration_seconds",
			Help:    "Latency of ConversionReview requests, end to end.",
			Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
		}, []string{"xrd", "direction", "result"}),
		ReviewRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xrdconv_webhook_conversion_review_requests_total",
			Help: "Total ConversionReview requests handled.",
		}, []string{"xrd", "result"}),
		ObjectsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xrdconv_webhook_conversion_objects_total",
			Help: "Total individual objects converted.",
		}, []string{"xrd", "from_version", "to_version", "result"}),
		LossyTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xrdconv_webhook_lossy_conversion_total",
			Help: "Total conversions performed in a direction statically known to be lossy.",
		}, []string{"xrd", "direction"}),
		RegistrySize: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "xrdconv_webhook_registry_size",
			Help: "Number of XRD paths currently loaded on this replica.",
		}),
		RegistryLastReload: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xrdconv_webhook_registry_last_reload_timestamp_seconds",
			Help: "Unix timestamp of the last successful compile, per XRD.",
		}, []string{"xrd"}),
		RegistryReloadTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xrdconv_webhook_registry_reload_total",
			Help: "Total attempted (re)compiles, per XRD and result.",
		}, []string{"xrd", "result"}),
		RegistryCompileErr: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xrdconv_webhook_registry_compile_errors_total",
			Help: "Total compile failures that left a stale-but-serving (or absent) plan in place.",
		}, []string{"xrd", "reason"}),
		Ready: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "xrdconv_webhook_ready",
			Help: "1 if this replica's registry has completed its initial sync and is serving traffic.",
		}),
	}
	reg.MustRegister(m.ReviewDuration, m.ReviewRequestsTotal, m.ObjectsTotal, m.LossyTotal, m.RegistrySize, m.RegistryLastReload, m.RegistryReloadTotal, m.RegistryCompileErr, m.Ready)
	return m
}
