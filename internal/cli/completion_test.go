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
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestListResourceNames(t *testing.T) {
	xrdA := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.crossplane.io/v2",
		"kind":       "CompositeResourceDefinition",
		"metadata":   map[string]any{"name": "xwidgets.example.org"},
	}}
	xrdB := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.crossplane.io/v2",
		"kind":       "CompositeResourceDefinition",
		"metadata":   map[string]any{"name": "xfoos.example.org"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{xrdGVR: "CompositeResourceDefinitionList"},
		xrdA, xrdB)

	got, err := listResourceNames(context.Background(), dyn, xrdGVR, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"xfoos.example.org", "xwidgets.example.org"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}

	got, err = listResourceNames(context.Background(), dyn, xrdGVR, "xwid")
	if err != nil {
		t.Fatalf("list prefix: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"xwidgets.example.org"}) {
		t.Fatalf("prefix filter: got %v", got)
	}
}

func TestCompleteKubeContexts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeconfig")
	kubeconfig := []byte(`apiVersion: v1
kind: Config
clusters:
- name: kind
  cluster: {server: https://127.0.0.1}
- name: prod
  cluster: {server: https://example.com}
contexts:
- name: kind-dev
  context: {cluster: kind, user: kind}
- name: prod
  context: {cluster: prod, user: prod}
users:
- name: kind
  user: {}
- name: prod
  user: {}
`)
	if err := os.WriteFile(path, kubeconfig, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{Use: "migrate-storage"}
	cmd.Flags().String("kubeconfig", "", "")
	cmd.Flags().String("context", "", "")
	if err := cmd.Flags().Set("kubeconfig", path); err != nil {
		t.Fatal(err)
	}

	got, dirct := completeKubeContexts(cmd, nil, "")
	if dirct != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("directive %v", dirct)
	}
	want := []string{"kind-dev", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}

	got, _ = completeKubeContexts(cmd, nil, "kind")
	if !reflect.DeepEqual(got, []string{"kind-dev"}) {
		t.Fatalf("prefix: got %v", got)
	}
}

func TestMigrateStorageRegistersLiveCompletions(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	cmd := newMigrateStorageCmd()
	for _, flag := range []string{"xrd", "crd", "context", "namespace"} {
		fn, ok := cmd.GetFlagCompletionFunc(flag)
		if !ok {
			t.Fatalf("missing completion for --%s", flag)
		}
		// An unreachable cluster must not crash tab-complete or fall back
		// to filename completion.
		_, dirct := fn(cmd, nil, "")
		if dirct != cobra.ShellCompDirectiveNoFileComp {
			t.Fatalf("--%s directive %v, want NoFileComp", flag, dirct)
		}
	}
}

func TestKubeCommandsRegisterContextCompletion(t *testing.T) {
	for _, cmd := range []*cobra.Command{newTestCmd(), newDiffCmd(), newMigrateStorageCmd()} {
		if _, ok := cmd.GetFlagCompletionFunc("context"); !ok {
			t.Errorf("%s: missing --context completion", cmd.Name())
		}
	}
}
