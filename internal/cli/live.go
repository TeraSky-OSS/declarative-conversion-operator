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
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// KubeOptions configures how `test --live` reaches a cluster. It follows
// the same kubeconfig/context resolution every standard Kubernetes CLI
// uses: an explicit path (falling back to the KUBECONFIG env var and
// finally ~/.kube/config) and an explicit context name (falling back to
// the kubeconfig's current-context) — never a bespoke scheme.
type KubeOptions struct {
	Kubeconfig string
	Context    string
}

// buildDynamicClient resolves a *rest.Config the same way kubectl does
// and returns a dynamic client for it.
func buildDynamicClient(opts KubeOptions) (dynamic.Interface, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		loadingRules.ExplicitPath = opts.Kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if opts.Context != "" {
		overrides.CurrentContext = opts.Context
	}
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("resolving kubeconfig: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("building Kubernetes client: %w", err)
	}
	return dyn, nil
}

// xrdResourceInfo pulls the group and plural resource name a live XRD
// generates straight off the XRD object itself — the two fields needed to
// address the generated CRD by GroupVersionResource, which pkg/xrdadapter
// itself never needs since it only ever reads spec.versions.
func xrdResourceInfo(xrd *unstructured.Unstructured) (group, plural string, err error) {
	group, found, err := unstructured.NestedString(xrd.Object, "spec", "group")
	if err != nil || !found || group == "" {
		return "", "", fmt.Errorf("xrd is missing spec.group")
	}
	plural, found, err = unstructured.NestedString(xrd.Object, "spec", "names", "plural")
	if err != nil || !found || plural == "" {
		return "", "", fmt.Errorf("xrd is missing spec.names.plural")
	}
	return group, plural, nil
}

// FetchLiveSamples lists every existing instance of the XRD's generated
// composite resource type at hubVersion — the storage/referenceable
// version, which the apiserver always serves directly from etcd with no
// conversion webhook involved. That's deliberate: a pre-upgrade check has
// to work at the exact moment spec.conversion is still None, or still
// pointing at whatever was configured before, so it can never depend on
// the very conversion path it's about to validate.
//
// Results are paginated through in full rather than capped, since a
// pre-upgrade check that silently missed some fraction of live objects
// would report false confidence — worse than not running it at all.
func FetchLiveSamples(ctx context.Context, dyn dynamic.Interface, xrd *unstructured.Unstructured, hubVersion string) ([]Sample, error) {
	group, plural, err := xrdResourceInfo(xrd)
	if err != nil {
		return nil, err
	}
	gvr := schema.GroupVersionResource{Group: group, Version: hubVersion, Resource: plural}

	var samples []Sample
	continueToken := ""
	for {
		list, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{Continue: continueToken, Limit: 200})
		if err != nil {
			return nil, fmt.Errorf("listing %s (version %s): %w", gvr.GroupResource().String(), hubVersion, err)
		}
		for i := range list.Items {
			item := list.Items[i]
			samples = append(samples, Sample{
				File:    "cluster:" + objectLabel(&item),
				Object:  item.Object,
				Version: versionFromAPIVersion(item.GetAPIVersion()),
			})
		}
		continueToken = list.GetContinue()
		if continueToken == "" {
			break
		}
	}
	return samples, nil
}

func objectLabel(obj *unstructured.Unstructured) string {
	if ns := obj.GetNamespace(); ns != "" {
		return ns + "/" + obj.GetName()
	}
	return obj.GetName()
}

func versionFromAPIVersion(apiVersion string) string {
	if idx := strings.LastIndex(apiVersion, "/"); idx >= 0 {
		return apiVersion[idx+1:]
	}
	return apiVersion
}
