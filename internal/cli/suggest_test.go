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

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func spokeSuggestions(t *testing.T, out *SuggestOutput, version string) []teraskyv1alpha1.ConversionRule {
	t.Helper()
	for _, s := range out.Spokes {
		if s.Version == version {
			return s.Rules
		}
	}
	t.Fatalf("expected suggestions for spoke %s, got %+v", version, out.Spokes)
	return nil
}

func hasRename(rules []teraskyv1alpha1.ConversionRule, hubPath, spokePath string) bool {
	for _, r := range rules {
		if r.Strategy == teraskyv1alpha1.StrategyFieldRename && r.FieldRename != nil &&
			r.FieldRename.HubPath == hubPath && r.FieldRename.SpokePath == spokePath {
			return true
		}
	}
	return false
}

func hasCoerce(rules []teraskyv1alpha1.ConversionRule, path string) bool {
	for _, r := range rules {
		if r.Strategy == teraskyv1alpha1.StrategyTypeCoerce && r.TypeCoerce != nil && r.TypeCoerce.Path == path {
			return true
		}
	}
	return false
}

// TestRunSuggest_ProposesRenamesAndCoerces runs against the full fixture's
// schemas with every rule stripped, which is the situation suggest exists
// for: the storageGB/storageSize rename the real config declares must come
// back as a proposal, as must the priority type change.
func TestRunSuggest_ProposesRenamesAndCoerces(t *testing.T) {
	out, err := RunSuggest("testdata/full/xrd.yaml", "", "testdata/full/config-norules.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	v2 := spokeSuggestions(t, out, "v2")
	if !hasRename(v2, "spec.storageGB", "spec.storageSize") {
		t.Fatalf("expected a spec.storageGB -> spec.storageSize rename proposal, got %+v", v2)
	}
	if !hasRename(v2, "spec.memoryMB", "spec.memoryGB") {
		t.Fatalf("expected a spec.memoryMB -> spec.memoryGB rename proposal, got %+v", v2)
	}
	if !hasCoerce(v2, "spec.priority") {
		t.Fatalf("expected a spec.priority TypeCoerce proposal (integer on the hub, string on v2), got %+v", v2)
	}
}

// TestRunSuggest_NeverProposesNoOpOrCrossTypeRenames guards the two ways a
// name-similarity heuristic most easily embarrasses itself: proposing to
// rename a field to itself, and proposing a rename across a type change
// that no rename can actually perform.
func TestRunSuggest_NeverProposesNoOpOrCrossTypeRenames(t *testing.T) {
	out, err := RunSuggest("testdata/full/xrd.yaml", "", "testdata/full/config-norules.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, s := range out.Spokes {
		for _, r := range s.Rules {
			if r.FieldRename == nil {
				continue
			}
			if r.FieldRename.HubPath == r.FieldRename.SpokePath {
				t.Fatalf("spoke %s: proposed renaming %q to itself", s.Version, r.FieldRename.HubPath)
			}
		}
		// dnsServers is an array on the hub and dnsServersCSV a string on
		// v2: similar names, incompatible kinds, so no rename is valid.
		if hasRename(s.Rules, "spec.dnsServers", "spec.dnsServersCSV") {
			t.Fatalf("spoke %s: proposed a rename across an array/string type change", s.Version)
		}
	}
}

// TestRunSuggest_FullyCoveredConfigSuggestsNothing is the other end of the
// range: the complete fixture leaves no field uncovered, so there is
// nothing left to guess at.
func TestRunSuggest_FullyCoveredConfigSuggestsNothing(t *testing.T) {
	out, err := RunSuggest("testdata/full/xrd.yaml", "", "testdata/full/config.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Spokes) != 0 {
		t.Fatalf("expected no suggestions for a fully covered config, got %+v", out.Spokes)
	}
}

func TestRunSuggest_WrongSchemaFlag(t *testing.T) {
	if _, err := RunSuggest("", "testdata/crd.yaml", "testdata/config.yaml"); err == nil {
		t.Fatalf("expected an error when an XRDConversionConfig is given --crd")
	}
	if _, err := RunSuggest("testdata/xrd.yaml", "", "testdata/crdconfig.yaml"); err == nil {
		t.Fatalf("expected an error when a CRDConversionConfig is given --xrd")
	}
}

func TestNameSimilarity(t *testing.T) {
	cases := []struct {
		a, b    string
		atLeast float64
	}{
		{"storageGB", "storage_gb", 1},
		{"memoryMB", "memoryGB", 0.8},
		{"storageGB", "storageSize", suggestSimilarityThreshold},
	}
	for _, c := range cases {
		if got := nameSimilarity(c.a, c.b); got < c.atLeast {
			t.Errorf("nameSimilarity(%q, %q) = %v, want >= %v", c.a, c.b, got, c.atLeast)
		}
	}
	if got := nameSimilarity("phase", "endpoints"); got >= suggestSimilarityThreshold {
		t.Errorf("nameSimilarity(phase, endpoints) = %v, want below the suggestion threshold", got)
	}
	if got := nameSimilarity("", "anything"); got != 0 {
		t.Errorf("nameSimilarity with an empty name = %v, want 0", got)
	}
}
