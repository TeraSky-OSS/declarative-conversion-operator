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

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func migrateListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		{Group: "e2e.example.org", Version: "v2", Resource: "xwidgets"}:                                 "XWidgetList",
		{Group: extv1.GroupName, Version: "v1", Resource: "customresourcedefinitions"}:                  "CustomResourceDefinitionList",
		{Group: "apiextensions.crossplane.io", Version: "v2", Resource: "compositeresourcedefinitions"}: "CompositeResourceDefinitionList",
	}
}

func newMigrateFake(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), migrateListKinds(), objs...)
	// The fake client's default Apply path tries to strategic-merge-patch
	// into the Unstructured Go struct and rejects "metadata". Tests care
	// that Apply was invoked with the right body/options, not that the
	// fake tracker can SSA-merge, so succeed every patch unless a test
	// prepends a more specific reactor.
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

func migrateXRD(referenceable string, namespaced bool) *unstructured.Unstructured {
	scope := string(extv1.ClusterScoped)
	if namespaced {
		scope = string(extv1.NamespaceScoped)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.crossplane.io/v2",
		"kind":       "CompositeResourceDefinition",
		"metadata":   map[string]any{"name": "xwidgets.e2e.example.org"},
		"spec": map[string]any{
			"group": "e2e.example.org",
			"scope": scope,
			"names": map[string]any{"kind": "XWidget", "plural": "xwidgets"},
			"versions": []any{
				map[string]any{"name": "v1", "served": true, "referenceable": referenceable == "v1"},
				map[string]any{"name": "v2", "served": true, "referenceable": referenceable == "v2"},
			},
		},
	}}
}

func migrateCRD(name, group, kind, plural, storage string, stored []string, namespaced bool) *unstructured.Unstructured {
	scope := string(extv1.ClusterScoped)
	if namespaced {
		scope = string(extv1.NamespaceScoped)
	}
	versions := []any{
		map[string]any{"name": "v1", "served": true, "storage": storage == "v1"},
		map[string]any{"name": "v2", "served": true, "storage": storage == "v2"},
	}
	storedAny := make([]any, len(stored))
	for i, v := range stored {
		storedAny[i] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"group":    group,
			"scope":    scope,
			"names":    map[string]any{"kind": kind, "plural": plural},
			"versions": versions,
		},
		"status": map[string]any{"storedVersions": storedAny},
	}}
}

func applyPatches(t *testing.T, dyn *dynamicfake.FakeDynamicClient) []clienttesting.PatchAction {
	t.Helper()
	var out []clienttesting.PatchAction
	for _, a := range dyn.Actions() {
		p, ok := a.(clienttesting.PatchAction)
		if !ok {
			continue
		}
		if p.GetPatchType() != types.ApplyPatchType {
			continue
		}
		out = append(out, p)
	}
	return out
}

func decodePatch(t *testing.T, p clienttesting.PatchAction) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(p.GetPatch(), &m); err != nil {
		t.Fatalf("decoding apply patch: %v", err)
	}
	return m
}

func TestMigrateEmptyPatch_IdentityOnly(t *testing.T) {
	p := migrateEmptyPatch("e2e.example.org", "v2", "XWidget", "a", "default")
	if p.GetAPIVersion() != "e2e.example.org/v2" || p.GetKind() != "XWidget" {
		t.Fatalf("unexpected gvk: %s %s", p.GetAPIVersion(), p.GetKind())
	}
	if p.GetName() != "a" || p.GetNamespace() != "default" {
		t.Fatalf("unexpected identity: %s/%s", p.GetNamespace(), p.GetName())
	}
	if _, ok := p.Object["spec"]; ok {
		t.Fatalf("empty patch must not include spec: %#v", p.Object)
	}
	if _, ok := p.Object["status"]; ok {
		t.Fatalf("empty patch must not include status: %#v", p.Object)
	}
	meta, _ := p.Object["metadata"].(map[string]any)
	for _, k := range []string{"resourceVersion", "uid", "annotations", "labels", "managedFields"} {
		if _, ok := meta[k]; ok {
			t.Fatalf("empty patch metadata must not include %s: %#v", k, meta)
		}
	}

	cluster := migrateEmptyPatch("e2e.example.org", "v2", "XWidget", "a", "")
	cmeta, _ := cluster.Object["metadata"].(map[string]any)
	if _, ok := cmeta["namespace"]; ok {
		t.Fatalf("cluster-scoped patch must omit namespace: %#v", cmeta)
	}
}

