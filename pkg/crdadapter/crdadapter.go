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

// Package crdadapter implements engine.SchemaSource by reading a live
// plain Kubernetes apiextensions.k8s.io/v1 CustomResourceDefinition. It is
// pkg/xrdadapter's sibling for CRDConversionConfig: the same seam
// (pkg/engine never imports either adapter, only the SchemaSource
// interface) that lets pkg/engine stay entirely agnostic of which kind of
// resource it's converting.
//
// Unlike pkg/xrdadapter, this adapter works with the real, already-vendored
// apiextensions.k8s.io/v1 Go types directly rather than unstructured
// content — a CustomResourceDefinition is a core Kubernetes API type this
// module already depends on (via k8s.io/apiextensions-apiserver, used
// elsewhere for JSONSchemaProps and structural-schema validation), so
// there's no equivalent reason to avoid a typed dependency the way
// pkg/xrdadapter avoids vendoring Crossplane.
package crdadapter

import (
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// GroupVersionKind identifies a plain Kubernetes CustomResourceDefinition.
var GroupVersionKind = schema.GroupVersionKind{
	Group:   "apiextensions.k8s.io",
	Version: "v1",
	Kind:    "CustomResourceDefinition",
}

// GroupVersionResource is the CRD's list/watch resource identity.
var GroupVersionResource = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// Source implements engine.SchemaSource by reading a live
// CustomResourceDefinition. The schema handed to the engine for each
// version is the whole per-version openAPIV3Schema root (not narrowed to
// .properties.spec), matching pkg/xrdadapter, so rule paths written as
// "spec.foo" resolve exactly as declared.
type Source struct {
	CRD *extv1.CustomResourceDefinition
}

// New wraps a live CustomResourceDefinition as an engine.SchemaSource.
func New(crd *extv1.CustomResourceDefinition) *Source {
	return &Source{CRD: crd}
}

// Describe implements engine.SchemaSource.
func (s *Source) Describe() engine.ResourceDescriptor {
	return engine.ResourceDescriptor{Kind: "CRD", Name: s.CRD.Name, Generation: s.CRD.Generation}
}

// Versions implements engine.SchemaSource.
func (s *Source) Versions() ([]engine.VersionSchema, error) {
	out := make([]engine.VersionSchema, 0, len(s.CRD.Spec.Versions))
	for _, v := range s.CRD.Spec.Versions {
		var jsonSchema *extv1.JSONSchemaProps
		if v.Schema != nil {
			jsonSchema = v.Schema.OpenAPIV3Schema
		}
		out = append(out, engine.VersionSchema{
			Name:    v.Name,
			Schema:  jsonSchema,
			Served:  v.Served,
			Storage: v.Storage,
		})
	}
	return out, nil
}

// Established reports whether the CRD's own Established condition is
// True, used by the controller as a health gate before ever patching it —
// the exact native-CRD equivalent of xrdadapter.Established.
func Established(crd *extv1.CustomResourceDefinition) bool {
	for _, c := range crd.Status.Conditions {
		if c.Type == extv1.Established {
			return c.Status == extv1.ConditionTrue
		}
	}
	return false
}
