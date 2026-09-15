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
	"reflect"
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// This file pins the exact set of properties Crossplane merges into every
// generated CRD, per scope, against the engine's passthrough.
//
// SOURCE OF TRUTH: crossplane-runtime/pkg/xcrd — schemas.go
// (CompositeResourceSpecProps, CompositeResourceClaimSpecProps,
// CompositeResourceStatusProps) and crd.go (ForCompositeResource,
// ForCompositeResourceClaim), read at commit 5b9c969. The tables below
// were transcribed from that code, not inferred from documentation; if
// Crossplane adds an injected property, this is the file to update.
//
// Why these tests exist at all: pkg/engine is deliberately unaware that
// Crossplane exists. It preserves these fields not by knowing their names
// but because passthroughUnknownOp works key-by-key against the *authored*
// openAPIV3Schema, and Crossplane never puts its machinery into that. That
// makes the behaviour layout-agnostic by construction — LegacyCluster's
// bare spec.* machinery needs no code of its own — but it also makes it
// correct by accident rather than by assertion, which is what a refactor
// of the passthrough walk would silently break. These cases turn the
// accident into a contract.
//
// Note there is no status.crossplane in ANY scope.

// injectedSpecCrossplane is the spec.crossplane subtree Crossplane injects
// for the modern scopes. resourceRefs items carry a namespace on Cluster
// but NOT on Namespaced — the one shape difference between the two.
func injectedSpecCrossplane(resourceRefsHaveNamespace bool) map[string]any {
	ref := map[string]any{"apiVersion": "example.org/v1", "kind": "Bucket", "name": "bucket-1"}
	if resourceRefsHaveNamespace {
		ref["namespace"] = "team-a"
	}
	return map[string]any{
		"compositionRef":              map[string]any{"name": "widget-aws"},
		"compositionSelector":         map[string]any{"matchLabels": map[string]any{"provider": "aws"}},
		"compositionRevisionRef":      map[string]any{"name": "widget-aws-abc123"},
		"compositionRevisionSelector": map[string]any{"matchLabels": map[string]any{"channel": "stable"}},
		"compositionUpdatePolicy":     "Automatic",
		"resourceRefs":                []any{ref},
	}
}

// injectedLegacySpec is the LegacyCluster machinery, which sits DIRECTLY
// under spec as siblings of the author's own fields rather than nested
// under spec.crossplane — plus claimRef and writeConnectionSecretToRef,
// which the modern scopes do not have at all.
func injectedLegacySpec() map[string]any {
	return map[string]any{
		"compositionRef":              map[string]any{"name": "widget-aws"},
		"compositionSelector":         map[string]any{"matchLabels": map[string]any{"provider": "aws"}},
		"compositionRevisionRef":      map[string]any{"name": "widget-aws-abc123"},
		"compositionRevisionSelector": map[string]any{"matchLabels": map[string]any{"channel": "stable"}},
		"compositionUpdatePolicy":     "Automatic",
		"resourceRefs": []any{
			map[string]any{"apiVersion": "example.org/v1", "kind": "Bucket", "name": "bucket-1", "namespace": "team-a"},
		},
		"claimRef": map[string]any{
			"apiVersion": "example.org/v1", "kind": "Widget", "name": "my-widget", "namespace": "team-a",
		},
		"writeConnectionSecretToRef": map[string]any{"name": "widget-conn", "namespace": "team-a"},
	}
}

// injectedClaimSpec is the claim CRD's machinery: resourceRef is a
// SINGULAR object rather than the resourceRefs array, compositeDeletePolicy
// exists only here, and writeConnectionSecretToRef takes only a name — the
// claim is already namespaced, so there is no namespace key.
func injectedClaimSpec() map[string]any {
	return map[string]any{
		"compositionRef":              map[string]any{"name": "widget-aws"},
		"compositionSelector":         map[string]any{"matchLabels": map[string]any{"provider": "aws"}},
		"compositionRevisionRef":      map[string]any{"name": "widget-aws-abc123"},
		"compositionRevisionSelector": map[string]any{"matchLabels": map[string]any{"channel": "stable"}},
		"compositionUpdatePolicy":     "Automatic",
		"compositeDeletePolicy":       "Foreground",
		"resourceRef": map[string]any{
			"apiVersion": "example.org/v1", "kind": "XWidget", "name": "my-widget-abc12",
		},
		"writeConnectionSecretToRef": map[string]any{"name": "widget-conn"},
	}
}