func TestRunMigrateStorage_XRDAppliesEveryObject(t *testing.T) {
	dyn := newMigrateFake(
		migrateXRD("v2", true),
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v1", "v2"}, true),
		widget("default", "a", "v2", map[string]any{"n": "1"}),
		widget("prod", "b", "v2", map[string]any{"n": "2"}),
	)

	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		XRDName: "xwidgets.e2e.example.org",
		Quiet:   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.ResourceKind != "XRD" || rep.Resource != "xwidgets.e2e.example.org" {
		t.Fatalf("unexpected target: %+v", rep)
	}
	if rep.StorageVersion != "v2" || !rep.Namespaced {
		t.Fatalf("unexpected storage/scope: %+v", rep)
	}
	if rep.Succeeded != 2 || rep.Failed != 0 || rep.HasFailures() {
		t.Fatalf("unexpected counts: succeeded=%d failed=%d", rep.Succeeded, rep.Failed)
	}
	if len(rep.Objects) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(rep.Objects))
	}

	patches := applyPatches(t, dyn)
	if len(patches) != 2 {
		t.Fatalf("expected 2 apply patches, got %d: %#v", len(patches), dyn.Actions())
	}
	seen := map[string]bool{}
	for _, p := range patches {
		body := decodePatch(t, p)
		if body["apiVersion"] != "e2e.example.org/v2" || body["kind"] != "XWidget" {
			t.Fatalf("patch GVK: %#v", body)
		}
		if _, ok := body["spec"]; ok {
			t.Fatalf("patch included spec: %#v", body)
		}
		meta, _ := body["metadata"].(map[string]any)
		key := fmt.Sprintf("%s/%s", meta["namespace"], meta["name"])
		seen[key] = true
		if p.GetNamespace() == "" {
			t.Fatalf("namespaced apply used empty namespace for %s", key)
		}
		opts := p.(clienttesting.PatchActionImpl).PatchOptions
		if opts.Force == nil || !*opts.Force || opts.FieldManager != defaultMigrateFieldManager {
			t.Fatalf("apply options: %+v", opts)
		}
		if len(opts.DryRun) != 0 {
			t.Fatalf("unexpected dry-run: %+v", opts)
		}
	}
	if !seen["default/a"] || !seen["prod/b"] {
		t.Fatalf("missing patches: %v", seen)
	}
}

func TestRunMigrateStorage_CRDClusterScoped(t *testing.T) {
	dyn := newMigrateFake(
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v1", "v2"}, false),
		widget("", "cluster-a", "v2", nil),
		widget("", "cluster-b", "v2", nil),
	)

	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		CRDName: "xwidgets.e2e.example.org",
		Quiet:   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.ResourceKind != "CRD" || rep.Namespaced {
		t.Fatalf("unexpected target: %+v", rep)
	}
	if rep.Succeeded != 2 {
		t.Fatalf("succeeded=%d", rep.Succeeded)
	}
	patches := applyPatches(t, dyn)
	if len(patches) != 2 {
		t.Fatalf("expected 2 patches, got %d", len(patches))
	}
	for _, p := range patches {
		if p.GetNamespace() != "" {
			t.Fatalf("cluster-scoped apply used namespace %q", p.GetNamespace())
		}
		body := decodePatch(t, p)
		meta, _ := body["metadata"].(map[string]any)
		if _, ok := meta["namespace"]; ok {
			t.Fatalf("cluster-scoped patch included namespace: %#v", body)
		}
	}
}

