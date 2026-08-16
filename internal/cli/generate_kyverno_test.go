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
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGenerateKyverno_FromV1ToV2(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "examples", "crossplane-xr-multiversion")
	docs, err := RunGenerateKyverno(GenerateKyvernoOptions{
		XRDPath: filepath.Join(root, "03-promote-v2", "xrd.yaml"),
		From:    "v1",
		To:      "v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := encodeKyvernoYAML(docs)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "gitops", "policies", "from-v1-to-v2.yaml")
	assertGoldenYAML(t, wantPath, got)
	if docs[0].Metadata.Name != "label-compositions-xwidgets" {
		t.Fatalf("composition policy name: got %q", docs[0].Metadata.Name)
	}
	if docs[1].Metadata.Name != "set-composition-version-selector-xwidgets" {
		t.Fatalf("migrate policy name: got %q", docs[1].Metadata.Name)
	}
	labeler := docs[0].Spec.Mutations[0].ApplyConfiguration.Expression
	if !strings.Contains(labeler, `kind == "XWidget"`) {
		t.Fatalf("labeler should match XRD kind, got:\n%s", labeler)
	}
	if !strings.Contains(labeler, `startsWith("example.org/")`) {
		t.Fatalf("labeler should match XRD group, got:\n%s", labeler)
	}
	if strings.Contains(labeler, "Object.metadata.labels{") {
		t.Fatalf("hyphenated label keys must use a CEL map literal, not Object.metadata.labels{}, got:\n%s", labeler)
	}
	if len(docs[0].Spec.MatchConditions) != 0 {
		t.Fatalf("labeler must not use matchConditions (Kyverno 1.18 ignores object.spec there)")
	}
	migrate := docs[1].Spec.Mutations[0].JSONPatch.Expression
	if !strings.Contains(migrate, `== "v1"`) {
		t.Fatalf("migrate --from v1 should compare == v1, got:\n%s", migrate)
	}
	if !strings.Contains(migrate, "oldObject") {
		t.Fatalf("migrate must match UPDATE against oldObject so a later compositionRef write does not rematch, got:\n%s", migrate)
	}
	if len(docs[1].Spec.MatchConditions) != 0 {
		t.Fatalf("migrate must not use matchConditions (Kyverno 1.18 ignores object.spec there)")
	}
	if !docs[1].Spec.Evaluation.Admission.Enabled {
		t.Fatal("migrate must enable admission; Kyverno 1.18.1 mutateExisting is a no-op")
	}
	labelerOnly, err := encodeKyvernoYAML(docs[:1])
	if err != nil {
		t.Fatal(err)
	}
	standalone, err := os.ReadFile(filepath.Join(root, "gitops", "policies", "label-compositions-xwidgets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Standalone file may carry an extra comment line after the generate banner.
	if !bytes.Contains(standalone, bytes.SplitN(labelerOnly, []byte("apiVersion:"), 2)[1]) {
		t.Fatalf("label-compositions-xwidgets.yaml should contain the generated labeler body")
	}
}

func TestRunGenerateKyverno_FromV2ToV3(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "examples", "crossplane-xr-multiversion")
	docs, err := RunGenerateKyverno(GenerateKyvernoOptions{
		XRDPath: filepath.Join(root, "05-promote-v3", "xrd.yaml"),
		From:    "v2",
		To:      "v3",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := encodeKyvernoYAML(docs)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "gitops", "policies", "from-v2-to-v3.yaml")
	assertGoldenYAML(t, wantPath, got)
	if got := docs[1].Spec.MatchConstraints.ResourceRules[0].APIVersions; len(got) != 3 {
		t.Fatalf("v3 XRD served versions: got %v", got)
	}
}

func TestRunGenerateKyverno_WithoutFrom(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "examples", "crossplane-xr-multiversion")
	docs, err := RunGenerateKyverno(GenerateKyvernoOptions{
		XRDPath: filepath.Join(root, "03-promote-v2", "xrd.yaml"),
		To:      "v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	expr := docs[1].Spec.Mutations[0].JSONPatch.Expression
	if !strings.Contains(expr, `!= "v2"`) {
		t.Fatalf("without --from, migrate should select anything not already v2, got:\n%s", expr)
	}
	if strings.Contains(expr, `== "v1"`) {
		t.Fatalf("without --from, should not pin to v1, got:\n%s", expr)
	}
}

func TestRunGenerateKyverno_UnknownTo(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "examples", "crossplane-xr-multiversion")
	_, err := RunGenerateKyverno(GenerateKyvernoOptions{
		XRDPath: filepath.Join(root, "03-promote-v2", "xrd.yaml"),
		To:      "v9",
	})
	if err == nil {
		t.Fatal("expected error for --to not on the XRD")
	}
	if !strings.Contains(err.Error(), "v9") {
		t.Fatalf("error should name the bad version, got %v", err)
	}
}

func TestRunGenerateKyverno_UnknownFrom(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "examples", "crossplane-xr-multiversion")
	_, err := RunGenerateKyverno(GenerateKyvernoOptions{
		XRDPath: filepath.Join(root, "03-promote-v2", "xrd.yaml"),
		From:    "v9",
		To:      "v2",
	})
	if err == nil {
		t.Fatal("expected error for --from not on the XRD")
	}
}

func TestDNS1123Label(t *testing.T) {
	t.Parallel()
	if got := dns1123Label("XWidgets"); got != "xwidgets" {
		t.Fatalf("got %q", got)
	}
	if got := dns1123Label("xwidgets.example.org"); got != "xwidgets-example-org" {
		t.Fatalf("got %q", got)
	}
}

func assertGoldenYAML(t *testing.T, path string, got []byte) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDENS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("generated YAML drifted from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}
