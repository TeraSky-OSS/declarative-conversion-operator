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

// Package cli implements the convctl command tree for TeraSky's
// declarative-conversion-operator. It drives pkg/engine against local YAML
// (an XRDConversionConfig or CRDConversionConfig, the matching XRD/CRD schema,
// and optional sample objects) — the same conversion code path used in-cluster
// by the controller and webhook server, never a reimplementation of it.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

// LoadXRD reads a single YAML document from path and returns it as an
// unstructured object, the same shape pkg/xrdadapter reads from a live
// cluster.
func LoadXRD(path string) (*unstructured.Unstructured, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var m map[string]any
	if err := sigsyaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &unstructured.Unstructured{Object: m}, nil
}

// LoadConfig reads a single XRDConversionConfig YAML document from path.
func LoadConfig(path string) (*teraskyv1alpha1.XRDConversionConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var cfg teraskyv1alpha1.XRDConversionConfig
	if err := sigsyaml.UnmarshalStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &cfg, nil
}

// LoadCRD reads a single CustomResourceDefinition YAML document from path,
// decoded into the typed apiextensions.k8s.io/v1 type pkg/crdadapter itself
// works with. Unlike LoadXRD, this doesn't stay unstructured: a
// CustomResourceDefinition is a core Kubernetes type this module already
// depends on, so there's no reason to avoid the typed decode the way
// pkg/xrdadapter avoids vendoring Crossplane's own types.
func LoadCRD(path string) (*extv1.CustomResourceDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var crd extv1.CustomResourceDefinition
	if err := sigsyaml.Unmarshal(data, &crd); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &crd, nil
}

// LoadCRDConfig reads a single CRDConversionConfig YAML document from path.
func LoadCRDConfig(path string) (*teraskyv1alpha1.CRDConversionConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var cfg teraskyv1alpha1.CRDConversionConfig
	if err := sigsyaml.UnmarshalStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &cfg, nil
}

// PeekConfigKind reads just a config YAML's top-level `kind` field, letting
// callers dispatch between XRDConversionConfig and CRDConversionConfig
// handling before committing to parsing the file as either concrete type.
// Fails closed on a missing or unrecognized kind rather than guessing.
func PeekConfigKind(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	var m struct {
		Kind string `json:"kind"`
	}
	if err := sigsyaml.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}
	switch m.Kind {
	case "XRDConversionConfig", "CRDConversionConfig":
		return m.Kind, nil
	case "":
		return "", fmt.Errorf("%s: missing kind (expected XRDConversionConfig or CRDConversionConfig)", path)
	default:
		return "", fmt.Errorf("%s: unrecognized kind %q (expected XRDConversionConfig or CRDConversionConfig)", path, m.Kind)
	}
}

// Sample is one loaded sample object, with the version it asserts to be
// (inferred from its own apiVersion field, matching Kubernetes convention).
type Sample struct {
	File    string
	Index   int // position within File, for multi-document files
	Object  map[string]any
	Version string
}

// LoadSamples walks dir recursively, decoding every .yaml/.yml file as a
// (possibly multi-document) stream of sample objects. Filenames are purely
// for reporting — the asserted version always comes from the object's own
// apiVersion field.
func LoadSamples(dir string) ([]Sample, error) {
	var samples []Sample
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			rel = path
		}
		docs, err := decodeAllDocuments(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		for i, doc := range docs {
			apiVersion, _ := doc["apiVersion"].(string)
			samples = append(samples, Sample{File: rel, Index: i, Object: doc, Version: versionFromAPIVersion(apiVersion)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return samples, nil
}

// filterSamplesByGVK keeps objects of the XRD/CRD's group and kind so a
// GitOps apps/ tree (XRs plus kustomization.yaml or Helm values) can be
// passed to --samples. Other documents are ignored, not errors. A document
// of the target kind with no apiVersion is an error — that object cannot
// declare which version it represents.
func filterSamplesByGVK(samples []Sample, group, kind string) ([]Sample, error) {
	var out []Sample
	for _, s := range samples {
		k, _ := s.Object["kind"].(string)
		if k != kind {
			continue
		}
		api, _ := s.Object["apiVersion"].(string)
		if api == "" {
			return nil, fmt.Errorf("%s: %s object missing apiVersion; samples must declare which version they represent", s.File, kind)
		}
		if apiGroup(api) != group {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func apiGroup(apiVersion string) string {
	if i := strings.LastIndex(apiVersion, "/"); i >= 0 {
		return apiVersion[:i]
	}
	return ""
}

func decodeAllDocuments(path string) ([]map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []map[string]any
	dec := k8syaml.NewYAMLOrJSONDecoder(f, 4096)
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if len(doc) == 0 {
			continue
		}
		out = append(out, convertYAMLMap(doc).(map[string]any))
	}
	return out, nil
}

// convertYAMLMap normalizes map[interface{}]interface{} nodes (which
// gopkg.in/yaml.v3-style decoding can produce for nested maps in some
// paths) into map[string]any, matching what JSON decoding and
// unstructured.Unstructured expect throughout this codebase.
func convertYAMLMap(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = convertYAMLMap(vv)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[fmt.Sprintf("%v", k)] = convertYAMLMap(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = convertYAMLMap(vv)
		}
		return out
	default:
		return v
	}
}
