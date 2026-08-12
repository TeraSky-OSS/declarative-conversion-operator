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

// MapFunc maps a single object (Create / Generic) to reconcile requests.
type MapFunc func(context.Context, client.Object) []reconcile.Request

// UpdateMapFunc maps an Update event using both old and new objects so
// callers can enqueue the union of prior and current assignments.
type UpdateMapFunc func(ctx context.Context, oldObj, newObj client.Object) []reconcile.Request

// PacedMapFuncs returns an EventHandler that paces enqueue of mapped
// requests at the given QPS. Update events use updateFn (old+new);
// Create/Delete/Generic use mapFn on the single available object.
// qps <= 0 disables pacing (immediate Add for every request).
func PacedMapFuncs(mapFn MapFunc, updateFn UpdateMapFunc, qps float64) handler.EventHandler {
	enqueue := func(reqs []reconcile.Request, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		reqs = dedupeRequests(reqs)
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
			enqueue(mapFn(ctx, e.Object), q)
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(updateFn(ctx, e.ObjectOld, e.ObjectNew), q)
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(mapFn(ctx, e.Object), q)
		},
		GenericFunc: func(ctx context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(mapFn(ctx, e.Object), q)
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

func dedupeRequests(reqs []reconcile.Request) []reconcile.Request {
	if len(reqs) < 2 {
		return reqs
	}
	seen := make(map[string]struct{}, len(reqs))
	out := make([]reconcile.Request, 0, len(reqs))
	for _, req := range reqs {
		key := req.Namespace + "/" + req.Name
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, req)
	}
	return out
}
