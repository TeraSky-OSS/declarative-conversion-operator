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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

// claimXRD is a scope: LegacyCluster XRD with spec.claimNames — the only
// shape that generates a second CRD. The composite is cluster-scoped; its
// claims are namespaced.
func claimXRD() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.crossplane.io/v2",
		"kind":       "CompositeResourceDefinition",
		"metadata":   map[string]any{"name": "xwidgets.e2e.example.org"},
		"spec": map[string]any{
			"group":      "e2e.example.org",
			"scope":      "LegacyCluster",
			"names":      map[string]any{"kind": "XWidget", "plural": "xwidgets"},
			"claimNames": map[string]any{"kind": "Widget", "plural": "widgets"},
			"versions": []any{
				map[string]any{"name": "v1", "served": true, "referenceable": false},
				map[string]any{"name": "v2", "served": true, "referenceable": true},
			},
		},
	}}
}

func claimObject(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "e2e.example.org/v2",
		"kind":       "Widget",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec":       map[string]any{"n": "1"},
	}}
}

func claimListKinds() map[schema.GroupVersionResource]string {
	m := migrateListKinds()
	m[schema.GroupVersionResource{Group: "e2e.example.org", Version: "v2", Resource: "widgets"}] = "WidgetList"
	return m
}

func newClaimFake(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), claimListKinds(), objs...)
	dyn.PrependReactor("patch", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		p := action.(clienttesting.PatchAction)
		meta := map[string]any{"name": p.GetName()}
		if ns := p.GetNamespace(); ns != "" {
			meta["namespace"] = ns
		}
		return true, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "e2e.example.org/v2",
			"kind":       "XWidget",
			"metadata":   meta,
		}}, nil
	})
	return dyn
}

// claimCRDs is the pair Crossplane generates from claimXRD.
func claimCRDs() []runtime.Object {
	return []runtime.Object{
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v1", "v2"}, false),
		migrateCRD("widgets.e2e.example.org", "e2e.example.org", "Widget", "widgets", "v2", []string{"v1", "v2"}, true),
	}
}

func TestFetchLiveSamples_SamplesClaimsAsWellAsComposites(t *testing.T) {
	// Before this, --live listed spec.names.plural only, so a pre-upgrade
	// check on a claim-offering XRD silently covered roughly half the
	// objects that actually go through the webhook.
	dyn := newClaimFake(
		widget("", "composite-a", "v2", map[string]any{"n": "1"}),
		widget("", "composite-b", "v2", map[string]any{"n": "2"}),
		claimObject("team-a", "claim-a"),
	)

	samples, err := FetchLiveSamples(context.Background(), dyn, claimXRD(), "v2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("expected 2 composites + 1 claim, got %d: %+v", len(samples), samples)
	}

	byCRD := map[string]int{}
	for _, s := range samples {
		if s.CRD == "" || s.CRDRole == "" {
			t.Errorf("every live sample must name the CRD it came from: %+v", s)
		}
		byCRD[s.CRD]++
	}
	if byCRD["xwidgets.e2e.example.org"] != 2 || byCRD["widgets.e2e.example.org"] != 1 {
		t.Fatalf("samples not attributed per CRD: %v", byCRD)
	}
}

func TestFetchLiveSamples_NoClaimNamesMakesNoExtraCall(t *testing.T) {
	// An XRD with no claimNames must behave exactly as before — in
	// particular it must not LIST a claim GVR that does not exist, which
	// on a real cluster is a 404 and on the fake client is a panic.
	dyn := newMigrateFake(widget("default", "a", "v2", map[string]any{"n": "1"}))

	samples, err := FetchLiveSamples(context.Background(), dyn, migrateXRD("v2", true), "v2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	for _, a := range dyn.Actions() {
		if a.GetResource().Resource == "widgets" {
			t.Fatalf("an XRD with no claimNames must not touch a claim resource: %+v", a)
		}
	}
}

