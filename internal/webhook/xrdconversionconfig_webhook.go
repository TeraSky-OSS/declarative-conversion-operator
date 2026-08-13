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

// Package webhook implements this operator's OWN admission webhooks —
// fast, synchronous validation of XRDConversionConfig and
// ConversionWebhookServer objects themselves at kubectl-apply time. This is
// a completely different webhook surface from the per-XRD CRD CONVERSION
// webhook served by cmd/webhook-server: these validators run inside the
// manager binary and never touch Crossplane custom resource data.
//
// Validators live here rather than in api/v1alpha1 (the usual kubebuilder
// convention) because ConversionWebhookServer's delete validation needs
// internal/assign, which itself imports api/v1alpha1 — putting the
// validator in api/v1alpha1 would create an import cycle.
package webhook

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// XRDConversionConfigValidator validates XRDConversionConfig objects at
// admission time: structural well-formedness that the CRD's OpenAPI schema
// can't express (exactly one params field per strategy, no duplicate spoke
// versions, hub != spoke), the one-config-per-XRD invariant the engine and
// controller both assume, and — best-effort, when the target XRD already
// exists — the same lossy/coverage analysis the controller runs, so a
// broken config is rejected immediately rather than accepted and only
// reported as Invalid in status.
type XRDConversionConfigValidator struct {
	Client client.Client
	// Enabled mirrors --enable-xrd-support. When false, the manager never
	// watches Crossplane's CompositeResourceDefinition GVK at all (it may
	// not even exist on this cluster), so an XRDConversionConfig can never
	// be reconciled; rejecting creates/updates here gives an immediate,
	// clear reason instead of a config that silently sits unreconciled
	// forever.
	Enabled bool
}

var _ admission.Validator[*teraskyv1alpha1.XRDConversionConfig] = &XRDConversionConfigValidator{}

func (v *XRDConversionConfigValidator) ValidateCreate(ctx context.Context, cfg *teraskyv1alpha1.XRDConversionConfig) (admission.Warnings, error) {
	return v.validate(ctx, cfg)
}

func (v *XRDConversionConfigValidator) ValidateUpdate(ctx context.Context, _, newCfg *teraskyv1alpha1.XRDConversionConfig) (admission.Warnings, error) {
	return v.validate(ctx, newCfg)
}

func (v *XRDConversionConfigValidator) ValidateDelete(context.Context, *teraskyv1alpha1.XRDConversionConfig) (admission.Warnings, error) {
	return nil, nil
}

func (v *XRDConversionConfigValidator) validate(ctx context.Context, cfg *teraskyv1alpha1.XRDConversionConfig) (admission.Warnings, error) {
	if !v.Enabled {
		return nil, fmt.Errorf("XRD conversion support is disabled on this installation (--enable-xrd-support=false); enable it before creating XRDConversionConfig objects")
	}

	var warnings admission.Warnings

	if err := ValidateStructure(cfg); err != nil {
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

	xrd := &unstructured.Unstructured{}
	xrd.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	err := v.Client.Get(ctx, types.NamespacedName{Name: cfg.Spec.TargetXRD.Name}, xrd)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("target XRD %q does not currently exist; skipping live schema validation until it does", cfg.Spec.TargetXRD.Name))
		return warnings, nil
	}

	ruleSets, err := cfg.ToRuleSets()
	if err != nil {
		return warnings, fmt.Errorf("invalid rule configuration: %w", err)
	}
	report, err := engine.Analyze(engine.AnalyzeInput{Source: xrdadapter.New(xrd), HubVersion: cfg.Spec.HubVersion, Spokes: ruleSets})
	if err != nil {
		return warnings, fmt.Errorf("validating against live XRD schema: %w", err)
	}
	if report.HasErrors() {
		return warnings, fmt.Errorf("configuration is invalid against the current XRD schema: %s", summarizeErrors(report))
	}
	return warnings, nil
}

func summarizeErrors(report engine.AnalyzeReport) string {
	msg := ""
	for _, sr := range report.SpokeReports {
		for _, e := range sr.Errors {
			msg += fmt.Sprintf("[spoke %s] %s; ", sr.Version, e.Message)
		}
	}
	return msg
}

// ValidateStructure catches shape problems the CRD's OpenAPI schema can't
// express: duplicate spoke versions, a spoke equal to the hub,
// strategy/params mismatches (including recursively inside ForEach), and
// UnmappedFieldPolicy=Warn without a justification reason.
func ValidateStructure(cfg *teraskyv1alpha1.XRDConversionConfig) error {
	if err := validateUnmappedFieldReason(cfg.Spec.UnmappedFieldPolicy, cfg.Spec.UnmappedFieldReason); err != nil {
		return err
	}
	return validateSpokesStructure(cfg.Spec.HubVersion, cfg.Spec.Spokes)
}

// ValidateCRDStructure is CRDConversionConfig's counterpart to
// ValidateStructure, sharing the same underlying check — the structural
// rules a rule set must satisfy don't depend on whether it's converting an
// XRD or a native CRD.
func ValidateCRDStructure(cfg *teraskyv1alpha1.CRDConversionConfig) error {
	if err := validateUnmappedFieldReason(cfg.Spec.UnmappedFieldPolicy, cfg.Spec.UnmappedFieldReason); err != nil {
		return err
	}
	return validateSpokesStructure(cfg.Spec.HubVersion, cfg.Spec.Spokes)
}

