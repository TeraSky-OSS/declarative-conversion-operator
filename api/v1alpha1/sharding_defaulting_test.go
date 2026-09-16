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
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	"k8s.io/apimachinery/pkg/runtime"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

// cwsStructural loads the generated ConversionWebhookServer CRD and
// returns the structural schema the apiserver would default against —
// the real manifest, not a hand-written approximation, so a change to the
// kubebuilder markers shows up here.
func cwsStructural(t *testing.T) *structuralschema.Structural {
	t.Helper()
	scheme := runtime.NewScheme()
	install.Install(scheme)

	path := filepath.Join("..", "..", "config", "crd", "bases", "terasky.com_conversionwebhookservers.yaml")
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
		var internal apiextensions.JSONSchemaProps
		if err := scheme.Convert(v.Schema.OpenAPIV3Schema, &internal, nil); err != nil {
			t.Fatalf("converting schema for %s: %v", v.Name, err)
		}
		s, err := structuralschema.NewStructural(&internal)
		if err != nil {
			t.Fatalf("NewStructural for %s: %v", v.Name, err)
		}
		return s
	}
	t.Fatal("the ConversionWebhookServer CRD has no versioned schema")
	return nil
}

func defaultCWS(t *testing.T, spec map[string]any) map[string]any {
	t.Helper()
	obj := map[string]any{
		"apiVersion": GroupVersion.String(),
		"kind":       "ConversionWebhookServer",
		"metadata":   map[string]any{"name": "default"},
		"spec":       spec,
	}
	structuraldefaulting.Default(obj, cwsStructural(t))
	out, _ := obj["spec"].(map[string]any)
	return out
}

// Sharding is off unless the block is written, and the `default: true` on
// spec.sharding.enabled does not change that — structural defaulting only
// descends into an object that is present, and spec.sharding itself
// carries no default.
//
// This is asserted against the apiserver's own defaulting algorithm rather
// than reasoned about, because reading `default: true` off the leaf and
// concluding "sharding is on by default" is an easy and consequential
// mistake: it would mean every install silently moved unpinned configs off
// spec.default and into a shard pool. The chart's template filters the
// block out entirely for the same reason, and ShardingEnabled() agrees
// with both.
func TestSharding_IsOffWhenTheBlockIsAbsent(t *testing.T) {
	spec := defaultCWS(t, map[string]any{"default": true})
	if _, present := spec["sharding"]; present {
		t.Fatalf("defaulting invented a sharding block: %#v", spec["sharding"])
	}

	var cws ConversionWebhookServerSpec
	if cws.ShardingEnabled() {
		t.Error("ShardingEnabled() must be false when spec.sharding is unset")
	}
}

// The other half: writing the block at all IS the opt-in, and the leaf
// default is what makes `sharding: {}` mean enabled. Both halves have to
// hold together, or the documented behaviour is only half true.
func TestSharding_EmptyBlockMeansEnabled(t *testing.T) {
	spec := defaultCWS(t, map[string]any{"sharding": map[string]any{}})
	sharding, ok := spec["sharding"].(map[string]any)
	if !ok {
		t.Fatalf("sharding block disappeared: %#v", spec)
	}
	if enabled, _ := sharding["enabled"].(bool); !enabled {
		t.Errorf("sharding.enabled defaulted to %#v, want true", sharding["enabled"])
	}
	if weight, _ := sharding["weight"].(int64); weight != 1 {
		t.Errorf("sharding.weight defaulted to %#v, want 1", sharding["weight"])
	}

	enabledTrue := true
	cws := ConversionWebhookServerSpec{Sharding: &ShardingSpec{}}
	if !cws.ShardingEnabled() {
		t.Error("ShardingEnabled() must agree with the CRD: an empty block is enabled")
	}
	cws.Sharding.Enabled = &enabledTrue
	if !cws.ShardingEnabled() {
		t.Error("ShardingEnabled() must be true for an explicit enabled: true")
	}
}

// And explicit false stays false, so the block is not a one-way door.
func TestSharding_ExplicitlyDisabled(t *testing.T) {
	spec := defaultCWS(t, map[string]any{"sharding": map[string]any{"enabled": false}})
	sharding, _ := spec["sharding"].(map[string]any)
	if enabled, _ := sharding["enabled"].(bool); enabled {
		t.Error("defaulting overwrote an explicit sharding.enabled: false")
	}

	disabled := false
	cws := ConversionWebhookServerSpec{Sharding: &ShardingSpec{Enabled: &disabled}}
	if cws.ShardingEnabled() {
		t.Error("ShardingEnabled() must be false for an explicit enabled: false")
	}
}
