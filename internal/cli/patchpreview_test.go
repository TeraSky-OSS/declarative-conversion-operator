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

package cli

import (
	"encoding/base64"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

func basePreviewOptions() PatchPreviewOptions {
	return PatchPreviewOptions{
		ConfigPath:       "testdata/config.yaml",
		ServiceName:      "prod-webhook-server",
		ServiceNamespace: "conversion-system",
		CABundle:         base64.StdEncoding.EncodeToString([]byte("PEM")),
	}
}

func decodePreview(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := sigsyaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("preview is not valid YAML: %v\n%s", err, data)
	}
	return out
}

func TestRunPatchPreview_XRDDefaults(t *testing.T) {
	data, err := RunPatchPreview(basePreviewOptions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	obj := decodePreview(t, data)

	if obj["apiVersion"] != "apiextensions.crossplane.io/v2" || obj["kind"] != "CompositeResourceDefinition" {
		t.Fatalf("unexpected target type: %v/%v", obj["apiVersion"], obj["kind"])
	}
	name, _, _ := unstructured.NestedString(obj, "metadata", "name")
	if name != "xfoos.example.org" {
		t.Fatalf("expected the patch to target the config's XRD, got %q", name)
	}
	path, _, _ := unstructured.NestedString(obj, "spec", "conversion", "webhook", "clientConfig", "service", "path")
	if path != "/convert/xfoos.example.org" {
		t.Fatalf("expected the path to default to /convert/<target>, got %q", path)
	}
	port, _, _ := unstructured.NestedFloat64(obj, "spec", "conversion", "webhook", "clientConfig", "service", "port")
	if port != 443 {
		t.Fatalf("expected the port to default to 443, got %v", port)
	}
	versions, _, _ := unstructured.NestedStringSlice(obj, "spec", "conversion", "webhook", "conversionReviewVersions")
	if len(versions) != 1 || versions[0] != "v1" {
		t.Fatalf("expected conversionReviewVersions to default to [v1], got %v", versions)
	}
}

func TestRunPatchPreview_ExplicitOverrides(t *testing.T) {
	opts := basePreviewOptions()
	opts.Path = "/custom/route"
	opts.Port = 8443
	opts.PlanHash = "sha256:deadbeef"

	obj := decodePreview(t, mustPreview(t, opts))
	path, _, _ := unstructured.NestedString(obj, "spec", "conversion", "webhook", "clientConfig", "service", "path")
	if path != "/custom/route" {
		t.Fatalf("path = %q", path)
	}
	port, _, _ := unstructured.NestedFloat64(obj, "spec", "conversion", "webhook", "clientConfig", "service", "port")
	if port != 8443 {
		t.Fatalf("port = %v", port)
	}
	hash, _, _ := unstructured.NestedString(obj, "metadata", "annotations", "conversion.terasky.com/plan-hash")
	if hash != "sha256:deadbeef" {
		t.Fatalf("plan-hash annotation = %q", hash)
	}
}

// TestRunPatchPreview_RawPEMCABundle covers the convenience the flag
// documents: a raw PEM bundle is encoded on the way in, while an
// already-encoded one is passed through untouched.
func TestRunPatchPreview_RawPEMCABundle(t *testing.T) {
	raw := "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----"
	opts := basePreviewOptions()
	opts.CABundle = raw

	obj := decodePreview(t, mustPreview(t, opts))
	got, _, _ := unstructured.NestedString(obj, "spec", "conversion", "webhook", "clientConfig", "caBundle")
	decoded, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("expected a base64 caBundle, got %q: %v", got, err)
	}
	if string(decoded) != raw {
		t.Fatalf("decoded caBundle = %q, want the raw PEM back", decoded)
	}
}

func TestRunPatchPreview_ValidatesAgainstSchemaWhenGiven(t *testing.T) {
	opts := basePreviewOptions()
	opts.XRDPath = "testdata/xrd.yaml"
	if _, err := RunPatchPreview(opts); err != nil {
		t.Fatalf("a valid config should preview cleanly: %v", err)
	}

	// The same schema with the rename rule dropped leaves fields
	// uncovered, which the operator would refuse to apply — so should
	// this.
	opts.ConfigPath = "testdata/config-norename.yaml"
	if _, err := RunPatchPreview(opts); err == nil {
		t.Fatalf("expected a config that fails schema validation to be refused a preview")
	}
}

func TestRunPatchPreview_CRDConfig(t *testing.T) {
	opts := basePreviewOptions()
	opts.ConfigPath = "testdata/crdconfig.yaml"
	opts.CRDPath = "testdata/crd.yaml"

	obj := decodePreview(t, mustPreview(t, opts))
	if obj["apiVersion"] != "apiextensions.k8s.io/v1" || obj["kind"] != "CustomResourceDefinition" {
		t.Fatalf("unexpected target type: %v/%v", obj["apiVersion"], obj["kind"])
	}
	strategy, _, _ := unstructured.NestedString(obj, "spec", "conversion", "strategy")
	if strategy != "Webhook" {
		t.Fatalf("strategy = %q", strategy)
	}
}

func TestRunPatchPreview_RequiredFlags(t *testing.T) {
	noService := basePreviewOptions()
	noService.ServiceName = ""
	noNamespace := basePreviewOptions()
	noNamespace.ServiceNamespace = ""
	noCA := basePreviewOptions()
	noCA.CABundle = ""
	wrongSchemaFlag := basePreviewOptions()
	wrongSchemaFlag.CRDPath = "testdata/crd.yaml"

	cases := map[string]PatchPreviewOptions{
		"missing service name":      noService,
		"missing service namespace": noNamespace,
		"missing ca bundle":         noCA,
		"crd flag on an xrd config": wrongSchemaFlag,
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := RunPatchPreview(opts); err == nil {
				t.Fatalf("expected an error for %s", name)
			}
		})
	}
}

func mustPreview(t *testing.T, opts PatchPreviewOptions) []byte {
	t.Helper()
	data, err := RunPatchPreview(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return data
}