func TestRunMigrateStorage_DryRun(t *testing.T) {
	dyn := newMigrateFake(
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v1", "v2"}, true),
		widget("default", "a", "v2", nil),
	)

	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		CRDName:             "xwidgets.e2e.example.org",
		DryRun:              true,
		PruneStoredVersions: true,
		Quiet:               true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.DryRun || rep.Pruned {
		t.Fatalf("dry-run should not prune: %+v", rep)
	}
	if len(rep.Warnings) == 0 || !strings.Contains(rep.Warnings[0], "--dry-run") {
		t.Fatalf("expected dry-run prune warning, got %v", rep.Warnings)
	}
	patches := applyPatches(t, dyn)
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch, got %d", len(patches))
	}
	opts := patches[0].(clienttesting.PatchActionImpl).PatchOptions
	if len(opts.DryRun) != 1 || opts.DryRun[0] != metav1.DryRunAll {
		t.Fatalf("expected DryRun All, got %+v", opts)
	}
}

func TestRunMigrateStorage_OneFailureDoesNotSkipRestAndBlocksPrune(t *testing.T) {
	dyn := newMigrateFake(
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v1", "v2"}, true),
		widget("default", "a", "v2", nil),
		widget("default", "b", "v2", nil),
		widget("default", "c", "v2", nil),
	)
	dyn.PrependReactor("patch", "xwidgets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		p := action.(clienttesting.PatchAction)
		if p.GetName() == "b" {
			return true, nil, fmt.Errorf("apply conflict")
		}
		return false, nil, nil
	})

	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		CRDName:             "xwidgets.e2e.example.org",
		PruneStoredVersions: true,
		Quiet:               true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.HasFailures() || rep.Failed != 1 || rep.Succeeded != 2 {
		t.Fatalf("expected 1 failure / 2 success, got %+v", rep)
	}
	if rep.Pruned {
		t.Fatalf("prune must not run after a failure")
	}
	if len(rep.Warnings) == 0 || !strings.Contains(strings.Join(rep.Warnings, "\n"), "skipping --prune-stored-versions") {
		t.Fatalf("expected prune-skip warning, got %v", rep.Warnings)
	}

	var statusUpdates int
	for _, a := range dyn.Actions() {
		u, ok := a.(clienttesting.UpdateAction)
		if ok && u.GetSubresource() == "status" {
			statusUpdates++
		}
	}
	if statusUpdates != 0 {
		t.Fatalf("expected no CRD status update after failure, got %d", statusUpdates)
	}

	byName := map[string]string{}
	for _, o := range rep.Objects {
		byName[o.Name] = o.Error
	}
	if byName["b"] == "" || byName["a"] != "" || byName["c"] != "" {
		t.Fatalf("unexpected per-object errors: %v", byName)
	}
}

func TestRunMigrateStorage_PruneStoredVersions(t *testing.T) {
	dyn := newMigrateFake(
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v1", "v2"}, true),
		widget("default", "a", "v2", nil),
	)

	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		CRDName:             "xwidgets.e2e.example.org",
		PruneStoredVersions: true,
		Quiet:               true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.Pruned || rep.PruneError != "" {
		t.Fatalf("expected prune success: %+v", rep)
	}
	if len(rep.StoredVersions) != 1 || rep.StoredVersions[0] != "v2" {
		t.Fatalf("stored versions after prune: %v", rep.StoredVersions)
	}

	var got []string
	for _, a := range dyn.Actions() {
		u, ok := a.(clienttesting.UpdateAction)
		if !ok || u.GetSubresource() != "status" {
			continue
		}
		obj, ok := u.GetObject().(*unstructured.Unstructured)
		if !ok {
			t.Fatalf("status update object: %T", u.GetObject())
		}
		got, _, _ = unstructured.NestedStringSlice(obj.Object, "status", "storedVersions")
	}
	if len(got) != 1 || got[0] != "v2" {
		t.Fatalf("status update storedVersions = %v", got)
	}
}

