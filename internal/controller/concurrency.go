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

import ctrlcontroller "sigs.k8s.io/controller-runtime/pkg/controller"

// DefaultMaxConcurrentReconciles matches controller-runtime's own default.
// It is named here rather than left implicit because the shipped dashboard
// now plots workqueue depth, which invites the question "can I turn this
// up?" — and a lever whose default nobody can state is not much of a
// lever.
const DefaultMaxConcurrentReconciles = 1

// controllerOptions builds the per-controller options every controller in
// this package is wired with. A zero or negative value means "leave
// controller-runtime's default alone", so an unset field on a reconciler
// struct behaves exactly as it did before the field existed.
//
// Raising it is safe for these controllers specifically: each reconcile is
// keyed by one config or one server, controller-runtime guarantees a given
// key is never reconciled by two workers at once, and nothing in the
// reconcile path shares mutable state across keys. What it costs is
// apiserver QPS, which is the reason it is not raised by default.
func controllerOptions(maxConcurrent int) ctrlcontroller.Options {
	if maxConcurrent <= 0 {
		return ctrlcontroller.Options{}
	}
	return ctrlcontroller.Options{MaxConcurrentReconciles: maxConcurrent}
}
