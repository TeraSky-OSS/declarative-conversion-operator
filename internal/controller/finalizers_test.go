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
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

// capturedPatch records what a finalizer write actually sends to the
// apiserver. The body is the whole point: a patch carrying the full object
// makes this operator's field manager the owner of every field on it,
// which is what broke `helm upgrade` on Helm 4 against the chart-managed
// ConversionWebhookServer.
type capturedPatch struct {
	patchType types.PatchType
	body      map[string]any
	updates   int
}

func newFinalizerRecorder(t *testing.T, objs ...client.Object) (client.Client, *capturedPatch) {
	t.Helper()
	rec := &capturedPatch{}
	builder := newFakeClient()
	for _, o := range objs {
		builder = builder.WithObjects(o)
	}
	c := builder.WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			data, err := patch.Data(obj)
			if err != nil {
				return err
			}
			rec.patchType = patch.Type()
			rec.body = map[string]any{}
			if err := json.Unmarshal(data, &rec.body); err != nil {
				return err
			}
			return cl.Patch(ctx, obj, patch, opts...)
		},
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			rec.updates++
			return cl.Update(ctx, obj, opts...)
		},
	}).Build()
	return c, rec
}

// chartManagedServer mirrors what the Helm chart creates: a
// ConversionWebhookServer whose spec.certificate fields the chart owns.
func chartManagedServer() *teraskyv1alpha1.ConversionWebhookServer {
	return &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			Default:   true,
			Namespace: "operator-ns",
			Certificate: teraskyv1alpha1.CertificateSpec{
				IssuerRef: teraskyv1alpha1.CertificateIssuerRef{Name: "selfsigned", Kind: "ClusterIssuer"},
				Duration:  &metav1.Duration{Duration: 2160 * 60 * 60 * 1000000000},
			},
		},
	}
}

// TestAddFinalizer_TouchesOnlyTheFinalizerList is the regression test for a
// bug the new package-managed e2e leg surfaced: `helm upgrade` on Helm 4
// (which applies server-side) failed with
//
//	Apply failed with 2 conflicts: conflicts with "app":
//	- .spec.certificate.duration
//	- .spec.certificate.renewBefore
//
// because the finalizer was added with a full client.Update, which claims
// ownership of every field set on the object. The same would fight Flux or
// Argo CD over an XRDConversionConfig, both of which apply server-side.
func TestAddFinalizer_TouchesOnlyTheFinalizerList(t *testing.T) {
	server := chartManagedServer()
	c, rec := newFinalizerRecorder(t, server)

	if err := addFinalizer(context.Background(), c, server, teraskyv1alpha1.ConversionWebhookServerFinalizer); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.updates != 0 {
		t.Errorf("a finalizer write must not be a full Update, saw %d", rec.updates)
	}
	if rec.body == nil {
		t.Fatal("expected a patch to have been sent")
	}

	// Exactly one top-level key, and it is metadata.
	if len(rec.body) != 1 {
		t.Fatalf("patch touches more than metadata: %#v", rec.body)
	}
	meta, ok := rec.body["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("patch body has no metadata object: %#v", rec.body)
	}
	if _, claimed := rec.body["spec"]; claimed {
		t.Error("the patch must not carry spec — that is what claimed .spec.certificate.* and broke helm upgrade")
	}
	if _, claimed := meta["annotations"]; claimed {
		t.Error("the patch must not carry annotations")
	}
	finalizers, ok := meta["finalizers"].([]any)
	if !ok || len(finalizers) != 1 || finalizers[0] != teraskyv1alpha1.ConversionWebhookServerFinalizer {
		t.Fatalf("unexpected finalizers in the patch: %#v", meta["finalizers"])
	}

	// And it actually took effect.
	var got teraskyv1alpha1.ConversionWebhookServer
	if err := c.Get(context.Background(), types.NamespacedName{Name: "default"}, &got); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if len(got.Finalizers) != 1 {
		t.Fatalf("finalizer was not persisted: %+v", got.Finalizers)
	}
	// The chart's own field is untouched.
	if got.Spec.Certificate.Duration == nil {
		t.Error("the chart's spec.certificate.duration was lost")
	}
}

func TestAddFinalizer_AlreadyPresentSendsNothing(t *testing.T) {
	server := chartManagedServer()
	server.Finalizers = []string{teraskyv1alpha1.ConversionWebhookServerFinalizer}
	c, rec := newFinalizerRecorder(t, server)

	if err := addFinalizer(context.Background(), c, server, teraskyv1alpha1.ConversionWebhookServerFinalizer); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.body != nil || rec.updates != 0 {
		t.Fatalf("an already-present finalizer must produce no write at all (patch=%v updates=%d)", rec.body, rec.updates)
	}
}

func TestRemoveFinalizer_TouchesOnlyTheFinalizerList(t *testing.T) {
	server := chartManagedServer()
	server.Finalizers = []string{teraskyv1alpha1.ConversionWebhookServerFinalizer, "someone.else/keep-me"}
	c, rec := newFinalizerRecorder(t, server)

	if err := removeFinalizer(context.Background(), c, server, teraskyv1alpha1.ConversionWebhookServerFinalizer); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.updates != 0 {
		t.Errorf("removal must not be a full Update either, saw %d", rec.updates)
	}
	if _, claimed := rec.body["spec"]; claimed {
		t.Errorf("removal patch must not carry spec: %#v", rec.body)
	}

	var got teraskyv1alpha1.ConversionWebhookServer
	if err := c.Get(context.Background(), types.NamespacedName{Name: "default"}, &got); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	// Somebody else's finalizer survives — a merge patch on an atomic list
	// replaces it wholesale, so this asserts the replacement was built from
	// the current list rather than from ours alone.
	if len(got.Finalizers) != 1 || got.Finalizers[0] != "someone.else/keep-me" {
		t.Fatalf("unexpected finalizers after removal: %+v", got.Finalizers)
	}
}

func TestRemoveFinalizer_AbsentSendsNothing(t *testing.T) {
	server := chartManagedServer()
	c, rec := newFinalizerRecorder(t, server)

	if err := removeFinalizer(context.Background(), c, server, teraskyv1alpha1.ConversionWebhookServerFinalizer); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.body != nil || rec.updates != 0 {
		t.Fatalf("removing an absent finalizer must produce no write (patch=%v updates=%d)", rec.body, rec.updates)
	}
}
