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
)

func TestValidateRegistryKey_XRDVsCRD_Rejected(t *testing.T) {
	existingXRD := renameRuleXRDConfig("xrd-cfg", "shared.example.org")
	c := newFakeClient(existingXRD).Build()

	v := &CRDConversionConfigValidator{Client: c, Enabled: true}
	cfg := renameRuleCRDConfig("crd-cfg", "shared.example.org")
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil {
		t.Fatalf("expected cross-kind registry key collision to be rejected")
	}
	if !strings.Contains(err.Error(), "XRDConversionConfig \"xrd-cfg\"") {
		t.Fatalf("expected error to name the colliding XRD config, got: %v", err)
	}
	if !strings.Contains(err.Error(), "shared.example.org") {
		t.Fatalf("expected error to name the colliding target, got: %v", err)
	}
}

func TestValidateRegistryKey_CRDVsXRD_Rejected(t *testing.T) {
	existingCRD := renameRuleCRDConfig("crd-cfg", "shared.example.org")
	c := newFakeClient(existingCRD).Build()

	v := &XRDConversionConfigValidator{Client: c, Enabled: true}
	cfg := renameRuleXRDConfig("xrd-cfg", "shared.example.org")
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil {
		t.Fatalf("expected cross-kind registry key collision to be rejected")
	}
	if !strings.Contains(err.Error(), "CRDConversionConfig \"crd-cfg\"") {
		t.Fatalf("expected error to name the colliding CRD config, got: %v", err)
	}
}

func TestValidateRegistryKey_CRDVsCRD_Rejected(t *testing.T) {
	existing := renameRuleCRDConfig("existing", "foos.example.org")
	c := newFakeClient(existing).Build()
	v := &CRDConversionConfigValidator{Client: c, Enabled: true}
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	if _, err := v.ValidateCreate(context.Background(), cfg); err == nil {
		t.Fatalf("expected same-kind CRD target collision to be rejected")
	}
}

func TestValidateRegistryKey_UpdateSelf_Allowed(t *testing.T) {
	existing := renameRuleXRDConfig("cfg", "xfoos.example.org")
	xrd := establishedXRD("xfoos.example.org")
	c := newFakeClient(existing, xrd).Build()
	v := &XRDConversionConfigValidator{Client: c, Enabled: true}
	if _, err := v.ValidateUpdate(context.Background(), existing, existing); err != nil {
		t.Fatalf("updating a config must not collide with itself: %v", err)
	}
}
