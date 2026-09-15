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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clienttesting "k8s.io/client-go/testing"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// pinnedXR is an XR still pinned to a Composition by name, in the modern
// spec.crossplane layout.
func pinnedXR(namespace, name, composition string) *unstructured.Unstructured {
	obj := widget(namespace, name, "v2", map[string]any{"n": "1"})
	_ = unstructured.SetNestedMap(obj.Object, map[string]any{
		"compositionRef":         map[string]any{"name": composition},
		"compositionRevisionRef": map[string]any{"name": composition + "-abc123"},
	}, "spec", "crossplane")
	return obj
}

// legacyPinnedXR is the same thing in the LegacyCluster layout, with the
// machinery directly under spec.
func legacyPinnedXR(name, composition string) *unstructured.Unstructured {
	obj := widget("", name, "v2", map[string]any{"n": "1"})
	_ = unstructured.SetNestedMap(obj.Object, map[string]any{"name": composition}, "spec", "compositionRef")
	return obj
}

func mergePatches(t *testing.T, dyn interface{ Actions() []clienttesting.Action }) []clienttesting.PatchAction {
	t.Helper()
	var out []clienttesting.PatchAction
	for _, a := range dyn.Actions() {
		p, ok := a.(clienttesting.PatchAction)
		if !ok || p.GetPatchType() != types.MergePatchType {
			continue
		}
		out = append(out, p)
	}
	return out
}

