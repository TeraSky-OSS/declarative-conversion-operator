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
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/watchmap"
)

func TestMapServerToAssignedConfigs_ListErrorSurfaced(t *testing.T) {
	var hooked string
	var hookedErr error
	prev := watchmap.ErrorHook
	watchmap.ErrorHook = func(mapFunc string, err error) {
		hooked = mapFunc
		hookedErr = err
	}
	t.Cleanup(func() { watchmap.ErrorHook = prev })

	srv := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "srv"}}
	c := newFakeClient(srv).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				return fmt.Errorf("injected list failure")
			},
		}).
		Build()
	r := &XRDConversionConfigReconciler{Client: c}

	reqs := r.mapServerToAssignedConfigs(context.Background(), srv)
	if len(reqs) != 0 {
		t.Fatalf("expected empty requests on list failure, got %#v", reqs)
	}
	if hooked != "xrdconversionconfig.mapServerToAssignedConfigs" {
		t.Fatalf("expected ErrorHook for mapServerToAssignedConfigs, got %q", hooked)
	}
	if hookedErr == nil || hookedErr.Error() == "" {
		t.Fatalf("expected ErrorHook to receive the list error")
	}
}

func TestMapXRDToConfigs_ListErrorSurfaced(t *testing.T) {
	var hooked string
	prev := watchmap.ErrorHook
	watchmap.ErrorHook = func(mapFunc string, err error) { hooked = mapFunc }
	t.Cleanup(func() { watchmap.ErrorHook = prev })

	xrd := establishedXRD("xfoos.example.org")
	c := newFakeClient().
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				return fmt.Errorf("injected list failure")
			},
		}).
		Build()
	r := &XRDConversionConfigReconciler{Client: c}
	reqs := r.mapXRDToConfigs(context.Background(), xrd)
	if len(reqs) != 0 {
		t.Fatalf("expected empty requests on list failure, got %#v", reqs)
	}
	if hooked != "xrdconversionconfig.mapXRDToConfigs" {
		t.Fatalf("expected ErrorHook for mapXRDToConfigs, got %q", hooked)
	}
}

func TestEnqueueAllServers_ListErrorSurfaced(t *testing.T) {
	var hooked string
	prev := watchmap.ErrorHook
	watchmap.ErrorHook = func(mapFunc string, err error) { hooked = mapFunc }
	t.Cleanup(func() { watchmap.ErrorHook = prev })

	c := newFakeClient().
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				return fmt.Errorf("injected list failure")
			},
		}).
		Build()
	fn := enqueueAllServers(c)
	reqs := fn(context.Background(), &teraskyv1alpha1.XRDConversionConfig{})
	if len(reqs) != 0 {
		t.Fatalf("expected empty requests on list failure, got %#v", reqs)
	}
	if hooked != "conversionwebhookserver.enqueueAllServers" {
		t.Fatalf("expected ErrorHook for enqueueAllServers, got %q", hooked)
	}
}

// Ensure the interceptor import's runtime Object usage stays compile-clean
// when only client.ObjectList is referenced above.
var _ runtime.Object = &teraskyv1alpha1.XRDConversionConfig{}
