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

// lintTree writes a small platform repository: two nested directories, a
// schema and a config in each.
func lintTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "apis", "buckets", "xrd.yaml"), mustRead(t, "../../examples/field-rename/xrd.yaml"))
	mustWrite(t, filepath.Join(dir, "apis", "buckets", "conversion.yaml"), mustRead(t, "../../examples/field-rename/xrdconversionconfig.yaml"))
	mustWrite(t, filepath.Join(dir, "apis", "widgets", "crd.yaml"), mustRead(t, "../../examples/native-crd/crd.yaml"))
	mustWrite(t, filepath.Join(dir, "apis", "widgets", "conversion.yaml"), mustRead(t, "../../examples/native-crd/crdconversionconfig.yaml"))
	return dir
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The point of the command is one run over a tree, so discovery has to reach
// nested directories and has to recognise both config kinds.
func TestRunLint_DiscoversAndPairsAcrossNestedDirectories(t *testing.T) {
	rep, err := RunLint(LintOptions{Paths: []string{lintTree(t)}})
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	if rep.Configs != 2 || rep.Schemas != 2 {
		t.Fatalf("found %d configs and %d schemas, want 2 and 2", rep.Configs, rep.Schemas)
	}
	kinds := map[string]bool{}
	for _, res := range rep.Results {
		if res.Status != "ok" {
			t.Errorf("%s: status %s, findings %+v", res.Config, res.Status, res.Findings)
		}
		if res.Schema == "" {
			t.Errorf("%s was not paired with a schema", res.Config)
		}
		kinds[res.Kind] = true
	}
	if !kinds["XRD"] || !kinds["CRD"] {
		t.Errorf("both config kinds should be discovered, got %v", kinds)
	}
	if rep.Errors != 0 || rep.Unpaired != 0 {
		t.Errorf("a clean tree reported %d error(s), %d unpaired", rep.Errors, rep.Unpaired)
	}
}

