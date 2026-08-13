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
	"testing"
)

func TestRunDiff_IdenticalConfigsAreClean(t *testing.T) {
	out, err := RunDiff(DiffOptions{
		ConfigPaths: []string{"testdata/config.yaml", "testdata/config.yaml"},
		XRDPath:     "testdata/xrd.yaml",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.HasDeltas {
		t.Fatalf("expected no deltas between a config and itself, got %+v", out)
	}
	if len(out.Spokes) != 0 || len(out.SpokesAdded) != 0 || len(out.SpokesRemoved) != 0 {
		t.Fatalf("expected an entirely empty diff, got %+v", out)
	}
}

// TestRunDiff_DroppedFieldRename asserts the whole chain a real review
// cares about: the rename's claim disappears, both of the paths it used to
// cover become uncovered, and new errors are reported for them.
func TestRunDiff_DroppedFieldRename(t *testing.T) {
	out, err := RunDiff(DiffOptions{
		ConfigPaths: []string{"testdata/config.yaml", "testdata/config-norename.yaml"},
		XRDPath:     "testdata/xrd.yaml",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.HasDeltas {
		t.Fatalf("expected deltas after dropping a FieldRename rule, got %+v", out)
	}
	if out.ResourceKind != "XRD" || out.Resource != "xfoos.example.org" {
		t.Fatalf("unexpected diff target: %s %s", out.ResourceKind, out.Resource)
	}
	if len(out.Spokes) != 1 || out.Spokes[0].Version != "v1" {
		t.Fatalf("expected exactly one changed spoke (v1), got %+v", out.Spokes)
	}
	sd := out.Spokes[0]

	if len(sd.RuleClaimsRemoved) != 1 || sd.RuleClaimsRemoved[0].Strategy != "FieldRename" {
		t.Fatalf("expected the FieldRename claim to be reported as removed, got %+v", sd.RuleClaimsRemoved)
	}
	claim := sd.RuleClaimsRemoved[0]
	if len(claim.HubPaths) != 1 || claim.HubPaths[0] != "spec.storageGB" {
		t.Fatalf("expected the removed claim to name spec.storageGB, got %+v", claim.HubPaths)
	}
	if len(claim.SpokePaths) != 1 || claim.SpokePaths[0] != "spec.storageSize" {
		t.Fatalf("expected the removed claim to name spec.storageSize, got %+v", claim.SpokePaths)
	}
	if len(sd.RuleClaimsAdded) != 0 {
		t.Fatalf("expected no added claims, got %+v", sd.RuleClaimsAdded)
	}

	if len(sd.UncoveredHubAdded) != 1 || sd.UncoveredHubAdded[0] != "spec.storageGB" {
		t.Fatalf("expected spec.storageGB to become uncovered on the hub side, got %+v", sd.UncoveredHubAdded)
	}
	if len(sd.UncoveredSpokeAdded) != 1 || sd.UncoveredSpokeAdded[0] != "spec.storageSize" {
		t.Fatalf("expected spec.storageSize to become uncovered on the spoke side, got %+v", sd.UncoveredSpokeAdded)
	}
	if len(sd.ErrorsAdded) == 0 {
		t.Fatalf("expected new error diagnostics for the now-uncovered fields")
	}
	if len(sd.ErrorsRemoved) != 0 {
		t.Fatalf("expected no errors to disappear, got %+v", sd.ErrorsRemoved)
	}
}

// TestRunDiff_AgainstEmptyRules covers what --live falls back to when the
// cluster has no config applied yet: every rule the local config declares
// shows up as an addition against a same-spokes, zero-rule baseline.
func TestRunDiff_AgainstEmptyRules(t *testing.T) {
	xrd, err := LoadXRD("testdata/xrd.yaml")
	if err != nil {
		t.Fatalf("load xrd: %v", err)
	}
	local, err := LoadConfig("testdata/config.yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	out, err := diffXRDConfigs(xrd, emptyRulesXRDConfig(local), local, "cluster:(none)", "testdata/config.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.HasDeltas || len(out.Spokes) != 1 {
		t.Fatalf("expected deltas on the single spoke, got %+v", out)
	}
	if len(out.Spokes[0].RuleClaimsAdded) != 2 {
		t.Fatalf("expected both declared rules to be reported as added, got %+v", out.Spokes[0].RuleClaimsAdded)
	}
	if len(out.Spokes[0].ErrorsRemoved) == 0 {
		t.Fatalf("expected the baseline's uncovered-field errors to be resolved by the local config")
	}
}

func TestRunDiff_UsageErrors(t *testing.T) {
	cases := map[string]DiffOptions{
		"one config without --live": {ConfigPaths: []string{"testdata/config.yaml"}, XRDPath: "testdata/xrd.yaml"},
		"three configs":             {ConfigPaths: []string{"testdata/config.yaml", "testdata/config.yaml", "testdata/config.yaml"}, XRDPath: "testdata/xrd.yaml"},
		"two configs with --live":   {ConfigPaths: []string{"testdata/config.yaml", "testdata/config.yaml"}, Live: true},
		"missing schema":            {ConfigPaths: []string{"testdata/config.yaml", "testdata/config.yaml"}},
		"mismatched kinds":          {ConfigPaths: []string{"testdata/config.yaml", "testdata/crdconfig.yaml"}, XRDPath: "testdata/xrd.yaml"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := RunDiff(opts); err == nil {
				t.Fatalf("expected an error for %s", name)
			}
		})
	}
}

func TestRunDiff_CRDConfigs(t *testing.T) {
	out, err := RunDiff(DiffOptions{
		ConfigPaths: []string{"testdata/crdconfig.yaml", "testdata/crdconfig.yaml"},
		CRDPath:     "testdata/crd.yaml",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ResourceKind != "CRD" {
		t.Fatalf("expected a CRD diff, got %q", out.ResourceKind)
	}
	if out.HasDeltas {
		t.Fatalf("expected no deltas between a config and itself, got %+v", out)
	}
}