// injectedConditions is deliberately mixed: two conditions Crossplane owns
// and one written by somebody else entirely. The engine must never filter
// conditions by type — which conditions are whose is not knowable from the
// schema, so any filtering is guesswork that silently destroys data.
func injectedConditions() []any {
	return []any{
		map[string]any{"type": "Synced", "status": "True", "reason": "ReconcileSuccess", "lastTransitionTime": "2026-09-15T10:00:00Z"},
		map[string]any{"type": "Ready", "status": "True", "reason": "Available", "lastTransitionTime": "2026-09-15T10:01:00Z"},
		map[string]any{"type": "TeamPolicyApproved", "status": "False", "reason": "AwaitingReview", "message": "written by a controller that is not Crossplane"},
	}
}

func legacyStatusExtras() map[string]any {
	return map[string]any{
		"connectionDetails":   map[string]any{"lastPublishedTime": "2026-09-15T10:02:00Z"},
		"claimConditionTypes": []any{"DatabaseReady"},
	}
}

// injectedFieldCase is one scope's worth of injected data, alongside the
// authored fields a rule actually converts.
type injectedFieldCase struct {
	name string
	// injectedSpec is merged into the input object's spec, next to the
	// author's own fields. The authored schema never declares any of it.
	injectedSpec map[string]any
	// injectedStatus is merged into the input object's status.
	injectedStatus map[string]any
}

func crossplaneInjectedCases() []injectedFieldCase {
	return []injectedFieldCase{
		{
			name: "Namespaced",
			// Modern scopes nest everything under one spec.crossplane key.
			// Namespaced's resourceRefs items have NO namespace.
			injectedSpec:   map[string]any{"crossplane": injectedSpecCrossplane(false)},
			injectedStatus: map[string]any{"conditions": injectedConditions()},
		},
		{
			name: "Cluster",
			// Identical to Namespaced except resourceRefs items DO carry a
			// namespace. Asserting this difference is the point of having
			// both cases rather than one.
			injectedSpec:   map[string]any{"crossplane": injectedSpecCrossplane(true)},
			injectedStatus: map[string]any{"conditions": injectedConditions()},
		},
		{
			name: "LegacyCluster",
			// The v1 layout: machinery directly under spec, plus claimRef
			// and writeConnectionSecretToRef, and two extra status blocks.
			injectedSpec:   injectedLegacySpec(),
			injectedStatus: mergeMaps(map[string]any{"conditions": injectedConditions()}, legacyStatusExtras()),
		},
		{
			name: "LegacyClusterClaim",
			// The claim CRD built from the SAME authored schema: singular
			// resourceRef, compositeDeletePolicy, name-only connection
			// secret ref. Its status is the LegacyCluster set.
			injectedSpec:   injectedClaimSpec(),
			injectedStatus: mergeMaps(map[string]any{"conditions": injectedConditions()}, legacyStatusExtras()),
		},
	}
}

