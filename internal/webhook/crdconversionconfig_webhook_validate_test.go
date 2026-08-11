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
	"context"
	"strings"
	"testing"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func TestCRDConversionConfigValidator_Disabled_Rejected(t *testing.T) {
	c := newFakeClient().Build()
	v := &CRDConversionConfigValidator{Client: c, Enabled: false}
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	if _, err := v.ValidateCreate(context.Background(), cfg); err == nil {
		t.Fatalf("expected an error when native CRD support is disabled")
	}
}

func TestCRDConversionConfigValidator_DuplicateTarget_Rejected(t *testing.T) {
	existing := renameRuleCRDConfig("existing", "foos.example.org")
	c := newFakeClient(existing).Build()
	v := &CRDConversionConfigValidator{Client: c, Enabled: true}
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	if _, err := v.ValidateCreate(context.Background(), cfg); err == nil {
		t.Fatalf("expected an error: only one config per CRD is supported")
	}
}

func TestCRDConversionConfigValidator_TargetMissing_WarnsAndSkipsLiveValidation(t *testing.T) {
	c := newFakeClient().Build()
	v := &CRDConversionConfigValidator{Client: c, Enabled: true}
	cfg := renameRuleCRDConfig("cfg", "missing.example.org")
	warnings, err := v.ValidateCreate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error when the target CRD doesn't exist yet: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected a warning about the missing target CRD")
	}
}

func TestCRDConversionConfigValidator_WebhookServerRefMissing_Warns(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	c := newFakeClient(crd).Build()
	v := &CRDConversionConfigValidator{Client: c, Enabled: true}
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "does-not-exist"}
	warnings, err := v.ValidateCreate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "does-not-exist") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning naming the missing webhookServerRef, got %v", warnings)
	}
}

func TestCRDConversionConfigValidator_LiveSchemaValidationFailure_Rejected(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	c := newFakeClient(crd).Build()
	v := &CRDConversionConfigValidator{Client: c, Enabled: true}
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	cfg.Spec.Spokes[0].Rules[0].FieldRename.HubPath = "spec.doesNotExist"
	if _, err := v.ValidateCreate(context.Background(), cfg); err == nil {
		t.Fatalf("expected an error when the config doesn't validate against the live CRD schema")
	}
}

func TestCRDConversionConfigValidator_HappyPath_NoErrorNoWarnings(t *testing.T) {
	crd := establishedCRD("foos.example.org")
	c := newFakeClient(crd).Build()
	v := &CRDConversionConfigValidator{Client: c, Enabled: true}
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	warnings, err := v.ValidateCreate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for a fully well-formed, live-validated config, got %v", warnings)
	}
}

func TestCRDConversionConfigValidator_ValidateDelete_AlwaysAllowed(t *testing.T) {
	v := &CRDConversionConfigValidator{Client: newFakeClient().Build(), Enabled: false}
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	if _, err := v.ValidateDelete(context.Background(), cfg); err != nil {
		t.Fatalf("ValidateDelete must never reject, even when disabled: %v", err)
	}
}
