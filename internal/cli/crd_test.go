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

import "testing"

// TestRunTest_CRD_EndToEnd is TestRunTest_EndToEnd's sibling for a native
// CRD + CRDConversionConfig, proving convctl test works identically for
// both resource kinds — same fixture shape, same rules, same expected
// counts, just loaded via --crd instead of --xrd.
func TestRunTest_CRD_EndToEnd(t *testing.T) {
	rep, err := RunTest(TestOptions{
		CRDPath:    "testdata/crd.yaml",
		ConfigPath: "testdata/crdconfig.yaml",
		SamplesDir: "testdata/crdsamples",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Meta.ResourceKind != "CRD" {
		t.Fatalf("expected ResourceKind CRD, got %q", rep.Meta.ResourceKind)
	}
	if rep.Meta.Resource != "widgets.example.org" {
		t.Fatalf("expected resource name widgets.example.org, got %q", rep.Meta.Resource)
	}
	if rep.Summary.Samples != 2 {
		t.Fatalf("expected 2 samples, got %d", rep.Summary.Samples)
	}
	if rep.Summary.PathsTested != 4 {
		t.Fatalf("expected 4 paths tested, got %d", rep.Summary.PathsTested)
	}
	if rep.Summary.Errors != 0 {
		t.Fatalf("expected 0 errors, got %d", rep.Summary.Errors)
	}
	if rep.Summary.UnacknowledgedLoss != 0 {
		t.Fatalf("expected 0 unacknowledged losses, got %d", rep.Summary.UnacknowledgedLoss)
	}
	if rep.Summary.AcknowledgedLoss != 1 {
		t.Fatalf("expected exactly 1 acknowledged loss, got %d", rep.Summary.AcknowledgedLoss)
	}
}

// TestRunTest_ResourceKindMismatch_IsAnError proves the CLI refuses to
// validate a config against the wrong resource type's flag, rather than
// silently mis-analyzing it.
func TestRunTest_ResourceKindMismatch_IsAnError(t *testing.T) {
	_, err := RunTest(TestOptions{
		CRDPath:    "testdata/crd.yaml",
		ConfigPath: "testdata/config.yaml", // an XRDConversionConfig
		SamplesDir: "testdata/samples",
	})
	if err == nil {
		t.Fatal("expected an error for an XRDConversionConfig passed with --crd, got nil")
	}
}

func TestRunAnalyze_CRD(t *testing.T) {
	out, err := RunAnalyze("", "testdata/crd.yaml", "testdata/crdconfig.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ResourceKind != "CRD" {
		t.Fatalf("expected ResourceKind CRD, got %q", out.ResourceKind)
	}
	if out.Resource != "widgets.example.org" {
		t.Fatalf("expected resource widgets.example.org, got %q", out.Resource)
	}
	if out.Lossless {
		t.Fatalf("expected Lossless=false (the Delete rule is genuinely lossy, just acknowledged), got true")
	}
}

func TestRunValidate_CRD(t *testing.T) {
	res, err := RunValidate("testdata/crdconfig.yaml", "", "testdata/crd.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.StructurallyValid {
		t.Fatalf("expected structurally valid, got errors: %v", res.Errors)
	}
	if !res.SchemaValidated {
		t.Fatalf("expected schema validated, got errors: %v", res.Errors)
	}
}

func TestPeekConfigKind(t *testing.T) {
	kind, err := PeekConfigKind("testdata/config.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != "XRDConversionConfig" {
		t.Fatalf("expected XRDConversionConfig, got %q", kind)
	}

	kind, err = PeekConfigKind("testdata/crdconfig.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != "CRDConversionConfig" {
		t.Fatalf("expected CRDConversionConfig, got %q", kind)
	}
}