func mergeMaps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// TestPassthrough_CrossplaneInjectedFieldsPerScope asserts, for every
// scope, that each field Crossplane injects into the generated CRD
// survives conversion byte-identically in both directions and across a
// full round trip — while the authored fields the rules target still
// convert.
func TestPassthrough_CrossplaneInjectedFieldsPerScope(t *testing.T) {
	// The AUTHORED schema. Crossplane's injected properties are merged
	// into the generated CRD *over* this, and never appear here — which is
	// exactly why the engine cannot see them and passthrough has to carry
	// them.
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{
			"sizeGiB": intSchema(),
			"region":  strSchema(),
		}),
		"status": objSchema(map[string]extv1.JSONSchemaProps{"phase": strSchema()}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{
			"size":   intSchema(),
			"region": strSchema(),
		}),
		"status": objSchema(map[string]extv1.JSONSchemaProps{"state": strSchema()}),
	})
	rs := RuleSet{HubVersion: "v2", SpokeVersion: "v1", Rules: []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("spec.sizeGiB"), SpokePath: ParsePath("spec.size")}},
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("status.phase"), SpokePath: ParsePath("status.state")}},
	}}

	plan, diags, err := Compile(rs, &hub, &spoke)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if errs := diagMessages(diags, SeverityError); len(errs) != 0 {
		t.Fatalf("unexpected compile errors: %v", errs)
	}

	for _, tc := range crossplaneInjectedCases() {
		t.Run(tc.name, func(t *testing.T) {
			in := map[string]any{
				// metadata is Convert's own responsibility rather than
				// passthrough's, but it has to be here for the round trip
				// to be comparable at all — Convert always emits a
				// metadata key, so an input without one can never come
				// back byte-identical.
				"metadata": map[string]any{"name": "my-widget", "annotations": map[string]any{"team": "platform"}},
				"spec":     mergeMaps(map[string]any{"sizeGiB": int64(100), "region": "eu-west-1"}, tc.injectedSpec),
				"status":   mergeMaps(map[string]any{"phase": "Ready"}, tc.injectedStatus),
			}
			// Keep an independent copy: a passthrough bug that aliases and
			// then mutates the input would otherwise compare equal to
			// itself and pass.
			want := deepCopyAny(in).(map[string]any)

			out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
			if err != nil {
				t.Fatalf("hub->spoke: %v", err)
			}

			// The authored fields converted.
			outSpec := out["spec"].(map[string]any)
			outStatus := out["status"].(map[string]any)
			if outSpec["size"] != int64(100) {
				t.Errorf("ruled spec field did not convert: got %#v", outSpec["size"])
			}
			if outStatus["state"] != "Ready" {
				t.Errorf("ruled status field did not convert: got %#v", outStatus["state"])
			}
			if _, stale := outSpec["sizeGiB"]; stale {
				t.Errorf("the hub-side name leaked into the spoke output: %#v", outSpec)
			}

			// Every injected field came through untouched.
			wantSpec := want["spec"].(map[string]any)
			wantStatus := want["status"].(map[string]any)
			for key := range tc.injectedSpec {
				if !reflect.DeepEqual(outSpec[key], wantSpec[key]) {
					t.Errorf("hub->spoke: injected spec.%s changed\n got: %#v\nwant: %#v", key, outSpec[key], wantSpec[key])
				}
			}
			for key := range tc.injectedStatus {
				if !reflect.DeepEqual(outStatus[key], wantStatus[key]) {
					t.Errorf("hub->spoke: injected status.%s changed\n got: %#v\nwant: %#v", key, outStatus[key], wantStatus[key])
				}
			}

			// Round trip back to the hub.
			back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: out})
			if err != nil {
				t.Fatalf("spoke->hub: %v", err)
			}
			if !reflect.DeepEqual(back, want) {
				t.Errorf("round trip is not byte-identical\n got: %#v\nwant: %#v", back, want)
			}
		})
	}
}

// TestPassthrough_ResourceRefsNamespaceDifferenceIsCarried asserts the one
// shape difference between the modern scopes explicitly, rather than
// letting it hide inside the table above: Namespaced's resourceRefs items
// have no namespace key and Cluster's do, and passthrough must reproduce
// whichever it was given — it must never normalise one into the other.
func TestPassthrough_ResourceRefsNamespaceDifferenceIsCarried(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{"sizeGiB": intSchema()}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"spec": objSchema(map[string]extv1.JSONSchemaProps{"size": intSchema()}),
	})
	plan, _, err := Compile(RuleSet{HubVersion: "v2", SpokeVersion: "v1", Rules: []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("spec.sizeGiB"), SpokePath: ParsePath("spec.size")}},
	}}, &hub, &spoke)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	for _, withNS := range []bool{false, true} {
		name := "Namespaced_noNamespaceOnResourceRefs"
		if withNS {
			name = "Cluster_namespaceOnResourceRefs"
		}
		t.Run(name, func(t *testing.T) {
			injected := injectedSpecCrossplane(withNS)
			in := map[string]any{"spec": map[string]any{"sizeGiB": int64(10), "crossplane": injected}}
			want := deepCopyAny(injected)

			out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			gotRefs := out["spec"].(map[string]any)["crossplane"].(map[string]any)["resourceRefs"].([]any)
			item := gotRefs[0].(map[string]any)
			_, hasNS := item["namespace"]
			if hasNS != withNS {
				t.Errorf("resourceRefs[0].namespace present = %v, want %v (item: %#v)", hasNS, withNS, item)
			}
			if !reflect.DeepEqual(out["spec"].(map[string]any)["crossplane"], want) {
				t.Errorf("spec.crossplane changed:\n got: %#v\nwant: %#v", out["spec"].(map[string]any)["crossplane"], want)
			}
		})
	}
}

