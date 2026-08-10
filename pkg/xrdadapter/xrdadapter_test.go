/*
Copyright 2026 The xrd-conversion-operator Authors.

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

package xrdadapter

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestVersions(t *testing.T) {
	xrd := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.crossplane.io/v1",
		"kind":       "CompositeResourceDefinition",
		"metadata":   map[string]any{"name": "xdatabases.example.org", "generation": int64(5)},
		"spec": map[string]any{
			"versions": []any{
				map[string]any{
					"name": "v2", "served": true, "referenceable": true,
					"schema": map[string]any{
						"openAPIV3Schema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"spec": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"storageGB": map[string]any{"type": "string"},
									},
								},
							},
						},
					},
				},
				map[string]any{
					"name": "v1", "served": true, "referenceable": false,
					"schema": map[string]any{
						"openAPIV3Schema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"spec": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"storageSize": map[string]any{"type": "string"},
									},
								},
							},
						},
					},
				},
			},
		},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Established", "status": "True"},
			},
		},
	}}

	src := New(xrd)
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

	if !Established(xrd) {
		t.Fatalf("expected Established() to be true")
	}
}
