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

package webhook

import (
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

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

func newFakeClient(objs ...runtime.Object) *fake.ClientBuilder {
	return fake.NewClientBuilder().WithScheme(newScheme()).WithRuntimeObjects(objs...)
}

// establishedXRD mirrors internal/controller's fixture of the same name: a
// hub version (v2) and a spoke version (v1) that differ by exactly one
// renamed field, with a live schema an engine.Analyze call can validate
// rules against.
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