// A config paired with nothing looks exactly like a config that passed. It
// has to be an error naming what was looked for, never a silent skip — this
// is the failure mode that makes a tool like this untrustworthy.
func TestRunLint_UnpairedConfigIsAnErrorNamingTheTarget(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "conversion.yaml"), mustRead(t, "../../examples/field-rename/xrdconversionconfig.yaml"))

	rep, err := RunLint(LintOptions{Paths: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Unpaired != 1 {
		t.Fatalf("unpaired = %d, want 1", rep.Unpaired)
	}
	res := rep.Results[0]
	if res.Status != "unpaired" {
		t.Errorf("status = %q", res.Status)
	}
	if len(res.Findings) == 0 || !strings.Contains(res.Findings[0].Message, "xbuckets.example.org") {
		t.Errorf("finding should name the target it looked for: %+v", res.Findings)
	}
	if decideLintExitCode(rep, failOnLoss) == ExitOK {
		t.Error("an unpaired config exited 0")
	}
}

// --schema-dir is for repositories that keep schemas apart from configs.
func TestRunLint_SchemaDirPairsAcrossTrees(t *testing.T) {
	configs := t.TempDir()
	schemas := t.TempDir()
	mustWrite(t, filepath.Join(configs, "conversion.yaml"), mustRead(t, "../../examples/field-rename/xrdconversionconfig.yaml"))
	mustWrite(t, filepath.Join(schemas, "xrd.yaml"), mustRead(t, "../../examples/field-rename/xrd.yaml"))

	rep, err := RunLint(LintOptions{Paths: []string{configs}, SchemaDirs: []string{schemas}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Unpaired != 0 || rep.Results[0].Schema == "" {
		t.Errorf("--schema-dir did not pair: %+v", rep.Results)
	}
}

// The operator enforces one config per target. Finding that out from an
// admission rejection after merge is what this command exists to prevent.
func TestRunLint_DuplicateTargetsAreReported(t *testing.T) {
	dir := lintTree(t)
	mustWrite(t, filepath.Join(dir, "apis", "buckets", "conversion-copy.yaml"), mustRead(t, "../../examples/field-rename/xrdconversionconfig.yaml"))

	rep, err := RunLint(LintOptions{Paths: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Duplicate != 1 {
		t.Fatalf("duplicate = %d, want 1 (the second config, not both)", rep.Duplicate)
	}
	var dup LintPairResult
	for _, res := range rep.Results {
		if res.Status == "duplicate" {
			dup = res
		}
	}
	if len(dup.Findings) == 0 || !strings.Contains(dup.Findings[0].Message, "one config per target") {
		t.Errorf("finding should explain what will happen: %+v", dup.Findings)
	}
}

// A platform tree is full of manifests that are none of this tool's
// business. Those must be ignored, not rejected.
func TestRunLint_IgnoresUnrelatedManifests(t *testing.T) {
	dir := lintTree(t)
	mustWrite(t, filepath.Join(dir, "kustomization.yaml"), []byte("resources:\n  - apis/\n"))
	mustWrite(t, filepath.Join(dir, "deployment.yaml"), []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: x\n"))
	mustWrite(t, filepath.Join(dir, "notes.txt"), []byte("not yaml at all"))

	rep, err := RunLint(LintOptions{Paths: []string{dir}})
	if err != nil {
		t.Fatalf("an unrelated manifest failed the whole run: %v", err)
	}
	if rep.Configs != 2 {
		t.Errorf("configs = %d, want the 2 real ones", rep.Configs)
	}
}

// Multi-document files are how most GitOps repositories are laid out.
func TestRunLint_HandlesMultiDocumentFiles(t *testing.T) {
	dir := t.TempDir()
	joined := append(append([]byte{}, mustRead(t, "../../examples/field-rename/xrd.yaml")...), []byte("\n---\n")...)
	joined = append(joined, mustRead(t, "../../examples/field-rename/xrdconversionconfig.yaml")...)
	mustWrite(t, filepath.Join(dir, "all.yaml"), joined)

	rep, err := RunLint(LintOptions{Paths: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Configs != 1 || rep.Schemas != 1 {
		t.Fatalf("found %d configs, %d schemas in one file, want 1 and 1", rep.Configs, rep.Schemas)
	}
	if rep.Unpaired != 0 {
		t.Errorf("a config and its schema in one file were not paired: %+v", rep.Results)
	}
}

func TestRunLint_ExcludeSkipsPaths(t *testing.T) {
	dir := lintTree(t)
	rep, err := RunLint(LintOptions{Paths: []string{dir}, Exclude: []string{"widgets"}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Configs != 1 {
		t.Errorf("configs = %d, want the excluded directory skipped", rep.Configs)
	}
}

// Parallel, but the report has to read the same every run or a CI diff of
// two runs is noise.
func TestRunLint_ReportOrderIsDeterministic(t *testing.T) {
	dir := lintTree(t)
	first, err := RunLint(LintOptions{Paths: []string{dir}, Concurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		again, err := RunLint(LintOptions{Paths: []string{dir}, Concurrency: 4})
		if err != nil {
			t.Fatal(err)
		}
		for j := range first.Results {
			if again.Results[j].Config != first.Results[j].Config {
				t.Fatalf("run %d differs at %d: %s vs %s", i, j, again.Results[j].Config, first.Results[j].Config)
			}
		}
	}
}

// A broken config has to be reported against the line that broke it, which
// is the whole reason the CI formats exist.
func TestRunLint_FindingsCarryConfigLocations(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "xrd.yaml"), mustRead(t, "../../examples/crossplane-xr-multiversion/02-add-v2/xrd.yaml"))
	mustWrite(t, filepath.Join(dir, "conversion.yaml"), mustRead(t, "../../examples/crossplane-xr-multiversion/mistakes/02-missing-rename.yaml"))

	rep, err := RunLint(LintOptions{Paths: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	findings := rep.Findings()
	if len(findings) == 0 {
		t.Fatal("a config with an uncovered field produced no findings")
	}
	for _, f := range findings {
		if !strings.HasSuffix(f.Location.File, "conversion.yaml") {
			t.Errorf("finding points at %q, not the config", f.Location.File)
		}
	}
	if decideLintExitCode(rep, failOnLoss) == ExitOK {
		t.Error("a config with errors exited 0")
	}
	if decideLintExitCode(rep, failOnNone) != ExitOK {
		t.Error("--fail-on none should never fail")
	}
}

func TestLintReport_WriteTableSaysWhenNothingWasFound(t *testing.T) {
	var buf bytes.Buffer
	(&LintReport{}).WriteTable(&buf)
	if !strings.Contains(buf.String(), "No conversion configs found") {
		t.Errorf("an empty tree rendered %q", buf.String())
	}
}
