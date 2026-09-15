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

package controller

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// ManagerMetrics is the operator manager's custom Prometheus set —
// compile/analyze failures, SSA apply latency, and drift-policy phase
// transitions that controller-runtime's generic reconcile metrics don't
// distinguish.
type ManagerMetrics struct {
	AnalyzeFailures  *prometheus.CounterVec
	ApplyDuration    *prometheus.HistogramVec
	PhaseTransitions *prometheus.CounterVec
	// ConversionReverts counts times the operator found a previously
	// applied conversion stanza missing from the live target. On a
	// package-managed XRD this is Crossplane's establisher having
	// overwritten it; the counter is what turns an invisible, silent,
	// roughly-hourly hazard into a number.
	ConversionReverts *prometheus.CounterVec
	// PropagationLag measures apply -> observed in the generated CRD.
	// Applied says the operator patched the XRD; this says Crossplane
	// re-rendered the CRD with it, which is when conversion actually
	// starts working.
	PropagationLag *prometheus.HistogramVec
}

var (
	managerMetrics     *ManagerMetrics
	managerMetricsOnce sync.Once
)

// GetManagerMetrics returns the process-wide manager metrics, registering
// them on controller-runtime's registry exactly once.
func GetManagerMetrics() *ManagerMetrics {
	managerMetricsOnce.Do(func() {
		managerMetrics = &ManagerMetrics{
			AnalyzeFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "dco_manager_analyze_failures_total",
				Help: "Analyze/compile validation failures during config reconcile (manager side).",
			}, []string{"config_kind", "target", "reason"}),
			ApplyDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:    "dco_manager_apply_duration_seconds",
				Help:    "Latency of SSA patches applying conversion webhook config onto the target XRD/CRD.",
				Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
			}, []string{"config_kind", "target", "result"}),
			PhaseTransitions: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "dco_manager_phase_transitions_total",
				Help: "Config status phase transitions observed by the manager (e.g. Applied→Stale, Applied→Failed).",
			}, []string{"config_kind", "target", "from_phase", "to_phase", "reason"}),
			ConversionReverts: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "dco_manager_conversion_reverts_total",
				Help: "Times a previously-applied conversion stanza was found missing from the target (an out-of-band overwrite, typically Crossplane's package establisher).",
			}, []string{"config_kind", "target"}),
			PropagationLag: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:    "dco_manager_propagation_lag_seconds",
				Help:    "Time from applying spec.conversion to the XRD until Crossplane's generated CRD was observed carrying it.",
				Buckets: []float64{0.5, 1, 2.5, 5, 10, 30, 60, 300, 900},
			}, []string{"target"}),
		}
		crmetrics.Registry.MustRegister(
			managerMetrics.AnalyzeFailures,
			managerMetrics.ApplyDuration,
			managerMetrics.PhaseTransitions,
			managerMetrics.ConversionReverts,
			managerMetrics.PropagationLag,
		)
	})
	return managerMetrics
}
