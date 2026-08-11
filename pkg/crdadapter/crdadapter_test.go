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

package crdadapter

import (
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestVersions(t *testing.T) {
	crd := &extv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "xdatabases.example.org", Generation: 5},
		Spec: extv1.CustomResourceDefinitionSpec{
			Versions: []extv1.CustomResourceDefinitionVersion{
				{
					Name: "v2", Served: true, Storage: true,
					Schema: &extv1.CustomResourceValidation{
						OpenAPIV3Schema: &extv1.JSONSchemaProps{
							Type: "object",
							Properties: map[string]extv1.JSONSchemaProps{
								"spec": {
									Type:       "object",
									Properties: map[string]extv1.JSONSchemaProps{"storageGB": {Type: "string"}},
								},
							},
						},
					},
				},
				{
					Name: "v1", Served: true, Storage: false,
					Schema: &extv1.CustomResourceValidation{
						OpenAPIV3Schema: &extv1.JSONSchemaProps{
							Type: "object",
							Properties: map[string]extv1.JSONSchemaProps{
								"spec": {
									Type:       "object",
									Properties: map[string]extv1.JSONSchemaProps{"storageSize": {Type: "string"}},
								},
							},
						},
					},
				},
			},
			Conversion: nil,
		},
		Status: extv1.CustomResourceDefinitionStatus{
			Conditions: []extv1.CustomResourceDefinitionCondition{
				{Type: extv1.Established, Status: extv1.ConditionTrue},
			},
		},
	}

	src := New(crd)
	if src.Describe().Name != "xdatabases.example.org" || src.Describe().Generation != 5 {
		t.Fatalf("unexpected descriptor: %+v", src.Describe())
	}
	versions, err := src.Versions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions))
	}
	if versions[0].Name != "v2" || !versions[0].Storage || !versions[0].Served {
		t.Fatalf("unexpected v2: %+v", versions[0])
	}
	if versions[0].Schema == nil || versions[0].Schema.Properties["spec"].Properties["storageGB"].Type != "string" {
		t.Fatalf("unexpected v2 schema: %+v", versions[0].Schema)
	}
	if versions[1].Name != "v1" || versions[1].Storage {
		t.Fatalf("unexpected v1: %+v", versions[1])
	}

	if !Established(crd) {
		t.Fatalf("expected Established() to be true")
	}
}

func TestEstablished_FalseWithoutCondition(t *testing.T) {
	crd := &extv1.CustomResourceDefinition{}
	if Established(crd) {
		t.Fatalf("expected Established() to be false for a CRD with no conditions at all")
	}

	crd.Status.Conditions = []extv1.CustomResourceDefinitionCondition{
		{Type: extv1.Established, Status: extv1.ConditionFalse},
	}
	if Established(crd) {
		t.Fatalf("expected Established() to be false when the condition is explicitly False")
	}
}

func TestVersions_NoSchemaLeavesNilSchema(t *testing.T) {
	crd := &extv1.CustomResourceDefinition{
		Spec: extv1.CustomResourceDefinitionSpec{
			Versions: []extv1.CustomResourceDefinitionVersion{
				{Name: "v1", Served: true, Storage: true},
			},
		},
	}
	versions, err := New(crd).Versions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 1 || versions[0].Schema != nil {
		t.Fatalf("expected a single version with a nil schema, got %+v", versions)
	}
}