func TestSamplesByCRD_OnlyWhenMoreThanOneContributed(t *testing.T) {
	// "1 CRD, N samples" is what every other run already reports, so the
	// breakdown would be noise.
	if got := samplesByCRD([]Sample{{CRD: "a", CRDRole: "composite"}, {CRD: "a", CRDRole: "composite"}}); got != nil {
		t.Errorf("single-CRD run should not produce a breakdown, got %+v", got)
	}
	got := samplesByCRD([]Sample{
		{CRD: "xwidgets.e2e.example.org", CRDRole: "composite"},
		{CRD: "widgets.e2e.example.org", CRDRole: "claim"},
		{CRD: "xwidgets.e2e.example.org", CRDRole: "composite"},
	})
	if len(got) != 2 {
		t.Fatalf("expected two entries, got %+v", got)
	}
	// Order follows FetchLiveSamples: composite first, then claim.
	if got[0].CRD != "xwidgets.e2e.example.org" || got[0].Samples != 2 || got[0].Role != "composite" {
		t.Errorf("unexpected composite entry: %+v", got[0])
	}
	if got[1].CRD != "widgets.e2e.example.org" || got[1].Samples != 1 || got[1].Role != "claim" {
		t.Errorf("unexpected claim entry: %+v", got[1])
	}
}

func TestRunMigrateStorage_MigratesAndPrunesBothCRDs(t *testing.T) {
	// The headline bug: --prune-stored-versions pruned the composite only,
	// so the claim CRD kept listing the old version and Kubernetes still
	// refused to drop it — which is the entire point of running this.
	objs := append(claimCRDs(),
		claimXRD(),
		widget("", "composite-a", "v2", map[string]any{"n": "1"}),
		claimObject("team-a", "claim-a"),
		claimObject("team-b", "claim-b"),
	)
	dyn := newClaimFake(objs...)

	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		XRDName:             "xwidgets.e2e.example.org",
		PruneStoredVersions: true,
		Quiet:               true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Succeeded != 3 || rep.Failed != 0 {
		t.Fatalf("expected 1 composite + 2 claims migrated, got %d ok / %d failed", rep.Succeeded, rep.Failed)
	}
	if len(rep.CRDs) != 2 {
		t.Fatalf("expected a per-CRD breakdown of two, got %+v", rep.CRDs)
	}

	composite, claim := rep.CRDs[0], rep.CRDs[1]
	if composite.Role != "composite" || composite.Namespaced {
		t.Errorf("a LegacyCluster composite is cluster-scoped: %+v", composite)
	}
	if claim.Role != "claim" || !claim.Namespaced {
		t.Errorf("a claim CRD is namespace-scoped: %+v", claim)
	}
	for _, c := range rep.CRDs {
		if !c.Pruned {
			t.Errorf("%s was not pruned: %+v", c.CRD, c)
		}
		if len(c.StoredVersions) != 1 || c.StoredVersions[0] != "v2" {
			t.Errorf("%s storedVersions = %v, want [v2]", c.CRD, c.StoredVersions)
		}
	}

	// Both CRDs' status must actually have been written, not just
	// reported — this is the assertion the old behaviour would fail.
	for _, name := range []string{"xwidgets.e2e.example.org", "widgets.e2e.example.org"} {
		got, err := dyn.Resource(crdGVR).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("reading back %s: %v", name, err)
		}
		stored, _, _ := unstructured.NestedStringSlice(got.Object, "status", "storedVersions")
		if len(stored) != 1 || stored[0] != "v2" {
			t.Errorf("%s status.storedVersions = %v, want [v2]", name, stored)
		}
	}
}

func TestRunMigrateStorage_ClaimFailureBlocksThePruneOnBothCRDs(t *testing.T) {
	// A half-pruned pair still cannot drop the version, but has already
	// discarded the record of which objects were stored at it — so a
	// failure anywhere must block the prune everywhere.
	objs := append(claimCRDs(),
		claimXRD(),
		widget("", "composite-a", "v2", map[string]any{"n": "1"}),
		claimObject("team-a", "claim-a"),
	)
	dyn := newClaimFake(objs...)
	dyn.PrependReactor("patch", "widgets", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, context.DeadlineExceeded
	})

	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		XRDName:             "xwidgets.e2e.example.org",
		PruneStoredVersions: true,
		Quiet:               true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Failed != 1 {
		t.Fatalf("expected the claim apply to fail, got %d failures", rep.Failed)
	}
	for _, c := range rep.CRDs {
		if c.Pruned {
			t.Errorf("%s must not be pruned when the other CRD failed: %+v", c.CRD, c)
		}
	}
	if !rep.HasFailures() {
		t.Error("a failed claim apply must be visible to the exit code")
	}

	joined := strings.Join(rep.Warnings, " | ")
	if !strings.Contains(joined, "widgets.e2e.example.org") || !strings.Contains(joined, "claim") {
		t.Errorf("the skip message must name which CRD failed, got %q", joined)
	}

	// And nothing was written to either CRD's status.
	for _, name := range []string{"xwidgets.e2e.example.org", "widgets.e2e.example.org"} {
		got, err := dyn.Resource(crdGVR).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("reading back %s: %v", name, err)
		}
		stored, _, _ := unstructured.NestedStringSlice(got.Object, "status", "storedVersions")
		if len(stored) != 2 {
			t.Errorf("%s storedVersions = %v, want both versions still listed", name, stored)
		}
	}
}

