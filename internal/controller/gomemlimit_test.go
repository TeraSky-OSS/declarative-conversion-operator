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

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func serverWithResources(limits corev1.ResourceList, extraEnv ...corev1.EnvVar) *teraskyv1alpha1.ConversionWebhookServer {
	return &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			Namespace:   "operator-ns",
			Certificate: teraskyv1alpha1.CertificateSpec{IssuerRef: teraskyv1alpha1.CertificateIssuerRef{Name: "ca-issuer"}},
			Resources:   corev1.ResourceRequirements{Limits: limits},
			ExtraEnv:    extraEnv,
		},
	}
}

func envValue(env []corev1.EnvVar, name string) (string, bool) {
	for _, e := range env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// The chart's default limit is 256Mi. Without GOMEMLIMIT the Go heap will
// happily grow past it during a cold start — measured at ~142 MiB of
// transient heap for a thousand targets — and the kernel, not the GC, is
// what notices.
func TestGOMEMLIMIT_DerivedFromTheMemoryLimit(t *testing.T) {
	dep := reconcileToDeployment(t, serverWithResources(corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse("256Mi"),
	}))
	got, ok := envValue(dep.Spec.Template.Spec.Containers[0].Env, "GOMEMLIMIT")
	if !ok {
		t.Fatal("no GOMEMLIMIT: the GC then has no idea the container has a limit at all")
	}
	// 256Mi = 268435456; 90% of it, computed without overflowing.
	if want := "241591860"; got != want {
		t.Errorf("GOMEMLIMIT = %s, want %s (90%% of 256Mi)", got, want)
	}
}

func TestGOMEMLIMIT_AbsentWithoutAMemoryLimit(t *testing.T) {
	dep := reconcileToDeployment(t, serverWithResources(nil))
	if got, ok := envValue(dep.Spec.Template.Spec.Containers[0].Env, "GOMEMLIMIT"); ok {
		t.Fatalf("GOMEMLIMIT = %s, want none: there is no limit to derive it from", got)
	}
}

// An operator who sets it deliberately has a reason; deriving a second
// value would leave two entries with the same name in the container spec.
func TestGOMEMLIMIT_ExplicitValueWins(t *testing.T) {
	dep := reconcileToDeployment(t, serverWithResources(
		corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
		corev1.EnvVar{Name: "GOMEMLIMIT", Value: "100MiB"},
	))
	env := dep.Spec.Template.Spec.Containers[0].Env
	count := 0
	for _, e := range env {
		if e.Name == "GOMEMLIMIT" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("GOMEMLIMIT appears %d times in %+v, want exactly once", count, env)
	}
	if got, _ := envValue(env, "GOMEMLIMIT"); got != "100MiB" {
		t.Errorf("GOMEMLIMIT = %s, want the operator's own 100MiB", got)
	}
}
