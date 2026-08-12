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

// TestValidateCRDStructure_* only spot-checks that CRDConversionConfig
// routes into the same validateSpokesStructure logic XRDConversionConfig
// uses — see xrdconversionconfig_webhook_test.go for exhaustive coverage
// of that shared logic itself.

func TestValidateCRDStructure_RejectsSpokeEqualToHub(t *testing.T) {
	cfg := &teraskyv1alpha1.CRDConversionConfig{Spec: teraskyv1alpha1.CRDConversionConfigSpec{
		HubVersion: "v2",
		Spokes:     []teraskyv1alpha1.SpokeVersionRules{{Version: "v2"}},
	}}
	if err := ValidateCRDStructure(cfg); err == nil {
		t.Fatalf("expected an error when a spoke version equals the hub version")
	}
}

func TestValidateCRDStructure_RejectsWarnPolicyWithoutReason(t *testing.T) {
	cfg := &teraskyv1alpha1.CRDConversionConfig{Spec: teraskyv1alpha1.CRDConversionConfigSpec{
		HubVersion:          "v2",
		UnmappedFieldPolicy: teraskyv1alpha1.UnmappedFieldPolicyWarn,
		UnmappedFieldReason: "",
		Spokes:              []teraskyv1alpha1.SpokeVersionRules{{Version: "v1"}},
	}}
	if err := ValidateCRDStructure(cfg); err == nil {
		t.Fatalf("expected an error when unmappedFieldPolicy is Warn and unmappedFieldReason is empty")
	}
}

func TestValidateCRDStructure_AcceptsWarnPolicyWithReason(t *testing.T) {
	cfg := &teraskyv1alpha1.CRDConversionConfig{Spec: teraskyv1alpha1.CRDConversionConfigSpec{
		HubVersion:          "v2",
		UnmappedFieldPolicy: teraskyv1alpha1.UnmappedFieldPolicyWarn,
		UnmappedFieldReason: "legacy field retained for backwards compatibility",
		Spokes:              []teraskyv1alpha1.SpokeVersionRules{{Version: "v1"}},
	}}
	if err := ValidateCRDStructure(cfg); err != nil {
		t.Fatalf("unexpected error when unmappedFieldPolicy is Warn and unmappedFieldReason is set: %v", err)
	}
}

func TestValidateCRDStructure_AcceptsDefaultErrorPolicyWithoutReason(t *testing.T) {
	cfg := &teraskyv1alpha1.CRDConversionConfig{Spec: teraskyv1alpha1.CRDConversionConfigSpec{
		HubVersion:          "v2",
		UnmappedFieldPolicy: teraskyv1alpha1.UnmappedFieldPolicyError,
		Spokes:              []teraskyv1alpha1.SpokeVersionRules{{Version: "v1"}},
	}}
	if err := ValidateCRDStructure(cfg); err != nil {
		t.Fatalf("unexpected error: unmappedFieldReason is only required when unmappedFieldPolicy is Warn: %v", err)
	}
}

func TestValidateCRDStructure_AcceptsWellFormedConfig(t *testing.T) {
	cfg := &teraskyv1alpha1.CRDConversionConfig{Spec: teraskyv1alpha1.CRDConversionConfigSpec{
		HubVersion: "v2",
		Spokes: []teraskyv1alpha1.SpokeVersionRules{{
			Version: "v1",
			Rules: []teraskyv1alpha1.ConversionRule{{
				Strategy:    teraskyv1alpha1.StrategyFieldRename,
				FieldRename: &teraskyv1alpha1.FieldRenameParams{HubPath: "a", SpokePath: "b"},
			}},
		}},
	}}
	if err := ValidateCRDStructure(cfg); err != nil {
		t.Fatalf("unexpected error for a well-formed config: %v", err)
	}
}