func TestRunMigrateStorage_SingleCRDShapeIsUnchanged(t *testing.T) {
	// An XRD with no claimNames must produce exactly the report it always
	// has, so existing JSON consumers keep working.
	dyn := newMigrateFake(
		migrateXRD("v2", true),
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v1", "v2"}, true),
		widget("default", "a", "v2", map[string]any{"n": "1"}),
	)

	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		XRDName:             "xwidgets.e2e.example.org",
		PruneStoredVersions: true,
		Quiet:               true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.CRDs != nil {
		t.Errorf("a single-CRD run must not grow a breakdown: %+v", rep.CRDs)
	}
	if !rep.Pruned {
		t.Error("the top-level Pruned flag must still be set on a single-CRD run")
	}
	for _, o := range rep.Objects {
		if o.CRD != "" {
			t.Errorf("a single-CRD run must not tag objects with a CRD: %+v", o)
		}
	}
}

func TestRunMigrateStorage_ClaimSideIsNamespacedAndCompositeIsNot(t *testing.T) {
	// The listing code must not assume one scope for both: a namespaced
	// Apply against the cluster-scoped composite (or vice versa) is a
	// different request entirely.
	objs := append(claimCRDs(),
		claimXRD(),
		widget("", "composite-a", "v2", map[string]any{"n": "1"}),
		claimObject("team-a", "claim-a"),
	)
	dyn := newClaimFake(objs...)

	if _, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		XRDName: "xwidgets.e2e.example.org",
		Quiet:   true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, p := range applyPatches(t, dyn) {
		switch p.GetResource().Resource {
		case "xwidgets":
			if p.GetNamespace() != "" {
				t.Errorf("composite apply must be cluster-scoped, got namespace %q", p.GetNamespace())
			}
		case "widgets":
			if p.GetNamespace() == "" {
				t.Error("claim apply must carry a namespace")
			}
			body := decodePatch(t, p)
			meta, _ := body["metadata"].(map[string]any)
			if meta["namespace"] == nil {
				t.Errorf("claim patch body must carry metadata.namespace: %#v", body)
			}
			if body["kind"] != "Widget" {
				t.Errorf("claim patch must use the claim kind, got %v", body["kind"])
			}
		}
	}
}

func TestRunMigrateStorage_NamespaceStillRefusesPrune(t *testing.T) {
	objs := append(claimCRDs(), claimXRD(), claimObject("team-a", "claim-a"))
	dyn := newClaimFake(objs...)

	_, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		XRDName:             "xwidgets.e2e.example.org",
		Namespace:           "team-a",
		PruneStoredVersions: true,
		Quiet:               true,
	})
	if err == nil || !strings.Contains(err.Error(), "--prune-stored-versions cannot be combined with --namespace") {
		t.Fatalf("expected the prune/namespace refusal, got %v", err)
	}
}

func TestRunMigrateStorage_NamespaceWarnsPerClusterScopedCRD(t *testing.T) {
	// On a LegacyCluster XRD only the claim side is namespaced, so
	// --namespace silently covers half the objects unless we say so.
	objs := append(claimCRDs(),
		claimXRD(),
		widget("", "composite-a", "v2", map[string]any{"n": "1"}),
		claimObject("team-a", "claim-a"),
	)
	dyn := newClaimFake(objs...)

	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		XRDName:   "xwidgets.e2e.example.org",
		Namespace: "team-a",
		Quiet:     true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(rep.Warnings, " | ")
	if !strings.Contains(joined, "xwidgets.e2e.example.org") || !strings.Contains(joined, "cluster-scoped") {
		t.Errorf("expected a warning naming the cluster-scoped composite, got %q", joined)
	}
}
