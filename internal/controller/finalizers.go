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
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// patchFinalizers adds or removes a finalizer with a patch that touches
// metadata.finalizers and nothing else.
//
// The obvious implementation — controllerutil.AddFinalizer followed by
// client.Update — sends the WHOLE object, and a full Update makes this
// process's field manager the owner of every field currently set on it.
// For an object somebody else manages declaratively, that is a land grab:
// the next server-side apply from its real owner fails with a conflict on
// fields this operator never meant to claim.
//
// It is not hypothetical. The chart creates the default
// ConversionWebhookServer, so `helm upgrade` on Helm 4 (which applies
// server-side) failed with:
//
//	conflict occurred while applying object ... Kind=ConversionWebhookServer:
//	Apply failed with 2 conflicts: conflicts with "app":
//	- .spec.certificate.duration
//	- .spec.certificate.renewBefore
//
// ("app" is this binary: the image's ENTRYPOINT is /app, and
// controller-runtime derives its default field manager from os.Args[0].)
//
// The same applies to XRDConversionConfig and CRDConversionConfig, which
// are exactly the kind of object a GitOps tool owns — Flux and Argo CD both
// apply server-side by default, so the finalizer write would fight them on
// every reconcile.
//
// A merge patch claims only the fields the patch body actually carries, so
// scoping the write to metadata.finalizers scopes the ownership with it.
//
// The patch carries an optimistic lock, which is not optional here.
// metadata.finalizers is an atomic list: a merge patch replaces it whole.
// Without a resourceVersion precondition, a finalizer another controller
// added between our read and our write would be silently dropped — and a
// dropped finalizer means that controller's cleanup is skipped entirely
// when the object is deleted. With the lock, a concurrent write makes this
// patch fail with a conflict and the reconcile retries against fresh state.
func patchFinalizers(ctx context.Context, c client.Client, obj client.Object, mutate func() bool) error {
	orig := obj.DeepCopyObject().(client.Object)
	if !mutate() {
		return nil
	}
	return c.Patch(ctx, obj, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}))
}

// addFinalizer adds one, patching only metadata.finalizers. Returns nil
// without a write when the finalizer is already present.
func addFinalizer(ctx context.Context, c client.Client, obj client.Object, finalizer string) error {
	return patchFinalizers(ctx, c, obj, func() bool {
		return controllerutil.AddFinalizer(obj, finalizer)
	})
}

// removeFinalizer is addFinalizer's counterpart, with the same scoping.
func removeFinalizer(ctx context.Context, c client.Client, obj client.Object, finalizer string) error {
	return patchFinalizers(ctx, c, obj, func() bool {
		return controllerutil.RemoveFinalizer(obj, finalizer)
	})
}
