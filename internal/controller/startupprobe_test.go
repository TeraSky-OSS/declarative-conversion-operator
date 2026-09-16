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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func serverWithStartupProbe(sp *teraskyv1alpha1.StartupProbeSpec) *teraskyv1alpha1.ConversionWebhookServer {
	return &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			Namespace:    "operator-ns",
			Certificate:  teraskyv1alpha1.CertificateSpec{IssuerRef: teraskyv1alpha1.CertificateIssuerRef{Name: "ca-issuer"}},
			StartupProbe: sp,
		},
	}
}

// An instance created before spec.startupProbe existed must still get one,
// which is why the controller defaults rather than relying on the CRD
// markers alone — the markers only default a field that is present.
func TestStartupProbe_DefaultsWithoutASpec(t *testing.T) {
	dep := reconcileToDeployment(t, serverWithStartupProbe(nil))
	c := dep.Spec.Template.Spec.Containers[0]

	if c.StartupProbe == nil {
		t.Fatal("no startupProbe: the liveness probe's 30s then bounds the whole cold start, and a replica slower than that never finishes one")
	}
	if c.StartupProbe.PeriodSeconds != 5 || c.StartupProbe.FailureThreshold != 60 {
		t.Errorf("startupProbe period/threshold = %d/%d, want 5/60 (a five-minute budget)",
			c.StartupProbe.PeriodSeconds, c.StartupProbe.FailureThreshold)
	}
	// /readyz, not /healthz. The plain endpoint carrying /healthz comes up
	// before the registry sync, so a startupProbe pointed at it succeeds
	// within milliseconds and bounds nothing — the budget every doc
	// describes would not exist. /readyz is false until InitialSync
	// completes, which is what makes period x failureThreshold a real
	// deadline on the sync and gets a wedged replica restarted instead of
	// left not-ready forever.
	if c.StartupProbe.HTTPGet == nil || c.StartupProbe.HTTPGet.Path != "/readyz" {
		t.Fatalf("startupProbe must poll /readyz, got %+v", c.StartupProbe.HTTPGet)
	}
	if c.LivenessProbe == nil || c.LivenessProbe.HTTPGet == nil || c.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Fatalf("the liveness probe must stay on /healthz, got %+v", c.LivenessProbe)
	}
}

func TestStartupProbe_HonoursAnExplicitBudget(t *testing.T) {
	period, threshold := int32(10), int32(90)
	dep := reconcileToDeployment(t, serverWithStartupProbe(&teraskyv1alpha1.StartupProbeSpec{
		PeriodSeconds: &period, FailureThreshold: &threshold,
	}))
	c := dep.Spec.Template.Spec.Containers[0]
	if c.StartupProbe == nil || c.StartupProbe.PeriodSeconds != 10 || c.StartupProbe.FailureThreshold != 90 {
		t.Fatalf("startupProbe = %+v, want period 10 / threshold 90", c.StartupProbe)
	}
}

func TestStartupProbe_CanBeTurnedOff(t *testing.T) {
	off := false
	dep := reconcileToDeployment(t, serverWithStartupProbe(&teraskyv1alpha1.StartupProbeSpec{Enabled: &off}))
	c := dep.Spec.Template.Spec.Containers[0]
	if c.StartupProbe != nil {
		t.Fatalf("startupProbe = %+v, want none when explicitly disabled", c.StartupProbe)
	}
	// Disabling the startupProbe must not take the other two with it.
	if c.ReadinessProbe == nil || c.LivenessProbe == nil {
		t.Fatal("readiness and liveness probes must survive disabling the startupProbe")
	}
}
