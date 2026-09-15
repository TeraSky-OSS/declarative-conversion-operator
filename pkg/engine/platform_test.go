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

package engine

import (
	"strings"
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// platformSource is a fakeSource that also declares injected paths, so
// these tests exercise the same opt-in seam pkg/xrdadapter uses.
type platformSource struct {
	fakeSource
	injected PlatformInjectedPaths
}

func (p platformSource) PlatformInjectedPaths() PlatformInjectedPaths { return p.injected }

func diagsWithCode(diags []Diagnostic, code string) []Diagnostic {
	var out []Diagnostic
	for _, d := range diags {
		if d.Code == code {
			out = append(out, d)
		}
	}
	return out
}

func spokeDiags(t *testing.T, report AnalyzeReport, version string) []Diagnostic {
	t.Helper()
	for _, sr := range report.SpokeReports {
		if sr.Version == version {
			return append(append([]Diagnostic{}, sr.Errors...), sr.Warnings...)
		}
	}
	t.Fatalf("no spoke report for %q", version)
	return nil
}

// analyzeWithInjected runs Analyze over a two-version resource whose
// authored schemas and rules the caller supplies.
func analyzeWithInjected(t *testing.T, hub, spoke extv1.JSONSchemaProps, rules []Rule, injected PlatformInjectedPaths) AnalyzeReport {
	t.Helper()
	src := platformSource{
		fakeSource: fakeSource{versions: []VersionSchema{
			{Name: "v2", Schema: &hub, Served: true, Storage: true},
			{Name: "v1", Schema: &spoke, Served: true},
		}},
		injected: injected,
	}
	report, err := Analyze(AnalyzeInput{
		Source:     src,
		HubVersion: "v2",
		Spokes:     []RuleSet{{SpokeVersion: "v1", Rules: rules, UnmappedFieldPolicy: UnmappedFieldPolicyWarn}},
	})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return report
}

func TestAnalyze_AuthoredFieldShadowedByPlatform(t *testing.T) {
	// The author declares spec.crossplane on a Namespaced XR. Crossplane
	// copies its own spec.crossplane over the top in the generated CRD, so
	// this subtree never exists at runtime — which, before this check,
	// compiled with zero errors and zero warnings.
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{
			"sizeGiB":    intSchema(),
			"crossplane": objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()}),
		}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{"size": intSchema()}),
	})
	report := analyzeWithInjected(t, hub, spoke, []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("spec.sizeGiB"), SpokePath: ParsePath("spec.size")}},
	}, PlatformInjectedPaths{Platform: "Crossplane", Paths: []FieldPath{{"spec", "crossplane"}, {"status", "conditions"}}})

	got := diagsWithCode(spokeDiags(t, report, "v1"), CodeAuthoredFieldShadowedByPlatform)
	if len(got) != 1 {
		t.Fatalf("expected exactly one shadowing diagnostic, got %d: %v", len(got), got)
	}
	d := got[0]
	if d.Severity != SeverityError {
		t.Errorf("severity = %q, want Error", d.Severity)
	}
	if d.FieldPath != "spec.crossplane" {
		t.Errorf("FieldPath = %q", d.FieldPath)
	}
	for _, want := range []string{"spec.crossplane", "Crossplane", `version "v2"`} {
		if !strings.Contains(d.Message, want) {
			t.Errorf("message %q does not mention %q", d.Message, want)
		}
	}
	if !report.HasErrors() {
		t.Error("expected the report to carry errors, so no plan is compiled")
	}
}

