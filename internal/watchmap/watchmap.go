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

// Package watchmap surfaces List failures inside EnqueueRequestsFromMapFunc
// handlers. Those handlers cannot return errors to controller-runtime, so a
// silent empty result looks identical to "no related objects" — this package
// logs and increments a Prometheus counter instead.
package watchmap

import (
	"context"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/log"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	listErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dco_watch_map_list_errors_total",
		Help: "Watch-mapping List failures that would otherwise look like an empty result (no related configs).",
	}, []string{"map_func"})

	registerOnce sync.Once
)

// ErrorHook is invoked from ListError when set. Tests use it to assert that
// a mapping function surfaced a List failure instead of returning silently.
var ErrorHook func(mapFunc string, err error)

func ensureRegistered() {
	registerOnce.Do(func() {
		crmetrics.Registry.MustRegister(listErrors)
	})
}

// ListError logs err, increments dco_watch_map_list_errors_total for
// mapFunc, invokes ErrorHook if set, and returns an empty request slice.
// Call this instead of a bare `return nil` when a watch-mapping List fails.
func ListError(ctx context.Context, mapFunc string, err error) []reconcile.Request {
	ensureRegistered()
	log.FromContext(ctx).Error(err, "watch mapping list failed; related reconciles may be skipped until the next event or periodic resync", "mapFunc", mapFunc)
	listErrors.WithLabelValues(mapFunc).Inc()
	if ErrorHook != nil {
		ErrorHook(mapFunc, err)
	}
	return nil
}
