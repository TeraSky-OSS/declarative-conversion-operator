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
	"fmt"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/crdadapter"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// CRDConversionConfigValidator is CRDConversionConfig's counterpart to
// XRDConversionConfigValidator, running the identical checks against a
// plain apiextensions.k8s.io/v1 CustomResourceDefinition instead of a
// Crossplane XRD.
type CRDConversionConfigValidator struct {
	Client client.Client
	// Enabled mirrors --enable-crd-support; see
	// XRDConversionConfigValidator.Enabled for why this is checked here
	// rather than left to the controller.
	Enabled bool
}

var _ admission.Validator[*teraskyv1alpha1.CRDConversionConfig] = &CRDConversionConfigValidator{}

func (v *CRDConversionConfigValidator) ValidateCreate(ctx context.Context, cfg *teraskyv1alpha1.CRDConversionConfig) (admission.Warnings, error) {
	return v.validate(ctx, cfg)
}

func (v *CRDConversionConfigValidator) ValidateUpdate(ctx context.Context, _, newCfg *teraskyv1alpha1.CRDConversionConfig) (admission.Warnings, error) {
	return v.validate(ctx, newCfg)
}

func (v *CRDConversionConfigValidator) ValidateDelete(context.Context, *teraskyv1alpha1.CRDConversionConfig) (admission.Warnings, error) {
	return nil, nil
}

func (v *CRDConversionConfigValidator) validate(ctx context.Context, cfg *teraskyv1alpha1.CRDConversionConfig) (admission.Warnings, error) {
	if !v.Enabled {
		return nil, fmt.Errorf("native CRD conversion support is disabled on this installation (--enable-crd-support=false); enable it before creating CRDConversionConfig objects")
	}

	var warnings admission.Warnings

	if err := ValidateCRDStructure(cfg); err != nil {
		return nil, err
	}

	if err := v.validateUniqueTarget(ctx, cfg); err != nil {
		return nil, err
	}

	if cfg.Spec.WebhookServerRef != nil {
		var server teraskyv1alpha1.ConversionWebhookServer
		if err := v.Client.Get(ctx, types.NamespacedName{Name: cfg.Spec.WebhookServerRef.Name}, &server); err != nil {
			warnings = append(warnings, fmt.Sprintf("webhookServerRef %q does not currently exist; the config will report Invalid until it does", cfg.Spec.WebhookServerRef.Name))
		}
	}

	var crd extv1.CustomResourceDefinition
	err := v.Client.Get(ctx, types.NamespacedName{Name: cfg.Spec.TargetCRD.Name}, &crd)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("target CustomResourceDefinition %q does not currently exist; skipping live schema validation until it does", cfg.Spec.TargetCRD.Name))
		return warnings, nil
	}

	ruleSets, err := cfg.ToRuleSets()
	if err != nil {
		return warnings, fmt.Errorf("invalid rule configuration: %w", err)
	}
	report, err := engine.Analyze(engine.AnalyzeInput{Source: crdadapter.New(&crd), HubVersion: cfg.Spec.HubVersion, Spokes: ruleSets})
	if err != nil {
		return warnings, fmt.Errorf("validating against live CRD schema: %w", err)
	}
	if report.HasErrors() {
		return warnings, fmt.Errorf("configuration is invalid against the current CRD schema: %s", summarizeErrors(report))
	}
	return warnings, nil
}

func (v *CRDConversionConfigValidator) validateUniqueTarget(ctx context.Context, cfg *teraskyv1alpha1.CRDConversionConfig) error {
	var list teraskyv1alpha1.CRDConversionConfigList
	if err := v.Client.List(ctx, &list); err != nil {
		return fmt.Errorf("listing existing CRDConversionConfigs: %w", err)
	}
	for _, other := range list.Items {
		if other.Name == cfg.Name {
			continue
		}
		if other.Spec.TargetCRD.Name == cfg.Spec.TargetCRD.Name {
			return fmt.Errorf("CRDConversionConfig %q already targets CRD %q; only one config per CRD is supported", other.Name, cfg.Spec.TargetCRD.Name)
		}
	}
	return nil
}
