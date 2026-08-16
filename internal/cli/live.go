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

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
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

func xrdGroupKind(xrd *unstructured.Unstructured) (group, kind string, err error) {
	group, found, err := unstructured.NestedString(xrd.Object, "spec", "group")
	if err != nil || !found || group == "" {
		return "", "", fmt.Errorf("xrd is missing spec.group")
	}
	kind, found, err = unstructured.NestedString(xrd.Object, "spec", "names", "kind")
	if err != nil || !found || kind == "" {
		return "", "", fmt.Errorf("xrd is missing spec.names.kind")
	}
	return group, kind, nil
}

// FetchLiveSamples lists every existing instance of the XRD's generated
// composite resource type at hubVersion. See fetchLiveSamplesByGVR for why
// hubVersion specifically, and why pagination isn't capped.
func FetchLiveSamples(ctx context.Context, dyn dynamic.Interface, xrd *unstructured.Unstructured, hubVersion string) ([]Sample, error) {
	group, plural, err := xrdResourceInfo(xrd)
	if err != nil {
		return nil, err
	}
	return fetchLiveSamplesByGVR(ctx, dyn, schema.GroupVersionResource{Group: group, Version: hubVersion, Resource: plural}, hubVersion)
}

// FetchLiveSamplesCRD is FetchLiveSamples's sibling for a native
// CustomResourceDefinition. Unlike xrdResourceInfo, no unstructured
// digging is needed: a typed CRD already exposes spec.group and
// spec.names.plural directly.
func FetchLiveSamplesCRD(ctx context.Context, dyn dynamic.Interface, crd *extv1.CustomResourceDefinition, hubVersion string) ([]Sample, error) {
	if crd.Spec.Group == "" {
		return nil, fmt.Errorf("crd is missing spec.group")
	}
	if crd.Spec.Names.Plural == "" {
		return nil, fmt.Errorf("crd is missing spec.names.plural")
	}
	gvr := schema.GroupVersionResource{Group: crd.Spec.Group, Version: hubVersion, Resource: crd.Spec.Names.Plural}
	return fetchLiveSamplesByGVR(ctx, dyn, gvr, hubVersion)
}

// fetchLiveSamplesByGVR is FetchLiveSamples/FetchLiveSamplesCRD's shared
// pagination loop, listing every existing instance of gvr at hubVersion —
// the storage/referenceable version, which the apiserver always serves
// directly from etcd with no conversion webhook involved. That's
// deliberate: a pre-upgrade check has to work at the exact moment
// spec.conversion is still None, or still pointing at whatever was
// configured before, so it can never depend on the very conversion path
// it's about to validate.
//
// Results are paginated through in full rather than capped, since a
// pre-upgrade check that silently missed some fraction of live objects
// would report false confidence — worse than not running it at all.
func fetchLiveSamplesByGVR(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, hubVersion string) ([]Sample, error) {
	items, err := listAllByGVR(ctx, dyn, gvr, "")
	if err != nil {
		return nil, fmt.Errorf("listing %s (version %s): %w", gvr.GroupResource().String(), hubVersion, err)
	}
	samples := make([]Sample, 0, len(items))
	for i := range items {
		item := items[i]
		samples = append(samples, Sample{
			File:    "cluster:" + objectLabel(&item),
			Object:  item.Object,
			Version: versionFromAPIVersion(item.GetAPIVersion()),
		})
	}
	return samples, nil
}

// listAllByGVR paginates through every instance of gvr. An empty namespace
// lists cluster-wide (all namespaces for a namespaced type). A non-empty
// namespace lists only that namespace.
func listAllByGVR(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace string) ([]unstructured.Unstructured, error) {
	var items []unstructured.Unstructured
	continueToken := ""
	for {
		opts := metav1.ListOptions{Continue: continueToken, Limit: 200}
		var (
			list *unstructured.UnstructuredList
			err  error
		)
		if namespace != "" {
			list, err = dyn.Resource(gvr).Namespace(namespace).List(ctx, opts)
		} else {
			list, err = dyn.Resource(gvr).List(ctx, opts)
		}
		if err != nil {
			return nil, err
		}
		items = append(items, list.Items...)
		continueToken = list.GetContinue()
		if continueToken == "" {
			break
		}
	}
	return items, nil
}

// The GroupVersionResources `convctl diff --live` addresses. Both target
// schemas are cluster-scoped, and so are XRDConversionConfig and
// CRDConversionConfig, so every lookup below is by name alone.
var (
	xrdGVR = xrdadapter.GroupVersionKind.GroupVersion().WithResource("compositeresourcedefinitions")
	crdGVR = schema.GroupVersionResource{Group: extv1.GroupName, Version: "v1", Resource: "customresourcedefinitions"}

	xrdConversionConfigGVR = teraskyv1alpha1.GroupVersion.WithResource("xrdconversionconfigs")
	crdConversionConfigGVR = teraskyv1alpha1.GroupVersion.WithResource("crdconversionconfigs")
)

// FetchLiveXRD reads the target XRD straight from the cluster, so a diff
// against live state uses the schema the cluster actually has rather than
// whatever local copy happens to be lying around.
func FetchLiveXRD(ctx context.Context, dyn dynamic.Interface, name string) (*unstructured.Unstructured, error) {
	obj, err := dyn.Resource(xrdGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting XRD %q: %w", name, err)
	}
	return obj, nil
}

// FetchLiveCRD is FetchLiveXRD's sibling for a native CRD, decoded into the
// typed apiextensions.k8s.io/v1 shape pkg/crdadapter consumes.
func FetchLiveCRD(ctx context.Context, dyn dynamic.Interface, name string) (*extv1.CustomResourceDefinition, error) {
	obj, err := dyn.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting CRD %q: %w", name, err)
	}
	var crd extv1.CustomResourceDefinition
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &crd); err != nil {
		return nil, fmt.Errorf("decoding CRD %q: %w", name, err)
	}
	return &crd, nil
}

// FetchLiveXRDConversionConfig reads the XRDConversionConfig currently
// applied under name. A missing config is not an error — "nothing is
// configured yet" is exactly the state a diff is most often run against —
// so it returns (nil, nil) and lets the caller decide what to compare with.
func FetchLiveXRDConversionConfig(ctx context.Context, dyn dynamic.Interface, name string) (*teraskyv1alpha1.XRDConversionConfig, error) {
	obj, err := dyn.Resource(xrdConversionConfigGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting XRDConversionConfig %q: %w", name, err)
	}
	var cfg teraskyv1alpha1.XRDConversionConfig
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &cfg); err != nil {
		return nil, fmt.Errorf("decoding XRDConversionConfig %q: %w", name, err)
	}
	return &cfg, nil
}

// FetchLiveCRDConversionConfig is FetchLiveXRDConversionConfig's sibling
// for CRDConversionConfig, with the same "missing is not an error" contract.
func FetchLiveCRDConversionConfig(ctx context.Context, dyn dynamic.Interface, name string) (*teraskyv1alpha1.CRDConversionConfig, error) {
	obj, err := dyn.Resource(crdConversionConfigGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting CRDConversionConfig %q: %w", name, err)
	}
	var cfg teraskyv1alpha1.CRDConversionConfig
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &cfg); err != nil {
		return nil, fmt.Errorf("decoding CRDConversionConfig %q: %w", name, err)
	}
	return &cfg, nil
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
