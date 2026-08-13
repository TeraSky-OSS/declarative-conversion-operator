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
	"os"
	"path/filepath"
	"testing"
)

func TestRunConvert_SpokeToHub(t *testing.T) {
	out, err := RunConvert(ConvertOptions{
		XRDPath:    "testdata/xrd.yaml",
		ConfigPath: "testdata/config.yaml",
		SamplePath: "testdata/samples/sample1-v1.yaml",
		To:         "v2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["apiVersion"] != "example.org/v2" {
		t.Fatalf("expected apiVersion to be restamped to the target version, got %#v", out["apiVersion"])
	}
	if out["kind"] != "Foo" {
		t.Fatalf("expected kind to survive the conversion, got %#v", out["kind"])
	}
	spec, ok := out["spec"].(map[string]any)
	if !ok {
		t.Fatalf("expected a spec in the output, got %#v", out["spec"])
	}
	if spec["storageGB"] != "100" {
		t.Fatalf("expected spec.storageSize to be renamed to spec.storageGB, got %#v", spec)
	}
	if _, stillThere := spec["storageSize"]; stillThere {
		t.Fatalf("expected spec.storageSize not to survive the rename, got %#v", spec)
	}
}

func TestRunConvert_HubToSpokeDropsLossyField(t *testing.T) {
	out, err := RunConvert(ConvertOptions{
		XRDPath:    "testdata/xrd.yaml",
		ConfigPath: "testdata/config.yaml",
		SamplePath: "testdata/samples/sample2-v2.yaml",
		To:         "v1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["apiVersion"] != "example.org/v1" {
		t.Fatalf("expected apiVersion example.org/v1, got %#v", out["apiVersion"])
	}
	spec, ok := out["spec"].(map[string]any)
	if !ok {
		t.Fatalf("expected a spec in the output, got %#v", out["spec"])
	}
	if spec["storageSize"] != "200" {
		t.Fatalf("expected spec.storageGB renamed to spec.storageSize, got %#v", spec)
	}
	if _, present := spec["legacyDebugFlag"]; present {
		t.Fatalf("expected the acknowledged-lossy Delete rule to drop spec.legacyDebugFlag, got %#v", spec)
	}
}

// TestRunConvert_FromOverride pins the --from escape hatch: the sample
// asserts v1, but converting it as if it were a hub object must take the
// hub->spoke direction instead.
func TestRunConvert_FromOverride(t *testing.T) {
	out, err := RunConvert(ConvertOptions{
		XRDPath:    "testdata/xrd.yaml",
		ConfigPath: "testdata/config.yaml",
		SamplePath: "testdata/samples/sample1-v1.yaml",
		From:       "v2",
		To:         "v1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec, ok := out["spec"].(map[string]any); ok {
		if _, renamed := spec["storageSize"]; renamed {
			t.Fatalf("expected no spec.storageSize: the input had no spec.storageGB to rename, got %#v", spec)
		}
	}
	if out["apiVersion"] != "example.org/v1" {
		t.Fatalf("expected apiVersion example.org/v1, got %#v", out["apiVersion"])
	}
}

func TestRunConvert_CRDConfig(t *testing.T) {
	out, err := RunConvert(ConvertOptions{
		CRDPath:    "testdata/crd.yaml",
		ConfigPath: "testdata/crdconfig.yaml",
		SamplePath: "testdata/crdsamples/sample1-v1.yaml",
		To:         "v2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["apiVersion"] != "example.org/v2" {
		t.Fatalf("expected apiVersion example.org/v2, got %#v", out["apiVersion"])
	}
}

func TestRunConvert_UnknownTargetVersion(t *testing.T) {
	_, err := RunConvert(ConvertOptions{
		XRDPath:    "testdata/xrd.yaml",
		ConfigPath: "testdata/config.yaml",
		SamplePath: "testdata/samples/sample1-v1.yaml",
		To:         "v99",
	})
	if err == nil {
		t.Fatalf("expected an error converting to a version with no compiled plan")
	}
}

func TestRunConvert_MultiDocumentSampleIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "two.yaml")
	data, err := os.ReadFile("testdata/samples/sample1-v1.yaml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(path, append(append([]byte{}, data...), append([]byte("---\n"), data...)...), 0o600); err != nil {
		t.Fatalf("write temp sample: %v", err)
	}
	if _, err := loadSingleSample(path); err == nil {
		t.Fatalf("expected a multi-document sample to be rejected")
	}
}

func TestRunConvert_MissingTo(t *testing.T) {
	_, err := RunConvert(ConvertOptions{
		XRDPath:    "testdata/xrd.yaml",
		ConfigPath: "testdata/config.yaml",
		SamplePath: "testdata/samples/sample1-v1.yaml",
	})
	if err == nil {
		t.Fatalf("expected an error when --to is missing")
	}
}
