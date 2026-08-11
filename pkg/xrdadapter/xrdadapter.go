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

// Package xrdadapter is the only package in this repository that knows
// Crossplane XRDs exist. It implements engine.SchemaSource by reading a
// live CompositeResourceDefinition, so pkg/engine itself stays entirely
// agnostic of Crossplane — a future adapter for native Kubernetes CRDs
// (backing a CRDConversionConfig) would implement the same interface
// without any change to pkg/engine.
//
// XRDs are read generically via unstructured content rather than by
// vendoring Crossplane's own Go module: the fields this package needs
// (spec.versions[].{name,served,referenceable,schema.openAPIV3Schema} and
// metadata.generation) are a small, stable subset of the XRD shape, and
// avoiding the dependency means this operator tracks no compatibility
// matrix against crossplane-runtime's release cadence.
package xrdadapter

import (
	"fmt"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// GroupVersionKind identifies a Crossplane CompositeResourceDefinition.
//
// This targets Crossplane v2's XRD API (apiextensions.crossplane.io/v2),
// not the older v1 API — Crossplane 2.0 introduced a breaking XRD schema
// change (notably an explicit spec.scope field, defaulting composites to
// Namespaced instead of always-cluster-scoped) but kept spec.conversion
// and spec.versions[].{name,served,referenceable,schema} unchanged, which
// is all this package reads. Source.Versions never reads spec.scope, so
// it behaves identically for Namespaced, Cluster, and LegacyCluster XRDs.
var GroupVersionKind = schema.GroupVersionKind{
	Group:   "apiextensions.crossplane.io",
	Version: "v2",
	Kind:    "CompositeResourceDefinition",
}

// GroupVersionResource is the XRD's list/watch resource identity.
var GroupVersionResource = schema.GroupVersionResource{
	Group:    "apiextensions.crossplane.io",
	Version:  "v2",
	Resource: "compositeresourcedefinitions",
}

// Source implements engine.SchemaSource by reading a live
// CompositeResourceDefinition. The schema handed to the engine for each
// version is the whole per-version openAPIV3Schema root (not narrowed to
// .properties.spec) so that rule paths written as "spec.foo" in an
// XRDConversionConfig resolve exactly as declared.
type Source struct {
	XRD *unstructured.Unstructured
}

// New wraps a live XRD object as an engine.SchemaSource.
func New(xrd *unstructured.Unstructured) *Source {
	return &Source{XRD: xrd}
}

// Describe implements engine.SchemaSource.
func (s *Source) Describe() engine.ResourceDescriptor {
	return engine.ResourceDescriptor{Kind: "XRD", Name: s.XRD.GetName(), Generation: s.XRD.GetGeneration()}
}

// Versions implements engine.SchemaSource.
func (s *Source) Versions() ([]engine.VersionSchema, error) {
	versionsRaw, found, err := unstructured.NestedSlice(s.XRD.Object, "spec", "versions")
	if err != nil {
		return nil, fmt.Errorf("xrdadapter: reading spec.versions: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("xrdadapter: XRD %q has no spec.versions", s.XRD.GetName())
	}

	out := make([]engine.VersionSchema, 0, len(versionsRaw))
	for i, raw := range versionsRaw {
		vm, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("xrdadapter: spec.versions[%d] is not an object", i)
		}
		name, _, err := unstructured.NestedString(vm, "name")
		if err != nil {
			return nil, fmt.Errorf("xrdadapter: spec.versions[%d].name: %w", i, err)
		}
		served, _, err := unstructured.NestedBool(vm, "served")
		if err != nil {
			return nil, fmt.Errorf("xrdadapter: spec.versions[%d].served: %w", i, err)
		}
		// referenceable maps directly to the generated CRD's storage
		// field, and is exactly the "hub" version concept this operator
		// uses throughout.
		referenceable, _, err := unstructured.NestedBool(vm, "referenceable")
		if err != nil {
			return nil, fmt.Errorf("xrdadapter: spec.versions[%d].referenceable: %w", i, err)
		}

		var jsonSchema *extv1.JSONSchemaProps
		schemaMap, found, err := unstructured.NestedMap(vm, "schema", "openAPIV3Schema")
		if err != nil {
			return nil, fmt.Errorf("xrdadapter: spec.versions[%d].schema.openAPIV3Schema: %w", i, err)
		}
		if found {
			js := &extv1.JSONSchemaProps{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(schemaMap, js); err != nil {
				return nil, fmt.Errorf("xrdadapter: spec.versions[%d].schema.openAPIV3Schema: converting: %w", i, err)
			}
			jsonSchema = js
		}

		out = append(out, engine.VersionSchema{
			Name:    name,
			Schema:  jsonSchema,
			Served:  served,
			Storage: referenceable,
		})
	}
	return out, nil
}

// Established reports whether the XRD's Established condition is True,
// used by the controller as a health gate before ever patching the XRD.
func Established(xrd *unstructured.Unstructured) bool {
	conditions, found, err := unstructured.NestedSlice(xrd.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, c := range conditions {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, _, _ := unstructured.NestedString(cm, "type"); t == "Established" {
			status, _, _ := unstructured.NestedString(cm, "status")
			return status == "True"
		}
	}
	return false
}
