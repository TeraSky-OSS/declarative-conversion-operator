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

package webhook

import (
	"testing"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func TestValidateStructure_RejectsSpokeEqualToHub(t *testing.T) {
	cfg := &teraskyv1alpha1.XRDConversionConfig{Spec: teraskyv1alpha1.XRDConversionConfigSpec{
		HubVersion: "v2",
		Spokes:     []teraskyv1alpha1.SpokeVersionRules{{Version: "v2"}},
	}}
	if err := ValidateStructure(cfg); err == nil {
		t.Fatalf("expected an error when a spoke version equals the hub version")
	}
}

func TestValidateStructure_RejectsDuplicateSpokes(t *testing.T) {
	cfg := &teraskyv1alpha1.XRDConversionConfig{Spec: teraskyv1alpha1.XRDConversionConfigSpec{
		HubVersion: "v2",
		Spokes:     []teraskyv1alpha1.SpokeVersionRules{{Version: "v1"}, {Version: "v1"}},
	}}
	if err := ValidateStructure(cfg); err == nil {
		t.Fatalf("expected an error for a duplicate spoke version")
	}
}

func TestValidateStructure_RejectsAmbiguousRuleParams(t *testing.T) {
	cfg := &teraskyv1alpha1.XRDConversionConfig{Spec: teraskyv1alpha1.XRDConversionConfigSpec{
		HubVersion: "v2",
		Spokes: []teraskyv1alpha1.SpokeVersionRules{{
			Version: "v1",
			Rules: []teraskyv1alpha1.ConversionRule{{
				Strategy:    teraskyv1alpha1.StrategyFieldRename,
				FieldRename: &teraskyv1alpha1.FieldRenameParams{HubPath: "a", SpokePath: "b"},
				Delete:      &teraskyv1alpha1.DeleteParams{Path: "c", ExistsOn: teraskyv1alpha1.SideHub},
			}},
		}},
	}}
	if err := ValidateStructure(cfg); err == nil {
		t.Fatalf("expected an error when two params fields are set for one rule")
	}
}

func TestValidateStructure_RejectsDeepForEachNesting(t *testing.T) {
	inner := teraskyv1alpha1.ConversionRule{
		Strategy: teraskyv1alpha1.StrategyForEach,
		ForEach: &teraskyv1alpha1.ForEachParams{
			HubItemsPath: "x", SpokeItemsPath: "y",
			Rules: []teraskyv1alpha1.ConversionRule{{Strategy: teraskyv1alpha1.StrategyFieldRename, FieldRename: &teraskyv1alpha1.FieldRenameParams{HubPath: "a", SpokePath: "b"}}},
		},
	}
	outer := teraskyv1alpha1.ConversionRule{
		Strategy: teraskyv1alpha1.StrategyForEach,
		ForEach:  &teraskyv1alpha1.ForEachParams{HubItemsPath: "x", SpokeItemsPath: "y", Rules: []teraskyv1alpha1.ConversionRule{inner}},
	}
	cfg := &teraskyv1alpha1.XRDConversionConfig{Spec: teraskyv1alpha1.XRDConversionConfigSpec{
		HubVersion: "v2",
		Spokes:     []teraskyv1alpha1.SpokeVersionRules{{Version: "v1", Rules: []teraskyv1alpha1.ConversionRule{outer}}},
	}}
	if err := ValidateStructure(cfg); err == nil {
		t.Fatalf("expected an error for ForEach nested more than one level deep")
	}
}

func TestValidateStructure_AcceptsWellFormedConfig(t *testing.T) {
	cfg := &teraskyv1alpha1.XRDConversionConfig{Spec: teraskyv1alpha1.XRDConversionConfigSpec{
		HubVersion: "v2",
		Spokes: []teraskyv1alpha1.SpokeVersionRules{{
			Version: "v1",
			Rules: []teraskyv1alpha1.ConversionRule{{
				Strategy:    teraskyv1alpha1.StrategyFieldRename,
				FieldRename: &teraskyv1alpha1.FieldRenameParams{HubPath: "a", SpokePath: "b"},
			}},
		}},
	}}
	if err := ValidateStructure(cfg); err != nil {
		t.Fatalf("unexpected error for a well-formed config: %v", err)
	}
}