// TestPassthrough_ConditionsAreNeverFilteredByType is the assertion the
// old test only gestured at. Condition ownership is not knowable from the
// schema, so the engine must copy the array wholesale: a user-authored
// condition sitting between two Crossplane-owned ones has to survive in
// place, in order, with every one of its keys.
func TestPassthrough_ConditionsAreNeverFilteredByType(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"status": objSchema(map[string]extv1.JSONSchemaProps{"phase": strSchema()}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"status": objSchema(map[string]extv1.JSONSchemaProps{"state": strSchema()}),
	})
	plan, _, err := Compile(RuleSet{HubVersion: "v2", SpokeVersion: "v1", Rules: []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("status.phase"), SpokePath: ParsePath("status.state")}},
	}}, &hub, &spoke)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	conditions := injectedConditions()
	want := deepCopyAny(conditions)
	in := map[string]any{"status": map[string]any{"phase": "Ready", "conditions": conditions}}

	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
	if err != nil {
		t.Fatalf("hub->spoke: %v", err)
	}
	got := out["status"].(map[string]any)["conditions"]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("conditions were altered hub->spoke:\n got: %#v\nwant: %#v", got, want)
	}
	gotArr := got.([]any)
	if len(gotArr) != 3 {
		t.Fatalf("expected all three conditions, got %d", len(gotArr))
	}
	if gotArr[2].(map[string]any)["type"] != "TeamPolicyApproved" {
		t.Errorf("the non-Crossplane condition was dropped or reordered: %#v", gotArr)
	}

	back, err := Convert(ConvertInput{Plan: plan, Direction: SpokeToHub, Object: out})
	if err != nil {
		t.Fatalf("spoke->hub: %v", err)
	}
	if !reflect.DeepEqual(back["status"].(map[string]any)["conditions"], want) {
		t.Errorf("conditions were altered on the return leg: %#v", back["status"])
	}
}

// TestPassthrough_NoStatusCrossplaneIsInvented guards the negative half of
// the injected-field matrix: there is no status.crossplane in any scope,
// so the engine must never produce one.
func TestPassthrough_NoStatusCrossplaneIsInvented(t *testing.T) {
	hub := objSchema(map[string]extv1.JSONSchemaProps{
		"status": objSchema(map[string]extv1.JSONSchemaProps{"phase": strSchema()}),
	})
	spoke := objSchema(map[string]extv1.JSONSchemaProps{
		"status": objSchema(map[string]extv1.JSONSchemaProps{"state": strSchema()}),
	})
	plan, _, err := Compile(RuleSet{HubVersion: "v2", SpokeVersion: "v1", Rules: []Rule{
		{Strategy: StrategyFieldRename, Params: FieldRenameParams{HubPath: ParsePath("status.phase"), SpokePath: ParsePath("status.state")}},
	}}, &hub, &spoke)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	in := map[string]any{"spec": map[string]any{"crossplane": injectedSpecCrossplane(false)}, "status": map[string]any{"phase": "Ready"}}
	out, err := Convert(ConvertInput{Plan: plan, Direction: HubToSpoke, Object: in})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if _, found := out["status"].(map[string]any)["crossplane"]; found {
		t.Fatalf("engine invented a status.crossplane, which Crossplane injects in no scope: %#v", out["status"])
	}
}

// deepCopyAny copies the JSON-shaped value trees these tests compare, so a
// passthrough implementation that aliases its input and then mutates it
// cannot pass by comparing a value against itself.
func deepCopyAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = deepCopyAny(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = deepCopyAny(val)
		}
		return out
	default:
		return v
	}
}