func TestAnalyze_ShadowingIsDetectedOnTheSpokeSchemaToo(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{"sizeGiB": intSchema()}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"spec":   objSchema(map[string]extv1.JSONSchemaProps{"size": intSchema()}),
		"status": objSchema(map[string]extv1.JSONSchemaProps{"conditions": arrSchema(strSchema(), nil)}),
	})
	report := analyzeWithInjected(t, hub, spoke, []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("spec.sizeGiB"), SpokePath: ParsePath("spec.size")}},
	}, PlatformInjectedPaths{Platform: "Crossplane", Paths: []FieldPath{{"spec", "crossplane"}, {"status", "conditions"}}})

	got := diagsWithCode(spokeDiags(t, report, "v1"), CodeAuthoredFieldShadowedByPlatform)
	if len(got) != 1 || got[0].FieldPath != "status.conditions" {
		t.Fatalf("expected the spoke's status.conditions to be flagged, got %v", got)
	}
	if !strings.Contains(got[0].Message, `spoke version "v1"`) {
		t.Errorf("message should name the side and version it found the collision on: %q", got[0].Message)
	}
}

func TestAnalyze_RuleTargetsInjectedPath(t *testing.T) {
	// A rule aimed at LegacyCluster machinery. Passthrough already carries
	// these fields untouched in both directions, so converting them is
	// never right.
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{
			"compositionRef": objSchema(map[string]extv1.JSONSchemaProps{"name": strSchema()}),
		}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{
			"compositionReference": objSchema(map[string]extv1.JSONSchemaProps{"name": strSchema()}),
		}),
	})
	report := analyzeWithInjected(t, hub, spoke, []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{
			HubPath: ParsePath("spec.compositionRef"), SpokePath: ParsePath("spec.compositionReference")}},
	}, PlatformInjectedPaths{Platform: "Crossplane", Paths: []FieldPath{{"spec", "compositionRef"}}})

	got := diagsWithCode(spokeDiags(t, report, "v1"), CodeRuleTargetsInjectedPath)
	if len(got) != 1 {
		t.Fatalf("expected one rule-target diagnostic, got %d: %v", len(got), got)
	}
	if got[0].Severity != SeverityError {
		t.Errorf("severity = %q, want Error", got[0].Severity)
	}
	if got[0].FieldPath != "spec.compositionRef" {
		t.Errorf("FieldPath = %q", got[0].FieldPath)
	}
	if !strings.Contains(got[0].Message, "hub path") {
		t.Errorf("message should say which side the offending path is on: %q", got[0].Message)
	}
}

func TestAnalyze_RuleInsideAnInjectedSubtreeIsCaught(t *testing.T) {
	// The path does not equal an injected root — it sits underneath one.
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{
			"crossplane": objSchema(map[string]extv1.JSONSchemaProps{"compositionUpdatePolicy": strSchema()}),
		}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{"updatePolicy": strSchema()}),
	})
	report := analyzeWithInjected(t, hub, spoke, []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{
			HubPath: ParsePath("spec.crossplane.compositionUpdatePolicy"), SpokePath: ParsePath("spec.updatePolicy")}},
	}, PlatformInjectedPaths{Platform: "Crossplane", Paths: []FieldPath{{"spec", "crossplane"}}})

	got := diagsWithCode(spokeDiags(t, report, "v1"), CodeRuleTargetsInjectedPath)
	if len(got) != 1 {
		t.Fatalf("expected the nested path to be caught, got %v", got)
	}
	if !strings.Contains(got[0].Message, `"spec.crossplane"`) {
		t.Errorf("message should name the injected root it fell under: %q", got[0].Message)
	}
}

func TestAnalyze_LegacyClusterMayDeclareSpecCrossplane(t *testing.T) {
	// The whole point of making this scope-aware: Crossplane injects no
	// spec.crossplane on a LegacyCluster XRD, so an author who declares
	// one there is doing something perfectly legitimate and must not be
	// blocked.
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{
			"crossplane": objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()}),
		}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{
			"crossplane": objSchema(map[string]extv1.JSONSchemaProps{"level": strSchema()}),
		}),
	})
	report := analyzeWithInjected(t, hub, spoke, []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{
			HubPath: ParsePath("spec.crossplane.tier"), SpokePath: ParsePath("spec.crossplane.level")}},
	}, PlatformInjectedPaths{Platform: "Crossplane", Paths: []FieldPath{
		{"spec", "compositionRef"}, {"spec", "claimRef"}, {"status", "conditions"},
	}})

	all := spokeDiags(t, report, "v1")
	if got := diagsWithCode(all, CodeAuthoredFieldShadowedByPlatform); len(got) != 0 {
		t.Errorf("LegacyCluster does not inject spec.crossplane; must not be flagged: %v", got)
	}
	if got := diagsWithCode(all, CodeRuleTargetsInjectedPath); len(got) != 0 {
		t.Errorf("a rule on an authored spec.crossplane.* is fine under LegacyCluster: %v", got)
	}
	if report.HasErrors() {
		t.Errorf("expected a clean compile, got errors: %v", spokeDiags(t, report, "v1"))
	}
}

