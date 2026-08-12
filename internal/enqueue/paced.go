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

// Package enqueue provides shared watch-mapping helpers for controller
// fan-out, notably paced enqueue of ConversionWebhookServer changes onto
// the configs that resolve to them.
package enqueue

import (
	"context"
	"time"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// CWSConfigEnqueueQPS is the documented enqueue pacing for
// ConversionWebhookServer → config fan-out. Request i is delayed by
// i/QPS seconds, so N=200 assigned configs spreads over 4s at the default
// 50 QPS instead of an immediate workqueue burst. Feeds Phase 9 load e2e.
const CWSConfigEnqueueQPS = 50.0

// MapFunc is the same signature as controller-runtime's handler.MapFunc.
type MapFunc func(context.Context, client.Object) []reconcile.Request

// PacedMapFunc returns an EventHandler that runs fn then enqueues the
// resulting requests with staggered AddAfter delays at the given QPS.
// qps <= 0 disables pacing (immediate Add for every request).
func PacedMapFunc(fn MapFunc, qps float64) handler.EventHandler {
	enqueue := func(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		reqs := fn(ctx, obj)
		if qps <= 0 {
			for _, req := range reqs {
				q.Add(req)
			}
			return
		}
		for i, req := range reqs {
			delay := time.Duration(float64(i) / qps * float64(time.Second))
			if delay <= 0 {
				q.Add(req)
				continue
			}
			q.AddAfter(req, delay)
		}
	}
	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, e.Object, q)
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, e.ObjectNew, q)
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, e.Object, q)
		},
		GenericFunc: func(ctx context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, e.Object, q)
		},
	}
}

// FanoutSpread returns how long the last request is delayed for n
// requests at the given QPS. Used by tests and docs.
func FanoutSpread(n int, qps float64) time.Duration {
	if n <= 1 || qps <= 0 {
		return 0
	}
	return time.Duration(float64(n-1) / qps * float64(time.Second))
}