// validateUnmappedFieldReason enforces the documented contract that
// UnmappedFieldPolicy=Warn requires a non-empty UnmappedFieldReason.
// Without this check the reason field is silent dead API surface: adopters
// can set it (or omit it under Warn) and observe no behavior change.
func validateUnmappedFieldReason(policy teraskyv1alpha1.UnmappedFieldPolicy, reason string) error {
	if policy == teraskyv1alpha1.UnmappedFieldPolicyWarn && strings.TrimSpace(reason) == "" {
		return fmt.Errorf("spec.unmappedFieldReason is required when spec.unmappedFieldPolicy is Warn")
	}
	return nil
}

func validateSpokesStructure(hubVersion string, spokes []teraskyv1alpha1.SpokeVersionRules) error {
	if len(spokes) == 0 {
		return fmt.Errorf("spec.spokes must declare at least one spoke version")
	}
	seen := map[string]bool{}
	for _, s := range spokes {
		if s.Version == hubVersion {
			return fmt.Errorf("spoke version %q must not equal the hub version", s.Version)
		}
		if seen[s.Version] {
			return fmt.Errorf("spoke version %q is declared more than once", s.Version)
		}
		seen[s.Version] = true
		if err := validateRules(s.Rules, 0); err != nil {
			return fmt.Errorf("spoke %q: %w", s.Version, err)
		}
	}
	return nil
}

func validateRules(rules []teraskyv1alpha1.ConversionRule, depth int) error {
	for i, r := range rules {
		if err := validateOneRule(r, depth); err != nil {
			return fmt.Errorf("rule %d: %w", i, err)
		}
	}
	return nil
}

func validateOneRule(r teraskyv1alpha1.ConversionRule, depth int) error {
	set := 0
	if r.FieldRename != nil {
		set++
	}
	if r.ScalarToObject != nil {
		set++
	}
	if r.ObjectToScalar != nil {
		set++
	}
	if r.SingletonArrayToObject != nil {
		set++
	}
	if r.ObjectToSingletonArray != nil {
		set++
	}
	if r.FieldsToMap != nil {
		set++
	}
	if r.MapToFields != nil {
		set++
	}
	if r.ToAnnotation != nil {
		set++
	}
	if r.ToLabel != nil {
		set++
	}
	if r.FromAnnotation != nil {
		set++
	}
	if r.FromLabel != nil {
		set++
	}
	if r.EnumRemap != nil {
		set++
	}
	if r.DefaultValue != nil {
		set++
	}
	if r.Constant != nil {
		set++
	}
	if r.Delete != nil {
		set++
	}
	if r.JSONPatch != nil {
		set++
	}
	if r.ForEach != nil {
		set++
	}
	if r.TypeCoerce != nil {
		set++
	}
	if r.ScalarToFields != nil {
		set++
	}
	if r.FieldsToScalar != nil {
		set++
	}
	if r.ArrayToMapByKey != nil {
		set++
	}
	if r.MapToArrayByKey != nil {
		set++
	}
	if r.NumericScale != nil {
		set++
	}
	if r.ListJoin != nil {
		set++
	}
	if r.ListSplit != nil {
		set++
	}
	if r.Quantity != nil {
		set++
	}
	if r.Duration != nil {
		set++
	}
	if r.MapKeyRename != nil {
		set++
	}
	if r.CEL != nil {
		set++
	}
	if set != 1 {
		return fmt.Errorf("strategy %q requires exactly one matching params field to be set, found %d", r.Strategy, set)
	}
	if r.ForEach != nil {
		if depth >= engine.MaxForEachDepth {
			return fmt.Errorf("ForEach nesting depth exceeds the supported maximum of %d", engine.MaxForEachDepth)
		}
		if err := validateRules(r.ForEach.Rules, depth+1); err != nil {
			return fmt.Errorf("forEach: %w", err)
		}
	}
	if r.When != nil {
		if strings.TrimSpace(r.When.Path) == "" {
			return fmt.Errorf("when.path is required")
		}
	}
	if r.Strategy == teraskyv1alpha1.StrategyCEL && !r.AcknowledgeLossy {
		return fmt.Errorf("CEL is always treated as lossy; acknowledgeLossy must be true (losslessOverride is not supported)")
	}
	if r.Strategy == teraskyv1alpha1.StrategyFromLabel && r.FromLabel != nil && r.FromLabel.Serialization == "JSON" {
		return fmt.Errorf("FromLabel does not support serialization=JSON; use String (labels cannot carry JSON-quoted values)")
	}
	if r.ToLabel != nil {
		if msgs := k8svalidation.IsQualifiedName(r.ToLabel.Key); len(msgs) > 0 {
			return fmt.Errorf("ToLabel key %q is not a valid label key: %s", r.ToLabel.Key, strings.Join(msgs, "; "))
		}
	}
	if r.FromLabel != nil {
		if msgs := k8svalidation.IsQualifiedName(r.FromLabel.Key); len(msgs) > 0 {
			return fmt.Errorf("FromLabel key %q is not a valid label key: %s", r.FromLabel.Key, strings.Join(msgs, "; "))
		}
	}
	return nil
}

func (v *XRDConversionConfigValidator) validateUniqueTarget(ctx context.Context, cfg *teraskyv1alpha1.XRDConversionConfig) error {
	return validateRegistryKeyAvailable(ctx, v.Client, cfg.Spec.TargetXRD.Name, cfg.Name, "xrd")
}