func TestRunMigrateStorage_NamespaceLimitsList(t *testing.T) {
	dyn := newMigrateFake(
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v2"}, true),
		widget("default", "a", "v2", nil),
		widget("prod", "b", "v2", nil),
	)

	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		CRDName:   "xwidgets.e2e.example.org",
		Namespace: "prod",
		Quiet:     true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Succeeded != 1 || len(rep.Objects) != 1 || rep.Objects[0].Name != "b" {
		t.Fatalf("expected only prod/b, got %+v", rep.Objects)
	}
	patches := applyPatches(t, dyn)
	if len(patches) != 1 || patches[0].GetName() != "b" || patches[0].GetNamespace() != "prod" {
		t.Fatalf("unexpected patches: %#v", patches)
	}
}

func TestRunMigrateStorage_NamespaceIgnoredWhenClusterScoped(t *testing.T) {
	dyn := newMigrateFake(
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v2"}, false),
		widget("", "a", "v2", nil),
	)

	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		CRDName:   "xwidgets.e2e.example.org",
		Namespace: "default",
		Quiet:     true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Succeeded != 1 {
		t.Fatalf("cluster-scoped list should still run, got %+v", rep)
	}
	if len(rep.Warnings) == 0 || !strings.Contains(rep.Warnings[0], "--namespace") {
		t.Fatalf("expected namespace-ignored warning, got %v", rep.Warnings)
	}
}

func TestRunMigrateStorage_PruneRefusedWithNamespace(t *testing.T) {
	dyn := newMigrateFake(
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v1", "v2"}, true),
		widget("default", "a", "v2", nil),
		widget("prod", "b", "v2", nil),
	)

	_, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		CRDName:             "xwidgets.e2e.example.org",
		Namespace:           "prod",
		PruneStoredVersions: true,
		Quiet:               true,
	})
	if err == nil || !strings.Contains(err.Error(), "--prune-stored-versions") || !strings.Contains(err.Error(), "--namespace") {
		t.Fatalf("expected prune+namespace refusal, got %v", err)
	}
	if n := len(applyPatches(t, dyn)); n != 0 {
		t.Fatalf("refused prune must not apply objects, got %d patches", n)
	}
	for _, a := range dyn.Actions() {
		u, ok := a.(clienttesting.UpdateAction)
		if ok && u.GetSubresource() == "status" {
			t.Fatal("refused prune must not update CRD status")
		}
	}
}

func TestRunMigrateStorage_PruneAllowedWhenNamespaceIgnored(t *testing.T) {
	dyn := newMigrateFake(
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v1", "v2"}, false),
		widget("", "a", "v2", nil),
	)

	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		CRDName:             "xwidgets.e2e.example.org",
		Namespace:           "default",
		PruneStoredVersions: true,
		Quiet:               true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.Pruned {
		t.Fatalf("cluster-scoped --namespace should be ignored, prune should proceed: %+v", rep)
	}
}

func TestRunMigrateStorage_MissingXRD(t *testing.T) {
	dyn := newMigrateFake()
	_, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		XRDName: "missing.example.org",
		Quiet:   true,
	})
	if err == nil {
		t.Fatal("expected an error for a missing XRD")
	}
}

func TestRunMigrateStorage_MissingCRD(t *testing.T) {
	dyn := newMigrateFake()
	_, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		CRDName: "missing.example.org",
		Quiet:   true,
	})
	if err == nil {
		t.Fatal("expected an error for a missing CRD")
	}
}

