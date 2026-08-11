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

package v1alpha1

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/install"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

// TestGeneratedCRDsAreStructural loads every controller-gen-generated CRD
// manifest and runs it through the exact same structural-schema
// validation the real kube-apiserver performs on `kubectl apply` — the
// same check that caught a real bug this way: ForEachParams.Rules is a
// recursive []ConversionRule (a rule can contain a ForEach, whose Rules
// are themselves ConversionRules), and controller-gen silently emitted an
// empty, type-less schema for the recursive reference instead of erroring
// at generation time. Neither `go build`, `go vet`, nor `kustomize build`
// (which never fully expands or validates the embedded OpenAPI schema)
// caught this — only an actual apiserver, or this same validation
// path run offline, does.
func TestGeneratedCRDsAreStructural(t *testing.T) {
	scheme := runtime.NewScheme()
	install.Install(scheme)

	matches, err := filepath.Glob(filepath.Join("..", "..", "config", "crd", "bases", "*.yaml"))
	if err != nil {
		t.Fatalf("glob CRD manifests: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("expected to find generated CRD manifests under config/crd/bases")
	}

	for _, path := range matches {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			jsonData, err := sigsyaml.YAMLToJSON(data)
			if err != nil {
				t.Fatalf("converting %s to JSON: %v", path, err)
			}
			var crd extv1.CustomResourceDefinition
			if err := k8syaml.Unmarshal(jsonData, &crd); err != nil {
				t.Fatalf("unmarshaling %s: %v", path, err)
			}

			for _, v := range crd.Spec.Versions {
				if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
					continue
				}
				var internalSchema apiextensions.JSONSchemaProps
				if err := scheme.Convert(v.Schema.OpenAPIV3Schema, &internalSchema, nil); err != nil {
					t.Fatalf("%s version %s: converting schema: %v", path, v.Name, err)
				}
				structural, err := structuralschema.NewStructural(&internalSchema)
				if err != nil {
					t.Fatalf("%s version %s: NewStructural: %v", path, v.Name, err)
				}
				if errs := structuralschema.ValidateStructural(nil, structural); len(errs) > 0 {
					t.Fatalf("%s version %s: not a valid structural schema (the apiserver would reject this CRD):\n%v", path, v.Name, errs.ToAggregate())
				}
			}
		})
	}
}
