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

func TestXRDConversionConfigValidator_Disabled_Rejected(t *testing.T) {
	c := newFakeClient().Build()
	v := &XRDConversionConfigValidator{Client: c, Enabled: false}
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	if _, err := v.ValidateCreate(context.Background(), cfg); err == nil {
		t.Fatalf("expected an error when XRD support is disabled")
	}
}

func TestXRDConversionConfigValidator_DuplicateTarget_Rejected(t *testing.T) {
	existing := renameRuleXRDConfig("existing", "xfoos.example.org")
	c := newFakeClient(existing).Build()
	v := &XRDConversionConfigValidator{Client: c, Enabled: true}
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org") // same target
	if _, err := v.ValidateCreate(context.Background(), cfg); err == nil {
		t.Fatalf("expected an error: only one config per XRD is supported")
	}
}

func TestXRDConversionConfigValidator_TargetMissing_WarnsAndSkipsLiveValidation(t *testing.T) {
	c := newFakeClient().Build()
	v := &XRDConversionConfigValidator{Client: c, Enabled: true}
	cfg := renameRuleXRDConfig("cfg", "missing.example.org")
	warnings, err := v.ValidateCreate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error when the target XRD doesn't exist yet: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected a warning about the missing target XRD")
	}
}

func TestXRDConversionConfigValidator_WebhookServerRefMissing_Warns(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	c := newFakeClient(xrd).Build()
	v := &XRDConversionConfigValidator{Client: c, Enabled: true}
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
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

func TestXRDConversionConfigValidator_LiveSchemaValidationFailure_Rejected(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	c := newFakeClient(xrd).Build()
	v := &XRDConversionConfigValidator{Client: c, Enabled: true}
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.Spokes[0].Rules[0].FieldRename.HubPath = "spec.doesNotExist" // hub field that isn't in the live schema
	if _, err := v.ValidateCreate(context.Background(), cfg); err == nil {
		t.Fatalf("expected an error when the config doesn't validate against the live XRD schema")
	}
}

func TestXRDConversionConfigValidator_HappyPath_NoErrorNoWarnings(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	c := newFakeClient(xrd).Build()
	v := &XRDConversionConfigValidator{Client: c, Enabled: true}
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	warnings, err := v.ValidateCreate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for a fully well-formed, live-validated config, got %v", warnings)
	}
}

func TestXRDConversionConfigValidator_ValidateDelete_AlwaysAllowed(t *testing.T) {
	v := &XRDConversionConfigValidator{Client: newFakeClient().Build(), Enabled: false}
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	if _, err := v.ValidateDelete(context.Background(), cfg); err != nil {
		t.Fatalf("ValidateDelete must never reject, even when disabled: %v", err)
	}
}
