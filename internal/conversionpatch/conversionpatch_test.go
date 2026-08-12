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

package conversionpatch

import (
	"encoding/base64"
	"reflect"
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func testParams() Params {
	return Params{
		TargetName:       "xwidgets.example.org",
		ConfigName:       "xwidgets-conversion",
		PlanHash:         "sha256:abc",
		ServiceName:      "prod-webhook-server",
		ServiceNamespace: "conversion-system",
		Path:             "/convert/xwidgets.example.org",
		Port:             8443,
		CABundle:         base64.StdEncoding.EncodeToString([]byte("PEM")),
		ReviewVersions:   []string{"v1", "v1beta1"},
	}
}

func TestBuildXRDConversionPatch(t *testing.T) {
	patch := BuildXRDConversionPatch(testParams())

	if got := patch.GetAPIVersion(); got != "apiextensions.crossplane.io/v2" {
		t.Fatalf("apiVersion = %q", got)
	}
	if got := patch.GetKind(); got != "CompositeResourceDefinition" {
		t.Fatalf("kind = %q", got)
	}
	if got := patch.GetName(); got != "xwidgets.example.org" {
		t.Fatalf("name = %q", got)
	}
	wantAnnotations := map[string]string{
		ManagedByAnnotation: "xwidgets-conversion",
		PlanHashAnnotation:  "sha256:abc",
	}
	if got := patch.GetAnnotations(); !reflect.DeepEqual(got, wantAnnotations) {
		t.Fatalf("annotations = %v, want %v", got, wantAnnotations)
	}

	strategy, found, err := unstructured.NestedString(patch.Object, "spec", "conversion", "strategy")
	if err != nil || !found || strategy != "Webhook" {
		t.Fatalf("spec.conversion.strategy = %q (found=%v, err=%v)", strategy, found, err)
	}
	svc, found, err := unstructured.NestedMap(patch.Object, "spec", "conversion", "webhook", "clientConfig", "service")
	if err != nil || !found {
		t.Fatalf("service block missing: found=%v err=%v", found, err)
	}
	want := map[string]any{
		"name":      "prod-webhook-server",
		"namespace": "conversion-system",
		"path":      "/convert/xwidgets.example.org",
		"port":      int64(8443),
	}
	if !reflect.DeepEqual(svc, want) {
		t.Fatalf("service = %#v, want %#v", svc, want)
	}
	ca, _, _ := unstructured.NestedString(patch.Object, "spec", "conversion", "webhook", "clientConfig", "caBundle")
	if ca != testParams().CABundle {
		t.Fatalf("caBundle = %q, want the base64 input verbatim", ca)
	}
	versions, _, err := unstructured.NestedStringSlice(patch.Object, "spec", "conversion", "webhook", "conversionReviewVersions")
	if err != nil {
		t.Fatalf("conversionReviewVersions is not a string slice: %v", err)
	}
	if !reflect.DeepEqual(versions, []string{"v1", "v1beta1"}) {
		t.Fatalf("conversionReviewVersions = %v", versions)
	}

	// An unstructured patch whose leaves aren't JSON-typed panics the
	// moment controller-runtime deep-copies it on the way to the API
	// server, so prove it survives that round trip here rather than in
	// production.
	_ = patch.DeepCopy()
}

func TestBuildCRDConversionPatch(t *testing.T) {
	patch, err := BuildCRDConversionPatch(testParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if patch.Name == nil || *patch.Name != "xwidgets.example.org" {
		t.Fatalf("name = %v", patch.Name)
	}
	if patch.Kind == nil || *patch.Kind != "CustomResourceDefinition" {
		t.Fatalf("kind = %v", patch.Kind)
	}
	if got := patch.Annotations[ManagedByAnnotation]; got != "xwidgets-conversion" {
		t.Fatalf("managed-by annotation = %q", got)
	}
	conv := patch.Spec.Conversion
	if conv == nil || conv.Strategy == nil || *conv.Strategy != extv1.WebhookConverter {
		t.Fatalf("conversion strategy = %+v", conv)
	}
	svc := conv.Webhook.ClientConfig.Service
	if *svc.Name != "prod-webhook-server" || *svc.Namespace != "conversion-system" || *svc.Port != 8443 {
		t.Fatalf("service = %+v", svc)
	}
	// The typed API takes raw bytes, so the base64 input must have been
	// decoded on the way in.
	if string(conv.Webhook.ClientConfig.CABundle) != "PEM" {
		t.Fatalf("caBundle = %q, want the decoded bytes", conv.Webhook.ClientConfig.CABundle)
	}
	if !reflect.DeepEqual(conv.Webhook.ConversionReviewVersions, []string{"v1", "v1beta1"}) {
		t.Fatalf("conversionReviewVersions = %v", conv.Webhook.ConversionReviewVersions)
	}
}

func TestBuildCRDConversionPatch_RejectsNonBase64CABundle(t *testing.T) {
	p := testParams()
	p.CABundle = "-----BEGIN CERTIFICATE-----"
	if _, err := BuildCRDConversionPatch(p); err == nil {
		t.Fatalf("expected an error for a CA bundle that isn't base64")
	}
}

func TestBuildRevertPatches(t *testing.T) {
	xrd := BuildXRDRevertPatch("xwidgets.example.org")
	strategy, found, err := unstructured.NestedString(xrd.Object, "spec", "conversion", "strategy")
	if err != nil || !found || strategy != "None" {
		t.Fatalf("XRD revert strategy = %q (found=%v, err=%v)", strategy, found, err)
	}
	if _, found, _ := unstructured.NestedMap(xrd.Object, "spec", "conversion", "webhook"); found {
		t.Fatalf("expected a revert patch to carry no webhook block at all")
	}

	crd := BuildCRDRevertPatch("widgets.example.org")
	if crd.Spec.Conversion == nil || *crd.Spec.Conversion.Strategy != extv1.NoneConverter {
		t.Fatalf("CRD revert strategy = %+v", crd.Spec.Conversion)
	}
	if crd.Spec.Conversion.Webhook != nil {
		t.Fatalf("expected a revert patch to carry no webhook block at all")
	}
}
