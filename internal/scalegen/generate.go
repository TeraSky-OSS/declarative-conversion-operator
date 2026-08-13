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

package scalegen

import (
	"fmt"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1a "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

const (
	Group      = "scale.dco.example.org"
	HubVersion = "v3"
	V1         = "v1"
	V2         = "v2"
	// Category is set on every generated CRD so `kubectl get widgets -n dco-scale`
	// lists instances of all scale-gen types together.
	Category = "widgets"
)

// Target is one generated CRD plus its conversion config and instance template.
type Target struct {
	Index   int
	Plural  string
	Kind    string
	CRDName string
	V1Slots []Slot
	V2Slots []Slot
	CRD     *extv1.CustomResourceDefinition
	Config  *v1a.CRDConversionConfig
	SpokeV1 map[string]any
}

func crdName(i int) (plural, kind, name string) {
	kind = fmt.Sprintf("Widget%03d", i)
	plural = fmt.Sprintf("widget%03ds", i)
	return plural, kind, plural + "." + Group
}

func schemaFor(props map[string]extv1.JSONSchemaProps) *extv1.CustomResourceValidation {
	return &extv1.CustomResourceValidation{OpenAPIV3Schema: &extv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"spec": {Type: "object", Properties: props},
		},
	}}
}

// BuildTargets generates `targets` CRDs with 3 versions and 3–10 strategies per spoke.
func BuildTargets(targets, minN, maxN int, seed int64) ([]Target, error) {
	v1s, v2s, err := Assign(targets, minN, maxN, seed)
	if err != nil {
		return nil, err
	}
	out := make([]Target, targets)
	for i := 0; i < targets; i++ {
		plural, kind, name := crdName(i)
		hubProps, v1Props, v2Props := map[string]extv1.JSONSchemaProps{}, map[string]extv1.JSONSchemaProps{}, map[string]extv1.JSONSchemaProps{}
		spokeSpec := map[string]any{}
		v1Rules, v2Rules := make([]v1a.ConversionRule, 0, len(v1s[i])), make([]v1a.ConversionRule, 0, len(v2s[i]))
		for _, s := range v1s[i] {
			mergeProps(hubProps, s.HubProps)
			mergeProps(v1Props, s.SpokeProps)
			mergeSpec(spokeSpec, s.SpokeSpec)
			v1Rules = append(v1Rules, s.Rule)
		}
		for _, s := range v2s[i] {
			mergeProps(hubProps, s.HubProps)
			mergeProps(v2Props, s.SpokeProps)
			v2Rules = append(v2Rules, s.Rule)
		}
		crd := &extv1.CustomResourceDefinition{
			TypeMeta:   metav1.TypeMeta{APIVersion: "apiextensions.k8s.io/v1", Kind: "CustomResourceDefinition"},
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: extv1.CustomResourceDefinitionSpec{
				Group: Group, Names: extv1.CustomResourceDefinitionNames{
					Plural: plural, Singular: fmt.Sprintf("widget%03d", i), Kind: kind,
					Categories: []string{Category},
				},
				Scope: extv1.NamespaceScoped,
				Versions: []extv1.CustomResourceDefinitionVersion{
					{Name: HubVersion, Served: true, Storage: true, Schema: schemaFor(hubProps)},
					{Name: V1, Served: true, Storage: false, Schema: schemaFor(v1Props)},
					{Name: V2, Served: true, Storage: false, Schema: schemaFor(v2Props)},
				},
			},
		}
		cfg := &v1a.CRDConversionConfig{
			TypeMeta:   metav1.TypeMeta{APIVersion: v1a.GroupVersion.String(), Kind: "CRDConversionConfig"},
			ObjectMeta: metav1.ObjectMeta{Name: name + "-conversion"},
			Spec: v1a.CRDConversionConfigSpec{
				TargetCRD:           v1a.TargetCRDRef{Name: name},
				HubVersion:          HubVersion,
				UnmappedFieldPolicy: v1a.UnmappedFieldPolicyWarn,
				UnmappedFieldReason: "scale-gen hub schema is the union of both spokes; leftover fields are unclaimed by design",
				Spokes: []v1a.SpokeVersionRules{
					{Version: V1, Rules: v1Rules},
					{Version: V2, Rules: v2Rules},
				},
			},
		}
		out[i] = Target{Index: i, Plural: plural, Kind: kind, CRDName: name, V1Slots: v1s[i], V2Slots: v2s[i], CRD: crd, Config: cfg, SpokeV1: spokeSpec}
	}
	return out, nil
}

func (t Target) Instance(ns string, n int) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Group + "/" + V1,
		"kind":       t.Kind,
		"metadata":   map[string]any{"name": fmt.Sprintf("obj-%03d", n), "namespace": ns},
		"spec":       t.SpokeV1,
	}}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: Group, Version: V1, Kind: t.Kind})
	return u
}

func (t Target) GVR(version string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: Group, Version: version, Resource: t.Plural}
}

func StrategyCoverage(ts []Target) map[v1a.Strategy]int {
	seen := map[v1a.Strategy]int{}
	for _, t := range ts {
		for _, s := range t.V1Slots {
			seen[s.Name]++
		}
		for _, s := range t.V2Slots {
			seen[s.Name]++
		}
	}
	return seen
}