func TestRunMigrateStorage_NeitherOrBoth(t *testing.T) {
	dyn := newMigrateFake()
	if _, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{}); err == nil {
		t.Fatal("expected error when neither --xrd nor --crd is set")
	}
	if _, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		XRDName: "a", CRDName: "b",
	}); err == nil {
		t.Fatal("expected error when both --xrd and --crd are set")
	}
}

func TestRunMigrateStorage_StorageMismatchUsesCRD(t *testing.T) {
	dyn := newMigrateFake(
		migrateXRD("v1", true),
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v1", "v2"}, true),
		widget("default", "a", "v2", nil),
	)
	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		XRDName: "xwidgets.e2e.example.org",
		Quiet:   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.StorageVersion != "v2" {
		t.Fatalf("should use CRD storage version, got %s", rep.StorageVersion)
	}
	if len(rep.Warnings) == 0 || !strings.Contains(rep.Warnings[0], "differs") {
		t.Fatalf("expected mismatch warning, got %v", rep.Warnings)
	}
}

func TestRunMigrateStorage_NoStorageVersion(t *testing.T) {
	crd := migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", nil, true)
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	for i, raw := range versions {
		vm := raw.(map[string]any)
		vm["storage"] = false
		versions[i] = vm
	}
	_ = unstructured.SetNestedSlice(crd.Object, versions, "spec", "versions")

	dyn := newMigrateFake(crd)
	_, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		CRDName: "xwidgets.e2e.example.org",
		Quiet:   true,
	})
	if err == nil || !strings.Contains(err.Error(), "no storage version") {
		t.Fatalf("expected no-storage-version error, got %v", err)
	}
}

func TestRunMigrateStorage_ConcurrencyAppliesEachOnce(t *testing.T) {
	objs := []runtime.Object{
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v2"}, true),
	}
	for _, name := range []string{"a", "b", "c", "d"} {
		objs = append(objs, widget("default", name, "v2", nil))
	}
	dyn := newMigrateFake(objs...)

	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		CRDName:     "xwidgets.e2e.example.org",
		Concurrency: 4,
		Quiet:       true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Succeeded != 4 || len(applyPatches(t, dyn)) != 4 {
		t.Fatalf("expected 4 applies, succeeded=%d patches=%d", rep.Succeeded, len(applyPatches(t, dyn)))
	}
}

func TestRunMigrateStorage_EmptyCluster(t *testing.T) {
	dyn := newMigrateFake(
		migrateCRD("xwidgets.e2e.example.org", "e2e.example.org", "XWidget", "xwidgets", "v2", []string{"v1", "v2"}, true),
	)
	rep, err := RunMigrateStorage(context.Background(), dyn, MigrateStorageOptions{
		CRDName:             "xwidgets.e2e.example.org",
		PruneStoredVersions: true,
		Quiet:               true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Succeeded != 0 || rep.Failed != 0 || !rep.Pruned {
		t.Fatalf("empty cluster should prune: %+v", rep)
	}
}

func TestMigrateStorageReport_WriteTable(t *testing.T) {
	rep := &MigrateStorageReport{
		ResourceKind:   "CRD",
		Resource:       "xwidgets.e2e.example.org",
		CRD:            "xwidgets.e2e.example.org",
		Group:          "e2e.example.org",
		Kind:           "XWidget",
		Plural:         "xwidgets",
		StorageVersion: "v2",
		StoredVersions: []string{"v1", "v2"},
		Namespaced:     true,
		FieldManager:   "convctl",
		Succeeded:      1,
		Failed:         1,
		Objects: []MigrateObjectResult{
			{Namespace: "default", Name: "a"},
			{Namespace: "prod", Name: "b", Error: "boom"},
		},
		Warnings: []string{"heads up"},
	}
	var b strings.Builder
	rep.WriteTable(&b)
	out := b.String()
	for _, want := range []string{"CRD: xwidgets.e2e.example.org", "default", "a", "ok", "prod", "b", "boom", "WARNING: heads up"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table missing %q:\n%s", want, out)
		}
	}
}
