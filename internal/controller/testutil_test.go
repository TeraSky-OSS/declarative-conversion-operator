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
	corev1 "k8s.io/api/core/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// newScheme returns a scheme carrying every type these controllers read or
// write, mirroring cmd/manager's init().
func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	must(clientgoscheme.AddToScheme(s))
	must(teraskyv1alpha1.AddToScheme(s))
	must(extv1.AddToScheme(s))
	return s
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// newFakeClient builds a fake client with status subresource tracking
// enabled for every custom type these reconcilers Status().Patch — without
// this, the fake client's Update would silently clobber (rather than
// preserve) status on a spec-only Update, unlike a real API server.
func newFakeClient(initObjs ...runtime.Object) *fake.ClientBuilder {
	return fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithStatusSubresource(&teraskyv1alpha1.XRDConversionConfig{}, &teraskyv1alpha1.CRDConversionConfig{}, &teraskyv1alpha1.ConversionWebhookServer{}).
		WithRuntimeObjects(initObjs...)
}

// establishedXRD returns a minimal, structurally valid unstructured
// CompositeResourceDefinition with a hub version ("v2") and a spoke version
// ("v1") differing by exactly one renamed field, and status.conditions
// marking it Established — enough for xrdadapter.New/Established and
// engine.Analyze to succeed end to end.
func establishedXRD(name string) *unstructured.Unstructured {
	xrd := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name, "generation": int64(1)},
		"spec": map[string]any{
			"scope": "Namespaced",
			"versions": []any{
				map[string]any{
					"name": "v2", "served": true, "referenceable": true,
					"schema": map[string]any{"openAPIV3Schema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"spec": map[string]any{"type": "object", "properties": map[string]any{
								"size": map[string]any{"type": "string"},
							}},
						},
					}},
				},
				map[string]any{
					"name": "v1", "served": true, "referenceable": false,
					"schema": map[string]any{"openAPIV3Schema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"spec": map[string]any{"type": "object", "properties": map[string]any{
								"storageSize": map[string]any{"type": "string"},
							}},
						},
					}},
				},
			},
		},
		"status": map[string]any{
			"conditions": []any{map[string]any{"type": "Established", "status": "True"}},
		},
	}}
	xrd.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	return xrd
}

// renameRuleXRDConfig returns an XRDConversionConfig that maps
// establishedXRD's hub spec.size to spoke v1's spec.storageSize.
func renameRuleXRDConfig(name, targetXRD string) *teraskyv1alpha1.XRDConversionConfig {
	return &teraskyv1alpha1.XRDConversionConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: teraskyv1alpha1.XRDConversionConfigSpec{
			TargetXRD:  teraskyv1alpha1.TargetXRDRef{Name: targetXRD},
			HubVersion: "v2",
			Spokes: []teraskyv1alpha1.SpokeVersionRules{{
				Version: "v1",
				Rules: []teraskyv1alpha1.ConversionRule{{
					Strategy:    teraskyv1alpha1.StrategyFieldRename,
					FieldRename: &teraskyv1alpha1.FieldRenameParams{HubPath: "spec.size", SpokePath: "spec.storageSize"},
				}},
			}},
		},
	}
}

// establishedCRD is establishedXRD's typed counterpart for
// CRDConversionConfigReconciler.
func establishedCRD(name string) *extv1.CustomResourceDefinition {
	return &extv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec: extv1.CustomResourceDefinitionSpec{
			Group: "example.org",
			Names: extv1.CustomResourceDefinitionNames{Plural: "foos", Kind: "Foo"},
			Scope: extv1.NamespaceScoped,
			Versions: []extv1.CustomResourceDefinitionVersion{
				{
					Name: "v2", Served: true, Storage: true,
					Schema: &extv1.CustomResourceValidation{OpenAPIV3Schema: &extv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]extv1.JSONSchemaProps{
							"spec": {Type: "object", Properties: map[string]extv1.JSONSchemaProps{
								"size": {Type: "string"},
							}},
						},
					}},
				},
				{
					Name: "v1", Served: true, Storage: false,
					Schema: &extv1.CustomResourceValidation{OpenAPIV3Schema: &extv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]extv1.JSONSchemaProps{
							"spec": {Type: "object", Properties: map[string]extv1.JSONSchemaProps{
								"storageSize": {Type: "string"},
							}},
						},
					}},
				},
			},
		},
		Status: extv1.CustomResourceDefinitionStatus{
			Conditions: []extv1.CustomResourceDefinitionCondition{
				{Type: extv1.Established, Status: extv1.ConditionTrue},
			},
		},
	}
}

func renameRuleCRDConfig(name, targetCRD string) *teraskyv1alpha1.CRDConversionConfig {
	return &teraskyv1alpha1.CRDConversionConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: teraskyv1alpha1.CRDConversionConfigSpec{
			TargetCRD:  teraskyv1alpha1.TargetCRDRef{Name: targetCRD},
			HubVersion: "v2",
			Spokes: []teraskyv1alpha1.SpokeVersionRules{{
				Version: "v1",
				Rules: []teraskyv1alpha1.ConversionRule{{
					Strategy:    teraskyv1alpha1.StrategyFieldRename,
					FieldRename: &teraskyv1alpha1.FieldRenameParams{HubPath: "spec.size", SpokePath: "spec.storageSize"},
				}},
			}},
		},
	}
}

// readyServer returns a ConversionWebhookServer whose status already
// reports every isServerReady gate as satisfied, plus a certificate Secret
// containing a CA bundle — everything XRDConversionConfigReconciler and
// CRDConversionConfigReconciler need past their health gates.
func readyServer(name string) (*teraskyv1alpha1.ConversionWebhookServer, *corev1.Secret) {
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: true, Namespace: "operator-ns"},
		Status: teraskyv1alpha1.ConversionWebhookServerStatus{
			ReadyReplicas: 2,
			Conditions: []metav1.Condition{
				{Type: teraskyv1alpha1.CWSConditionAvailable, Status: metav1.ConditionTrue},
				{Type: teraskyv1alpha1.CWSConditionCertificateReady, Status: metav1.ConditionTrue},
				{Type: teraskyv1alpha1.CWSConditionServiceReady, Status: metav1.ConditionTrue},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cwsCertificateSecretName(name), Namespace: server.Spec.Namespace},
		Data:       map[string][]byte{"ca.crt": []byte("fake-ca-bundle")},
	}
	return server, secret
}
