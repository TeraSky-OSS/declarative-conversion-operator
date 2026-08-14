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
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// completionTimeout bounds cluster lookups during shell completion so a
// hung apiserver cannot freeze tab-complete.
const completionTimeout = 2 * time.Second

var namespaceGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

func kubeOptionsFromCmd(cmd *cobra.Command) KubeOptions {
	kubeconfig, _ := cmd.Flags().GetString("kubeconfig")
	kubeContext, _ := cmd.Flags().GetString("context")
	return KubeOptions{Kubeconfig: kubeconfig, Context: kubeContext}
}

func registerYAMLFileCompletions(cmd *cobra.Command, flags ...string) {
	for _, name := range flags {
		if cmd.Flags().Lookup(name) == nil {
			continue
		}
		_ = cmd.MarkFlagFilename(name, "yaml", "yml")
	}
}

func registerOfflineFlagCompletions(cmd *cobra.Command) {
	registerYAMLFileCompletions(cmd, "xrd", "crd", "config", "sample")
	if cmd.Flags().Lookup("samples") != nil {
		_ = cmd.MarkFlagDirname("samples")
	}
	if cmd.Flags().Lookup("output-file") != nil {
		_ = cmd.MarkFlagFilename("output-file")
	}
}

func registerKubeFlagCompletions(cmd *cobra.Command) {
	if cmd.Flags().Lookup("kubeconfig") != nil {
		_ = cmd.MarkFlagFilename("kubeconfig")
	}
	if cmd.Flags().Lookup("context") != nil {
		_ = cmd.RegisterFlagCompletionFunc("context", completeKubeContexts)
	}
}

func registerOutputCompletions(cmd *cobra.Command, values ...string) {
	if cmd.Flags().Lookup("output") == nil {
		return
	}
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions(values, cobra.ShellCompDirectiveNoFileComp))
}

func registerLiveResourceCompletions(cmd *cobra.Command) {
	if cmd.Flags().Lookup("xrd") != nil {
		_ = cmd.RegisterFlagCompletionFunc("xrd", completeLiveXRDs)
	}
	if cmd.Flags().Lookup("crd") != nil {
		_ = cmd.RegisterFlagCompletionFunc("crd", completeLiveCRDs)
	}
	if cmd.Flags().Lookup("namespace") != nil {
		_ = cmd.RegisterFlagCompletionFunc("namespace", completeLiveNamespaces)
	}
}

func completeKubeContexts(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	opts := kubeOptionsFromCmd(cmd)
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		loadingRules.ExplicitPath = opts.Kubeconfig
	}
	cfg, err := loadingRules.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(cfg.Contexts))
	for name := range cfg.Contexts {
		if toComplete != "" && !strings.HasPrefix(name, toComplete) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, cobra.ShellCompDirectiveNoFileComp
}

func completeLiveXRDs(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeLiveNames(cmd, xrdGVR, toComplete)
}

func completeLiveCRDs(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeLiveNames(cmd, crdGVR, toComplete)
}

func completeLiveNamespaces(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeLiveNames(cmd, namespaceGVR, toComplete)
}

func completeLiveNames(cmd *cobra.Command, gvr schema.GroupVersionResource, toComplete string) ([]string, cobra.ShellCompDirective) {
	dyn, err := buildDynamicClient(kubeOptionsFromCmd(cmd))
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, completionTimeout)
	defer cancel()
	names, err := listResourceNames(ctx, dyn, gvr, toComplete)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func listResourceNames(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, toComplete string) ([]string, error) {
	list, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		name := list.Items[i].GetName()
		if toComplete != "" && !strings.HasPrefix(name, toComplete) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
