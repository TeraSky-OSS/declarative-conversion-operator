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

// Package actions_test checks the shipped GitHub Actions statically.
//
// actionlint validates workflows, but treats a composite action's action.yml
// as a malformed workflow — so the Actions this repository publishes, which
// are its public CI surface, would otherwise have no check at all outside a
// live workflow run.
package actions_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

const actionsDir = "../../.github/actions"

type actionDef struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Inputs      map[string]struct {
		Description string `yaml:"description"`
		Required    bool   `yaml:"required"`
		Default     string `yaml:"default"`
	} `yaml:"inputs"`
	Outputs map[string]struct {
		Description string `yaml:"description"`
		Value       string `yaml:"value"`
	} `yaml:"outputs"`
	Runs struct {
		Using string `yaml:"using"`
		Steps []struct {
			Name  string `yaml:"name"`
			ID    string `yaml:"id"`
			Uses  string `yaml:"uses"`
			Shell string `yaml:"shell"`
			Run   string `yaml:"run"`
			If    string `yaml:"if"`
		} `yaml:"steps"`
	} `yaml:"runs"`
}

func loadActions(t *testing.T) map[string]actionDef {
	t.Helper()
	entries, err := os.ReadDir(actionsDir)
	if err != nil {
		t.Fatalf("reading %s: %v", actionsDir, err)
	}
	out := map[string]actionDef{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(actionsDir, e.Name(), "action.yml")
		data, err := os.ReadFile(path) // #nosec G304 -- a path inside the repo
		if err != nil {
			t.Fatalf("%s has no action.yml: %v", e.Name(), err)
		}
		var def actionDef
		if err := yaml.Unmarshal(data, &def); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		out[e.Name()] = def
	}
	if len(out) == 0 {
		t.Fatal("no actions found")
	}
	return out
}

// Every action has to be a well-formed composite action with documented
// inputs, because an undocumented input is an input nobody uses correctly.
func TestActions_AreWellFormedAndDocumented(t *testing.T) {
	for name, def := range loadActions(t) {
		t.Run(name, func(t *testing.T) {
			if def.Name == "" || def.Description == "" {
				t.Error("action has no name or description")
			}
			if def.Runs.Using != "composite" {
				t.Errorf("runs.using = %q, want composite", def.Runs.Using)
			}
			if len(def.Runs.Steps) == 0 {
				t.Fatal("action has no steps")
			}
			for input, spec := range def.Inputs {
				if strings.TrimSpace(spec.Description) == "" {
					t.Errorf("input %q has no description", input)
				}
			}
			for output, spec := range def.Outputs {
				if strings.TrimSpace(spec.Description) == "" {
					t.Errorf("output %q has no description", output)
				}
				if spec.Value == "" {
					t.Errorf("output %q has no value, so it is always empty", output)
				}
			}
		})
	}
}

// `shell:` is required on every run step of a composite action — GitHub
// fails the action at runtime without it, which is a failure a consumer
// sees rather than us.
func TestActions_EveryRunStepDeclaresAShell(t *testing.T) {
	for name, def := range loadActions(t) {
		for i, step := range def.Runs.Steps {
			if step.Run != "" && step.Shell == "" {
				t.Errorf("%s step %d (%s) has run: without shell:", name, i+1, step.Name)
			}
		}
	}
}

// bash on all three runners, so the same script is what ships. pwsh or sh
// would mean the Windows path is a different program nobody tests.
func TestActions_UseBashEverywhere(t *testing.T) {
	for name, def := range loadActions(t) {
		for _, step := range def.Runs.Steps {
			if step.Shell != "" && step.Shell != "bash" {
				t.Errorf("%s step %q uses shell %q; bash keeps one script across all three runners", name, step.Name, step.Shell)
			}
		}
	}
}

// A composite action runs in the consumer's repository, so an unpinned
// dependency is their supply chain, not ours.
func TestActions_PinTheirDependencies(t *testing.T) {
	for name, def := range loadActions(t) {
		for _, step := range def.Runs.Steps {
			if step.Uses == "" || strings.HasPrefix(step.Uses, "./") {
				continue
			}
			if !strings.Contains(step.Uses, "@") {
				t.Errorf("%s: %q is not pinned to a version", name, step.Uses)
			}
		}
	}
}

