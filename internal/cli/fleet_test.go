/*
Copyright 2026 The declarative-conversion-operator Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

    10|Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cli

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveLiveTargets_SingleDefault(t *testing.T) {
	got, err := resolveLiveTargets(TestOptions{Live: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Label != "current-context" {
		t.Fatalf("single --live should stay one current-context target, got %+v", got)
	}
}

func TestResolveLiveTargets_OneContextMatchesSingleCluster(t *testing.T) {
	got, err := resolveLiveTargets(TestOptions{Live: true, Contexts: []string{"prod"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Context != "prod" || got[0].Label != "prod" {
		t.Fatalf("one --contexts value must be a single-cluster target, got %+v", got)
	}
}

func TestResolveLiveTargets_Contexts(t *testing.T) {
	got, err := resolveLiveTargets(TestOptions{Live: true, Contexts: []string{"east", "west"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Context != "east" || got[1].Context != "west" {
		t.Fatalf("expected east then west, got %+v", got)
	}
}

func TestResolveLiveTargets_KubeconfigDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "east.kubeconfig"), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "west.kubeconfig"), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveLiveTargets(TestOptions{Live: true, KubeconfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 kubeconfigs (README skipped), got %+v", got)
	}
}

func TestResolveLiveTargets_RejectsMixedFlags(t *testing.T) {
	if _, err := resolveLiveTargets(TestOptions{Kubeconfig: "a", KubeconfigDir: "b"}); err == nil {
		t.Fatal("expected --kubeconfig and --kubeconfig-dir to conflict")
	}
	if _, err := resolveLiveTargets(TestOptions{KubeContext: "a", Contexts: []string{"b"}}); err == nil {
		t.Fatal("expected --context and --contexts to conflict")
	}
}

func TestFleetReport_WriteJUnit_PerClusterSuites(t *testing.T) {
	rep, err := RunTest(TestOptions{
		XRDPath:    "testdata/xrd.yaml",
		ConfigPath: "testdata/config.yaml",
		SamplesDir: "testdata/samples",
	})
	if err != nil {
		t.Fatal(err)
	}
	fleet := FleetReport{Clusters: []ClusterResult{
		{Label: "east", Report: rep},
		{Label: "west", Error: "dial timeout"},
	}}
	var buf bytes.Buffer
	if err := fleet.WriteJUnit(&buf); err != nil {
		t.Fatal(err)
	}
	var parsed junitTestSuites
	if err := xml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("not JUnit XML: %v\n%s", err, buf.String())
	}
	if len(parsed.Suites) != 2 {
		t.Fatalf("expected 2 testsuites, got %d", len(parsed.Suites))
	}
	if !strings.HasPrefix(parsed.Suites[0].Name, "east/") {
		t.Fatalf("first suite should be prefixed with cluster label, got %q", parsed.Suites[0].Name)
	}
	if parsed.Suites[1].Errors != 1 {
		t.Fatalf("unreachable cluster should be a suite error, got %+v", parsed.Suites[1])
	}
	if parsed.Errors < 1 {
		t.Fatal("fleet totals must count the cluster error")
	}
}

func TestFleetReport_DecideExitCode(t *testing.T) {
	ok := &Report{}
	fleet := FleetReport{Clusters: []ClusterResult{
		{Label: "a", Report: ok},
		{Label: "b", Error: "no such host"},
	}}
	if fleet.decideExitCode(failOnLoss, false) != ExitTestFailure {
		t.Fatal("a cluster connection error must fail the fleet run")
	}
}
