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
	"testing"
)

func TestCurrentNamespace_PodNamespaceEnvVar(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "custom-ns")
	if got := currentNamespace(); got != "custom-ns" {
		t.Fatalf("expected POD_NAMESPACE to take priority, got %q", got)
	}
}

func TestCurrentNamespace_FallsBackToDefault(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")
	// This test's sandbox has no
	// /var/run/secrets/kubernetes.io/serviceaccount/namespace file, so this
	// also exercises that read failing and falling through.
	if got := currentNamespace(); got != "default" {
		t.Fatalf("expected the \"default\" fallback outside a cluster, got %q", got)
	}
}
