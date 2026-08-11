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

// Package cli implements the convctl command tree. It works entirely
// offline against local YAML files — an XRD, an XRDConversionConfig, and a
// directory of sample objects — by importing pkg/engine directly, so it
// exercises exactly the same conversion code path used in-cluster by the
// controller and the webhook server, never a reimplementation of it.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
			if apiVersion == "" {
				return fmt.Errorf("%s (document %d): missing apiVersion; samples must declare which version they represent", rel, i)
			}
			samples = append(samples, Sample{File: rel, Index: i, Object: doc, Version: versionFromAPIVersion(apiVersion)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return samples, nil
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
