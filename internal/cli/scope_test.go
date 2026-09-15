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
	"strings"
	"testing"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

func TestRunAnalyze_ReportsTheDetectedScope(t *testing.T) {
	out, err := RunAnalyze("testdata/xrd.yaml", "", "testdata/config.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Scope == nil {
		t.Fatal("expected an XRD analysis to report the detected Crossplane scope")
	}
	if out.Scope.Scope != string(xrdadapter.ScopeNamespaced) {
		t.Errorf("scope = %q, want Namespaced", out.Scope.Scope)
	}
	// The fixture declares scope explicitly, so there is nothing to infer.
	if out.Scope.Confidence != string(xrdadapter.ConfidenceHigh) {
		t.Errorf("confidence = %q, want High", out.Scope.Confidence)
	}

	var buf bytes.Buffer
	out.WriteTable(&buf)
	if !strings.Contains(buf.String(), "Crossplane scope: Namespaced") {
		t.Errorf("table output does not name the scope:\n%s", buf.String())
	}
}

func TestRunAnalyze_CRDTargetHasNoScope(t *testing.T) {
	// Scope is a Crossplane concept; a native CRD analysis must not
	// invent one, and its table must not print a scope line.
	out, err := RunAnalyze("", "testdata/crd.yaml", "testdata/crdconfig.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Scope != nil {
		t.Fatalf("expected no scope for a CRD target, got %+v", out.Scope)
	}

	var buf bytes.Buffer
	out.WriteTable(&buf)
	if strings.Contains(buf.String(), "Crossplane scope") {
		t.Errorf("CRD table must not mention Crossplane scope:\n%s", buf.String())
	}
}

func TestRunTest_ReportsTheDetectedScope(t *testing.T) {
	rep, err := RunTest(TestOptions{
		XRDPath:    "testdata/xrd.yaml",
		ConfigPath: "testdata/config.yaml",
		SamplesDir: "testdata/samples",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Meta.Scope == nil || rep.Meta.Scope.Scope != string(xrdadapter.ScopeNamespaced) {
		t.Fatalf("expected the test report to carry the detected scope, got %+v", rep.Meta.Scope)
	}

	var buf bytes.Buffer
	rep.WriteTable(&buf)
	if !strings.Contains(buf.String(), "Crossplane scope: Namespaced") {
		t.Errorf("table output does not name the scope:\n%s", buf.String())
	}
}

func TestScopeView_WriteSaysSoWhenIndeterminate(t *testing.T) {
	var buf bytes.Buffer
	scopeView(xrdadapter.ScopeResolution{
		Scope:      xrdadapter.ScopeIndeterminate,
		Confidence: xrdadapter.ConfidenceNone,
		Reason:     "spec.scope is absent and no claim signal is present",
	}).write(&buf)

	got := buf.String()
	if !strings.Contains(got, "INDETERMINATE") || !strings.Contains(got, "no claim signal") {
		t.Errorf("indeterminate scope must be loud and carry its reason, got %q", got)
	}
}
