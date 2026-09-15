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
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func statusListKinds() map[schema.GroupVersionResource]string {
	m := claimListKinds()
	m[compositionGVR] = "CompositionList"
	m[xrdConversionConfigGVR] = "XRDConversionConfigList"
	return m
}

func newStatusFake(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), statusListKinds(), objs...)
}

func composition(name, kind, apiVersion string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.crossplane.io/v1",
		"kind":       "Composition",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"compositeTypeRef": map[string]any{"kind": kind, "apiVersion": apiVersion}},
	}}
}

func xrdConversionConfigObj(name, target, hub string, spokes []string) *unstructured.Unstructured {
	spokeList := make([]any, 0, len(spokes))
	for _, s := range spokes {
		spokeList = append(spokeList, map[string]any{"version": s})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "terasky.com/v1alpha1",
		"kind":       "XRDConversionConfig",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"targetXRD":  map[string]any{"name": target},
			"hubVersion": hub,
			"spokes":     spokeList,
		},
		"status": map[string]any{
			"phase": "Applied",
			"conditions": []any{
				map[string]any{"type": "Applied", "status": "True", "reason": "Applied"},
				map[string]any{"type": "ConversionPropagated", "status": "True", "reason": "Propagated"},
			},
		},
	}}
}

func TestRunCrossplaneStatus_AssemblesTheWholeMigrationState(t *testing.T) {
	dyn := newStatusFake(
		claimXRD(),
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v1", "v2"}, false),
		migrateCRD("widgets.e2e.example.org", "e2e.example.org", "Widget", "widgets", "v2", []string{"v2"}, true),
		legacyPinnedXR("a", "widget-v1"),
		widget("", "b", "v2", map[string]any{"n": "2"}),
		claimObject("team-a", "c1"),
		composition("widget-v1", "XWidget", "e2e.example.org/v1"),
		composition("widget-v2", "XWidget", "e2e.example.org/v2"),
		composition("unrelated", "XOther", "other.example.org/v1"),
		xrdConversionConfigObj("cfg", "xwidgets.e2e.example.org", "v2", []string{"v1"}),
	)

	rep, err := RunCrossplaneStatus(context.Background(), dyn, "xwidgets.e2e.example.org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.Scope != "LegacyCluster" {
		t.Errorf("scope = %q", rep.Scope)
	}
	if len(rep.Versions) != 2 {
		t.Fatalf("expected both versions, got %+v", rep.Versions)
	}
	for _, v := range rep.Versions {
		if !v.HasSpokeRules {
			t.Errorf("version %s should be covered (hub v2, spoke v1): %+v", v.Version, v)
		}
	}

	// Both generated CRDs, with their own storedVersions.
	if len(rep.GeneratedCRDs) != 2 {
		t.Fatalf("a claim-offering XRD has two generated CRDs, got %+v", rep.GeneratedCRDs)
	}
	if rep.GeneratedCRDs[0].Namespaced {
		t.Error("the LegacyCluster composite is cluster-scoped")
	}
	if !rep.GeneratedCRDs[1].Namespaced {
		t.Error("the claim CRD is namespaced")
	}
	if strings.Join(rep.GeneratedCRDs[0].StoredVersions, ",") != "v1,v2" {
		t.Errorf("composite storedVersions = %v", rep.GeneratedCRDs[0].StoredVersions)
	}

	// Compositions are filtered to this XRD's type, and pinned counts
	// attributed by name.
	if len(rep.Compositions) != 2 {
		t.Fatalf("expected the two matching Compositions, got %+v", rep.Compositions)
	}
	byName := map[string]int{}
	for _, c := range rep.Compositions {
		byName[c.Name] = c.XRs
	}
	if byName["widget-v1"] != 1 {
		t.Errorf("expected one XR pinned to widget-v1, got %v", byName)
	}
	// Two objects (one composite, one claim) carry no compositionRef.
	if rep.Unpinned != 2 {
		t.Errorf("unpinned = %d, want 2", rep.Unpinned)
	}

	if rep.Config == nil || rep.Config.Phase != "Applied" {
		t.Fatalf("expected the config's phase, got %+v", rep.Config)
	}
	if rep.Config.Conditions["ConversionPropagated"] != "True" {
		t.Errorf("expected ConversionPropagated to be surfaced: %+v", rep.Config.Conditions)
	}
}

