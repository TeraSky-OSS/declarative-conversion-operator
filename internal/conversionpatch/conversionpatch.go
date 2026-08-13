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

// Package conversionpatch builds the server-side-apply patches that point
// an XRD's or CRD's spec.conversion at a webhook server.
//
// These are pure functions of their inputs with no client and no context,
// so the exact object the operator would apply can be rendered offline —
// that's what backs `convctl patch-preview`. The controllers call the same
// builders they always did, they just no longer own the construction.
package conversionpatch

import (
	"encoding/base64"
	"fmt"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	applyextv1 "k8s.io/apiextensions-apiserver/pkg/client/applyconfiguration/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// The annotations the operator stamps alongside spec.conversion, recording
// which config owns the patch and which compiled plan it reflects.
const (
	ManagedByAnnotation = "conversion.terasky.com/managed-by"
	PlanHashAnnotation  = "conversion.terasky.com/plan-hash"
)

// Params is everything a conversion patch needs. It deliberately takes
// already-resolved values (a service name, not a server name) so the
// builders stay free of both cluster lookups and the operator's own naming
// conventions.
type Params struct {
	// TargetName is the XRD's or CRD's metadata.name.
	TargetName string
	// ConfigName is the XRDConversionConfig/CRDConversionConfig that owns
	// this patch, recorded in the managed-by annotation.
	ConfigName string
	// PlanHash is the config's schema hash, recorded in the plan-hash
	// annotation.
	PlanHash string

	ServiceName      string
	ServiceNamespace string
	Path             string
	Port             int32
	// CABundle is base64-encoded, matching how it is read out of the
	// cert-manager Secret and how a CRD's YAML spells it.
	CABundle       string
	ReviewVersions []string
}

// BuildXRDConversionPatch renders the SSA patch that points a Crossplane
// XRD's spec.conversion at the webhook server. Crossplane's XRD type isn't
// vendored here, so this stays unstructured — the exact shape the
// controller has always applied.
func BuildXRDConversionPatch(p Params) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": xrdadapter.GroupVersionKind.GroupVersion().String(),
		"kind":       xrdadapter.GroupVersionKind.Kind,
		"metadata": map[string]any{
			"name": p.TargetName,
			// map[string]any, not map[string]string: everything inside an
			// unstructured.Unstructured has to stay JSON-typed or its
			// deep-copy panics.
			"annotations": map[string]any{
				ManagedByAnnotation: p.ConfigName,
				PlanHashAnnotation:  p.PlanHash,
			},
		},
		"spec": map[string]any{
			"conversion": map[string]any{
				"strategy": "Webhook",
				"webhook": map[string]any{
					"clientConfig": map[string]any{
						"service": map[string]any{
							"name":      p.ServiceName,
							"namespace": p.ServiceNamespace,
							"path":      p.Path,
							"port":      int64(p.Port),
						},
						"caBundle": p.CABundle,
					},
					"conversionReviewVersions": toAnySlice(p.ReviewVersions),
				},
			},
		},
	}}
}

// BuildCRDConversionPatch is BuildXRDConversionPatch's sibling for a native
// CustomResourceDefinition, which — being a vendored core Kubernetes type —
// gets a typed apply configuration instead of an unstructured object. Fails
// on a CA bundle that isn't valid base64, since the typed API takes raw
// bytes.
func BuildCRDConversionPatch(p Params) (*applyextv1.CustomResourceDefinitionApplyConfiguration, error) {
	caBundleBytes, err := base64.StdEncoding.DecodeString(p.CABundle)
	if err != nil {
		return nil, fmt.Errorf("decoding CA bundle: %w", err)
	}
	return applyextv1.CustomResourceDefinition(p.TargetName).
		WithAnnotations(map[string]string{
			ManagedByAnnotation: p.ConfigName,
			PlanHashAnnotation:  p.PlanHash,
		}).
		WithSpec(applyextv1.CustomResourceDefinitionSpec().
			WithConversion(applyextv1.CustomResourceConversion().
				WithStrategy(extv1.WebhookConverter).
				WithWebhook(applyextv1.WebhookConversion().
					WithClientConfig(applyextv1.WebhookClientConfig().
						WithService(applyextv1.ServiceReference().
							WithName(p.ServiceName).WithNamespace(p.ServiceNamespace).WithPath(p.Path).WithPort(p.Port)).
						WithCABundle(caBundleBytes...)).
					WithConversionReviewVersions(p.ReviewVersions...)))), nil
}

// BuildXRDRevertPatch renders the patch that resets an XRD's
// spec.conversion to strategy=None, relinquishing this operator's
// ownership of the field.
func BuildXRDRevertPatch(targetName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": xrdadapter.GroupVersionKind.GroupVersion().String(),
		"kind":       xrdadapter.GroupVersionKind.Kind,
		"metadata":   map[string]any{"name": targetName},
		"spec": map[string]any{
			"conversion": map[string]any{"strategy": "None"},
		},
	}}
}

// BuildCRDRevertPatch is BuildXRDRevertPatch's sibling for a native CRD.
func BuildCRDRevertPatch(targetName string) *applyextv1.CustomResourceDefinitionApplyConfiguration {
	return applyextv1.CustomResourceDefinition(targetName).
		WithSpec(applyextv1.CustomResourceDefinitionSpec().
			WithConversion(applyextv1.CustomResourceConversion().WithStrategy(extv1.NoneConverter)))
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
