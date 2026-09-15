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

// The rest of main() is manager/webhook wiring that needs a real (or
// envtest) cluster to exercise meaningfully; that's covered by the e2e
// suite (hack/e2e-test*.sh) instead. currentNamespace() is the one piece
// of pure, unit-testable logic in this binary.
package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

func TestCurrentNamespace_PodNamespaceEnvVar(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "custom-ns")
	if got := currentNamespace(); got != "custom-ns" {
		t.Fatalf("expected POD_NAMESPACE to take priority, got %q", got)
	}
}

func TestCurrentNamespace_ReadsServiceAccountFile(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "namespace")
	if err := os.WriteFile(path, []byte("from-file-ns"), 0o600); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}
	old := serviceAccountNamespaceFile
	serviceAccountNamespaceFile = path
	defer func() { serviceAccountNamespaceFile = old }()

	if got := currentNamespace(); got != "from-file-ns" {
		t.Fatalf("expected the namespace read from the service account file, got %q", got)
	}
}

func TestCurrentNamespace_FallsBackToDefault(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")
	// Point at a path that's guaranteed not to exist, rather than relying
	// on this test's sandbox not being a real Kubernetes pod (where the
	// real service-account file would exist and this would otherwise
	// return that namespace instead of "default").
	old := serviceAccountNamespaceFile
	serviceAccountNamespaceFile = filepath.Join(t.TempDir(), "does-not-exist")
	defer func() { serviceAccountNamespaceFile = old }()

	if got := currentNamespace(); got != "default" {
		t.Fatalf("expected the \"default\" fallback outside a cluster, got %q", got)
	}
}

// fakeDiscovery stubs just the one discovery call the startup check makes.
type fakeDiscovery struct {
	discovery.DiscoveryInterface
	resources *metav1.APIResourceList
	err       error
}

func (f *fakeDiscovery) ServerResourcesForGroupVersion(string) (*metav1.APIResourceList, error) {
	return f.resources, f.err
}

func withFakeDiscovery(t *testing.T, f discovery.DiscoveryInterface, err error) {
	t.Helper()
	old := newDiscoveryClient
	newDiscoveryClient = func(*rest.Config) (discovery.DiscoveryInterface, error) { return f, err }
	t.Cleanup(func() { newDiscoveryClient = old })
}

func TestCheckCrossplaneV2Served_PresentWhenKindIsListed(t *testing.T) {
	withFakeDiscovery(t, &fakeDiscovery{resources: &metav1.APIResourceList{
		GroupVersion: "apiextensions.crossplane.io/v2",
		APIResources: []metav1.APIResource{
			{Name: "compositionrevisions", Kind: "CompositionRevision"},
			{Name: "compositeresourcedefinitions", Kind: "CompositeResourceDefinition"},
		},
	}}, nil)

	if err := checkCrossplaneV2Served(&rest.Config{}); err != nil {
		t.Fatalf("expected the v2 XRD API to be considered served, got %v", err)
	}
}

func TestCheckCrossplaneV2Served_GroupAbsent(t *testing.T) {
	// A cluster with no Crossplane (or Crossplane 1.x) reports the group
	// version as missing rather than returning an empty resource list.
	withFakeDiscovery(t, &fakeDiscovery{err: apierrors.NewNotFound(
		schema.GroupResource{Group: "apiextensions.crossplane.io", Resource: "v2"}, "")}, nil)

	if err := checkCrossplaneV2Served(&rest.Config{}); !errors.Is(err, errCrossplaneV2NotServed) {
		t.Fatalf("expected errCrossplaneV2NotServed, got %v", err)
	}
}

func TestCheckCrossplaneV2Served_GroupServedWithoutTheKind(t *testing.T) {
	// Defensive: a group version that exists but does not carry the XRD
	// kind is not a usable Crossplane 2.x for this operator's purposes.
	withFakeDiscovery(t, &fakeDiscovery{resources: &metav1.APIResourceList{
		GroupVersion: "apiextensions.crossplane.io/v2",
		APIResources: []metav1.APIResource{{Name: "compositions", Kind: "Composition"}},
	}}, nil)

	if err := checkCrossplaneV2Served(&rest.Config{}); !errors.Is(err, errCrossplaneV2NotServed) {
		t.Fatalf("expected errCrossplaneV2NotServed, got %v", err)
	}
}

func TestCheckCrossplaneV2Served_TransientErrorIsNotReportedAsMissing(t *testing.T) {
	// An unreachable apiserver must not be reported to the user as "install
	// Crossplane" — that would send them chasing the wrong problem.
	boom := apierrors.NewServiceUnavailable("apiserver is having a moment")
	withFakeDiscovery(t, &fakeDiscovery{err: boom}, nil)

	err := checkCrossplaneV2Served(&rest.Config{})
	if err == nil || errors.Is(err, errCrossplaneV2NotServed) {
		t.Fatalf("expected the transient error to propagate distinctly, got %v", err)
	}
}