func TestRunRetarget_ClearsThePinAndSetsTheSelector(t *testing.T) {
	dyn := newMigrateFake(
		migrateXRD("v2", true),
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v2"}, true),
		pinnedXR("default", "a", "widget-v1"),
	)

	rep, err := RunRetarget(context.Background(), dyn, RetargetOptions{
		XRDName: "xwidgets.e2e.example.org", To: "v2", Quiet: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Patched != 1 || rep.Failed != 0 {
		t.Fatalf("expected 1 patched, got %+v", rep)
	}

	patches := mergePatches(t, dyn)
	if len(patches) != 1 {
		t.Fatalf("expected one merge patch, got %d", len(patches))
	}
	var body map[string]any
	if err := json.Unmarshal(patches[0].GetPatch(), &body); err != nil {
		t.Fatalf("decoding patch: %v", err)
	}
	crossplane := body["spec"].(map[string]any)["crossplane"].(map[string]any)

	// An explicit null is the point: Server-Side Apply can only remove a
	// field the applying manager already owns, and compositionRef is owned
	// by whoever pinned it — so an apply that merely omits the field would
	// leave every object still pinned.
	if v, ok := crossplane["compositionRef"]; !ok || v != nil {
		t.Errorf("compositionRef must be explicitly nulled, got %#v", crossplane["compositionRef"])
	}
	if v, ok := crossplane["compositionRevisionRef"]; !ok || v != nil {
		t.Errorf("compositionRevisionRef must be explicitly nulled, got %#v", crossplane["compositionRevisionRef"])
	}
	labels := crossplane["compositionSelector"].(map[string]any)["matchLabels"].(map[string]any)
	if labels[defaultXRDAPIVersionLabel] != "v2" {
		t.Errorf("selector = %#v", labels)
	}
}

func TestRunRetarget_LegacyClusterUsesTheBareSpecLayout(t *testing.T) {
	// Under LegacyCluster the machinery sits directly under spec, not
	// under spec.crossplane — patching the wrong subtree would silently
	// create a field Crossplane never reads.
	objs := append(claimCRDs(), claimXRD(), legacyPinnedXR("a", "widget-v1"), claimObject("team-a", "c1"))
	dyn := newClaimFake(objs...)

	rep, err := RunRetarget(context.Background(), dyn, RetargetOptions{
		XRDName: "xwidgets.e2e.example.org", To: "v2", Quiet: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Scope != string(xrdadapter.ScopeLegacyCluster) {
		t.Fatalf("scope = %q", rep.Scope)
	}
	if rep.Patched != 2 {
		t.Fatalf("expected the composite and the claim to be retargeted, got %+v", rep.Objects)
	}

	for _, p := range mergePatches(t, dyn) {
		var body map[string]any
		if err := json.Unmarshal(p.GetPatch(), &body); err != nil {
			t.Fatalf("decoding patch: %v", err)
		}
		spec := body["spec"].(map[string]any)
		if _, wrong := spec["crossplane"]; wrong {
			t.Errorf("LegacyCluster must not nest under spec.crossplane: %#v", spec)
		}
		if _, ok := spec["compositionRef"]; !ok {
			t.Errorf("expected spec.compositionRef in the patch: %#v", spec)
		}
	}
}

func TestRunRetarget_ClaimsAreRetargetedToo(t *testing.T) {
	objs := append(claimCRDs(), claimXRD(), legacyPinnedXR("a", "w1"), claimObject("team-a", "c1"), claimObject("team-b", "c2"))
	dyn := newClaimFake(objs...)

	rep, err := RunRetarget(context.Background(), dyn, RetargetOptions{
		XRDName: "xwidgets.e2e.example.org", To: "v2", Quiet: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byCRD := map[string]int{}
	for _, o := range rep.Objects {
		byCRD[o.CRD]++
	}
	if byCRD["widgets.e2e.example.org"] != 2 {
		t.Fatalf("expected both claims, got %v", byCRD)
	}
	// Claims are namespaced; the composite under LegacyCluster is not.
	for _, p := range mergePatches(t, dyn) {
		switch p.GetResource().Resource {
		case "widgets":
			if p.GetNamespace() == "" {
				t.Error("claim patch must carry a namespace")
			}
		case "xwidgets":
			if p.GetNamespace() != "" {
				t.Errorf("LegacyCluster composite patch must be cluster-scoped, got %q", p.GetNamespace())
			}
		}
	}
}

func TestRunRetarget_AlreadyOnTargetIsSkipped(t *testing.T) {
	// A re-run must be visibly a no-op, not a second pile of writes.
	obj := widget("default", "a", "v2", map[string]any{"n": "1"})
	_ = unstructured.SetNestedMap(obj.Object, map[string]any{
		"compositionSelector": map[string]any{"matchLabels": map[string]any{defaultXRDAPIVersionLabel: "v2"}},
	}, "spec", "crossplane")

	dyn := newMigrateFake(
		migrateXRD("v2", true),
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v2"}, true),
		obj,
	)

	rep, err := RunRetarget(context.Background(), dyn, RetargetOptions{
		XRDName: "xwidgets.e2e.example.org", To: "v2", Quiet: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Skipped != 1 || rep.Patched != 0 {
		t.Fatalf("expected the object to be skipped, got %+v", rep)
	}
	if len(mergePatches(t, dyn)) != 0 {
		t.Error("a no-op must send no patch at all")
	}
}

func TestRunRetarget_DryRunWritesNothing(t *testing.T) {
	dyn := newMigrateFake(
		migrateXRD("v2", true),
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v2"}, true),
		pinnedXR("default", "a", "widget-v1"),
	)

	rep, err := RunRetarget(context.Background(), dyn, RetargetOptions{
		XRDName: "xwidgets.e2e.example.org", To: "v2", DryRun: true, Quiet: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.DryRun {
		t.Error("report should record that this was a dry run")
	}
	// The request still goes to the apiserver — that is how conversion and
	// admission get exercised — it just does not persist.
	if len(mergePatches(t, dyn)) != 1 {
		t.Fatalf("expected the dry-run request to still be sent")
	}
}

func TestRetargetPatchOptions(t *testing.T) {
	// The fake dynamic client records actions but not their options, so
	// the DryRun flag is asserted where it is built rather than where it
	// is sent.
	plain := RetargetOptions{}.patchOptions()
	if len(plain.DryRun) != 0 {
		t.Errorf("a normal run must not set DryRun, got %v", plain.DryRun)
	}
	if plain.FieldManager != defaultRetargetFieldManager {
		t.Errorf("FieldManager = %q", plain.FieldManager)
	}

	dry := RetargetOptions{DryRun: true, FieldManager: "custom"}.patchOptions()
	if len(dry.DryRun) != 1 || dry.DryRun[0] != metav1.DryRunAll {
		t.Errorf("DryRun = %v, want [All]", dry.DryRun)
	}
	if dry.FieldManager != "custom" {
		t.Errorf("FieldManager = %q, want the override", dry.FieldManager)
	}
}

func TestApplyCanary(t *testing.T) {
	cases := []struct {
		canary  string
		total   int
		want    int
		wantErr bool
	}{
		{canary: "", total: 10, want: 10},
		{canary: "3", total: 10, want: 3},
		{canary: "0", total: 10, want: 0},
		// A count larger than the population is a clamp, not an error.
		{canary: "50", total: 10, want: 10},
		{canary: "50%", total: 10, want: 5},
		{canary: "100%", total: 10, want: 10},
		{canary: "0%", total: 10, want: 0},
		// A non-zero percentage of a non-empty population must select at
		// least one: rounding 1% of 50 down to zero would make the flag
		// silently do nothing.
		{canary: "1%", total: 50, want: 1},
		{canary: "10%", total: 0, want: 0},
		{canary: "-1", total: 10, wantErr: true},
		{canary: "101%", total: 10, wantErr: true},
		{canary: "half", total: 10, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s_of_%d", tc.canary, tc.total), func(t *testing.T) {
			got, err := applyCanary(tc.total, tc.canary)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRunRetarget_CanaryIsReportedAndDeterministic(t *testing.T) {
	objs := []runtime.Object{
		migrateXRD("v2", true),
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v2"}, true),
	}
	for _, n := range []string{"a", "b", "c", "d"} {
		objs = append(objs, pinnedXR("default", n, "widget-v1"))
	}
	dyn := newMigrateFake(objs...)

	rep, err := RunRetarget(context.Background(), dyn, RetargetOptions{
		XRDName: "xwidgets.e2e.example.org", To: "v2", Canary: "50%", Quiet: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Total != 4 || rep.Selected != 2 || rep.Patched != 2 {
		t.Fatalf("expected 2 of 4 selected, got total=%d selected=%d patched=%d", rep.Total, rep.Selected, rep.Patched)
	}
	// The selection must be reported, or a partially-migrated fleet looks
	// like a completed one.
	joined := strings.Join(rep.Warnings, " | ")
	if !strings.Contains(joined, "--canary") || !strings.Contains(joined, "re-run") {
		t.Errorf("the canary selection must be reported: %q", joined)
	}
	// Deterministic: the listing order is by name, so the first two are a
	// and b, not an arbitrary pair.
	var names []string
	for _, o := range rep.Objects {
		names = append(names, o.Name)
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Errorf("canary selection is not deterministic by name: %v", names)
	}
}

func TestRunRetarget_RejectsAVersionTheXRDDoesNotHave(t *testing.T) {
	dyn := newMigrateFake(
		migrateXRD("v2", true),
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v2"}, true),
	)
	_, err := RunRetarget(context.Background(), dyn, RetargetOptions{
		XRDName: "xwidgets.e2e.example.org", To: "v99", Quiet: true,
	})
	if err == nil || !strings.Contains(err.Error(), "is not a version of XRD") {
		t.Fatalf("expected a clear rejection, got %v", err)
	}
}

func TestRunRetarget_RefusesWhenScopeIsIndeterminate(t *testing.T) {
	// Scope decides where the machinery fields sit, so guessing would
	// write a subtree Crossplane never reads and report success.
	//
	// A merely ABSENT spec.scope is no longer indeterminate — a live XRD is
	// always read at a known API version, which says how the apiserver
	// defaults it. What the resolver still cannot interpret is a value it
	// does not recognize, which is what a future Crossplane scope would
	// look like to this build.
	xrd := migrateXRD("v2", true)
	_ = unstructured.SetNestedField(xrd.Object, "Galactic", "spec", "scope")
	dyn := newMigrateFake(xrd,
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v2"}, true))

	_, err := RunRetarget(context.Background(), dyn, RetargetOptions{
		XRDName: "xwidgets.e2e.example.org", To: "v2", Quiet: true,
	})
	if err == nil || !strings.Contains(err.Error(), "scope could not be determined") {
		t.Fatalf("expected a refusal, got %v", err)
	}
}

func TestMachineryPrefix(t *testing.T) {
	cases := []struct {
		scope xrdadapter.Scope
		role  xrdadapter.GeneratedCRDRole
		want  string
	}{
		{xrdadapter.ScopeNamespaced, xrdadapter.RoleComposite, "spec.crossplane"},
		{xrdadapter.ScopeCluster, xrdadapter.RoleComposite, "spec.crossplane"},
		{xrdadapter.ScopeLegacyCluster, xrdadapter.RoleComposite, "spec"},
		// A claim keeps the v1 layout whatever the XRD's scope says —
		// though in practice only LegacyCluster has claims at all.
		{xrdadapter.ScopeNamespaced, xrdadapter.RoleClaim, "spec"},
		{xrdadapter.ScopeLegacyCluster, xrdadapter.RoleClaim, "spec"},
	}
	for _, tc := range cases {
		t.Run(string(tc.scope)+"/"+string(tc.role), func(t *testing.T) {
			if got := strings.Join(machineryPrefix(tc.scope, tc.role), "."); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
