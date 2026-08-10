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

package engine

import (
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func strSchema() extv1.JSONSchemaProps  { return extv1.JSONSchemaProps{Type: "string"} }
func boolSchema() extv1.JSONSchemaProps { return extv1.JSONSchemaProps{Type: "boolean"} }

func objSchema(props map[string]extv1.JSONSchemaProps, required ...string) extv1.JSONSchemaProps {
	return extv1.JSONSchemaProps{Type: "object", Properties: props, Required: required}
}

func openMapSchema() extv1.JSONSchemaProps {
	allow := true
	return extv1.JSONSchemaProps{Type: "object", AdditionalProperties: &extv1.JSONSchemaPropsOrBool{Allows: allow}}
}

func arrSchema(item extv1.JSONSchemaProps, maxItems *int64) extv1.JSONSchemaProps {
	s := extv1.JSONSchemaProps{Type: "array", Items: &extv1.JSONSchemaPropsOrArray{Schema: &item}}
	s.MaxItems = maxItems
	return s
}

func i64(n int64) *int64 { return &n }

type fakeSource struct {
	versions []VersionSchema
	gen      int64
}

func (f fakeSource) Versions() ([]VersionSchema, error) { return f.versions, nil }
func (f fakeSource) Describe() ResourceDescriptor {
	return ResourceDescriptor{Kind: "Fake", Name: "fake", Generation: f.gen}
}

func diagMessages(diags []Diagnostic, sev Severity) []string {
	var out []string
	for _, d := range diags {
		if d.Severity == sev {
			out = append(out, d.Message)
		}
	}
	return out
}