// Every `run:` script sets the failure modes bash does not set by default.
// Without `set -e` a failing command in the middle of a step leaves the step
// green, which for a verification step is the worst possible outcome.
func TestActions_ScriptsFailFast(t *testing.T) {
	for name, def := range loadActions(t) {
		for _, step := range def.Runs.Steps {
			if step.Run == "" {
				continue
			}
			if !strings.Contains(step.Run, "set -euo pipefail") && !strings.Contains(step.Run, "set -eo pipefail") {
				t.Errorf("%s step %q does not set -euo pipefail, so a failing command mid-script would still pass", name, step.Name)
			}
		}
	}
}

// Verification is the reason the setup action exists, so its shape is
// asserted specifically rather than only in a live run.
func TestSetupConvctl_VerifiesByDefaultAndPinsTheIdentity(t *testing.T) {
	def, ok := loadActions(t)["setup-convctl"]
	if !ok {
		t.Fatal("setup-convctl is missing")
	}
	if got := def.Inputs["verify"].Default; got != "true" {
		t.Errorf("verify defaults to %q; an action that verifies by default is the whole point", got)
	}

	var verifyStep string
	for _, s := range def.Runs.Steps {
		if strings.Contains(s.Run, "cosign verify-blob") {
			verifyStep = s.Run
		}
	}
	if verifyStep == "" {
		t.Fatal("no step runs cosign verify-blob")
	}
	// A wildcard identity would accept a signature from any workflow in any
	// repository, which is the thing being defended against.
	if !strings.Contains(verifyStep, "declarative-conversion-operator/\\.github/workflows/release\\.yml") {
		t.Error("the certificate identity is not pinned to this repository's release workflow")
	}
	// Fulcio records the owner in GitHub's canonical casing (TeraSky-OSS).
	// A lowercase pattern fails with "none of the expected identities
	// matched", which reads like a bad signature rather than a typo — and
	// did, in this action and in the release notes, until it was run.
	if !strings.Contains(verifyStep, "(?i:terasky-oss)") {
		t.Error("the owner is matched case-sensitively, so verification fails against the real certificate")
	}
	if !strings.Contains(verifyStep, "--certificate-oidc-issuer https://token.actions.githubusercontent.com") {
		t.Error("the OIDC issuer is not pinned")
	}
	// And the checksum has to actually be checked against the file whose
	// signature was just verified.
	if !strings.Contains(verifyStep, "sha256sum -c") && !strings.Contains(verifyStep, "shasum -a 256 -c") {
		t.Error("the archive's checksum is never verified against checksums.txt")
	}

	// The cached path is the one that runs in practice; a cache hit that
	// skipped verification would let a poisoned cache launder itself.
	for _, s := range def.Runs.Steps {
		if strings.Contains(s.Run, "cosign verify-blob") {
			if strings.Contains(s.If, "cache-hit") {
				t.Error("verification is skipped on a cache hit, so a poisoned cache would be laundered by one")
			}
		}
	}
}

// The three runner OSes are the reason the archive naming and extraction
// logic exist; a missing case would be a consumer's broken pipeline.
func TestSetupConvctl_HandlesEveryRunnerPlatform(t *testing.T) {
	def := loadActions(t)["setup-convctl"]
	var resolve string
	for _, s := range def.Runs.Steps {
		if s.ID == "resolve" {
			resolve = s.Run
		}
	}
	if resolve == "" {
		t.Fatal("no resolve step")
	}
	for _, want := range []string{"Linux", "macOS", "Windows", "X64", "ARM64", "zip", "tar.gz", "convctl.exe"} {
		if !strings.Contains(resolve, want) {
			t.Errorf("the resolve step does not handle %q", want)
		}
	}
	// An unrecognised platform has to fail loudly rather than produce a
	// nonsense archive name.
	if strings.Count(resolve, "unsupported") < 2 {
		t.Error("an unknown OS or architecture is not rejected")
	}
}