func TestAnalyze_IndeterminateScopeDegradesToWarnings(t *testing.T) {
	// When the applicable set cannot be determined the caller hands in the
	// union of every candidate, which is a superset — so a false error
	// would block a config that is in fact correct. Warn instead.
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{
			"crossplane": objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()}),
		}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()}),
	})
	report := analyzeWithInjected(t, hub, spoke, []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{
			HubPath: ParsePath("spec.crossplane.tier"), SpokePath: ParsePath("spec.tier")}},
	}, PlatformInjectedPaths{
		Platform:        "Crossplane",
		Paths:           []FieldPath{{"spec", "crossplane"}},
		Uncertain:       true,
		UncertainReason: "the XRD's scope could not be determined",
	})

	all := spokeDiags(t, report, "v1")
	shadow := diagsWithCode(all, CodeAuthoredFieldShadowedByPlatform)
	target := diagsWithCode(all, CodeRuleTargetsInjectedPath)
	if len(shadow) != 1 || len(target) != 1 {
		t.Fatalf("expected both checks to still fire, got shadow=%v target=%v", shadow, target)
	}
	for _, d := range append(shadow, target...) {
		if d.Severity != SeverityWarning {
			t.Errorf("expected a warning under indeterminate scope, got %q: %s", d.Severity, d.Message)
		}
		if !strings.Contains(d.Message, "could not be determined") {
			t.Errorf("message should explain why it was softened: %q", d.Message)
		}
	}
	if report.HasErrors() {
		t.Error("indeterminate scope must never produce a false error")
	}
}

func TestAnalyze_NoInjectedPathsIsANoOp(t *testing.T) {
	// A SchemaSource that does not implement PlatformAwareSource — which
	// is pkg/crdadapter, since nothing rewrites a plain native CRD's
	// schema — must see no change in behaviour at all.
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{
			"crossplane": objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()}),
		}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{
			"crossplane": objSchema(map[string]extv1.JSONSchemaProps{"tier": strSchema()}),
		}),
	})
	src := fakeSource{versions: []VersionSchema{
		{Name: "v2", Schema: &hub, Served: true, Storage: true},
		{Name: "v1", Schema: &spoke, Served: true},
	}}
	report, err := Analyze(AnalyzeInput{Source: src, HubVersion: "v2", Spokes: []RuleSet{{SpokeVersion: "v1"}}})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	for _, d := range spokeDiags(t, report, "v1") {
		if d.Code == CodeAuthoredFieldShadowedByPlatform || d.Code == CodeRuleTargetsInjectedPath {
			t.Errorf("a platform-unaware source must produce no platform diagnostics: %v", d)
		}
	}
}

func TestSchemaDeclaresPath_DoesNotWalkIntoOpaqueSubtrees(t *testing.T) {
	// A path that disappears into a free-form map was never *declared* by
	// the author, so there is nothing for the platform to shadow and
	// flagging it would be a false positive.
	schema := objSchema(map[string]extv1.JSONSchemaProps{"spec": openMapSchema()})
	if schemaDeclaresPath(&schema, FieldPath{"spec", "crossplane"}) {
		t.Error("an opaque map's contents are not authored declarations")
	}
	if !schemaDeclaresPath(&schema, FieldPath{"spec"}) {
		t.Error("spec itself is declared")
	}
}