func TestRunCrossplaneStatus_IsStrictlyReadOnly(t *testing.T) {
	dyn := newStatusFake(
		claimXRD(),
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v2"}, false),
		migrateCRD("widgets.e2e.example.org", "e2e.example.org", "Widget", "widgets", "v2", []string{"v2"}, true),
	)

	if _, err := RunCrossplaneStatus(context.Background(), dyn, "xwidgets.e2e.example.org"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range dyn.Actions() {
		switch a.GetVerb() {
		case "get", "list", "watch":
		default:
			t.Errorf("crossplane status must issue no writes, saw %q on %s", a.GetVerb(), a.GetResource().Resource)
		}
		if _, isPatch := a.(clienttesting.PatchAction); isPatch {
			t.Errorf("crossplane status must not patch: %+v", a)
		}
	}
}

func TestRunCrossplaneStatus_WarnsOnAServedVersionWithNoRules(t *testing.T) {
	// The shape that breaks reads: a version the apiserver will serve,
	// with nothing able to convert it.
	dyn := newStatusFake(
		claimXRD(),
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v2"}, false),
		migrateCRD("widgets.e2e.example.org", "e2e.example.org", "Widget", "widgets", "v2", []string{"v2"}, true),
		// The config covers only the hub — v1 is served but uncovered.
		xrdConversionConfigObj("cfg", "xwidgets.e2e.example.org", "v2", nil),
	)

	rep, err := RunCrossplaneStatus(context.Background(), dyn, "xwidgets.e2e.example.org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var buf bytes.Buffer
	rep.WriteTable(&buf)
	out := buf.String()
	if !strings.Contains(out, "v1 is served but no rule set covers it") {
		t.Errorf("expected a warning for the uncovered served version:\n%s", out)
	}
}

func TestRunCrossplaneStatus_NoConfigIsNotAnError(t *testing.T) {
	// "Nothing is configured yet" is a perfectly normal state to ask about
	// — arguably the most common one for this command.
	dyn := newStatusFake(
		claimXRD(),
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v2"}, false),
		migrateCRD("widgets.e2e.example.org", "e2e.example.org", "Widget", "widgets", "v2", []string{"v2"}, true),
	)

	rep, err := RunCrossplaneStatus(context.Background(), dyn, "xwidgets.e2e.example.org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Config != nil {
		t.Fatalf("expected no config, got %+v", rep.Config)
	}
	var buf bytes.Buffer
	rep.WriteTable(&buf)
	if !strings.Contains(buf.String(), "no XRDConversionConfig targets this XRD") {
		t.Errorf("the table should say so plainly:\n%s", buf.String())
	}
}

// TestRunCrossplaneStatus_ConfigListFailureIsNotReportedAsNone guards
// against a definite wrong statement: an RBAC denial on
// XRDConversionConfigs used to be swallowed and rendered as "none — no
// XRDConversionConfig targets this XRD", which is a different claim
// entirely from "I could not look".
func TestRunCrossplaneStatus_ConfigListFailureIsNotReportedAsNone(t *testing.T) {
	dyn := newStatusFake(
		claimXRD(),
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v2"}, false),
		migrateCRD("widgets.e2e.example.org", "e2e.example.org", "Widget", "widgets", "v2", []string{"v2"}, true),
	)
	dyn.PrependReactor("list", "xrdconversionconfigs", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "terasky.com", Resource: "xrdconversionconfigs"}, "", errForbidden)
	})

	rep, err := RunCrossplaneStatus(context.Background(), dyn, "xwidgets.e2e.example.org")
	if err != nil {
		t.Fatalf("a config-list failure must not fail the whole command: %v", err)
	}
	if !rep.ConfigLookupFailed {
		t.Error("expected the lookup failure to be recorded")
	}

	var buf bytes.Buffer
	rep.WriteTable(&buf)
	out := buf.String()
	if strings.Contains(out, "no XRDConversionConfig targets this XRD") {
		t.Errorf("a permissions failure must not read as a clean 'none':\n%s", out)
	}
	if !strings.Contains(out, "UNKNOWN") {
		t.Errorf("expected the table to say the answer is unknown:\n%s", out)
	}
	if !strings.Contains(strings.Join(rep.Warnings, " "), "could not list XRDConversionConfigs") {
		t.Errorf("expected a warning naming the failure: %v", rep.Warnings)
	}
}

var errForbidden = errors.New("user cannot list resource")
