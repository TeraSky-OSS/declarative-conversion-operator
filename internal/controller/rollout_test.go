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
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func serverWithRollout(rollout *teraskyv1alpha1.RolloutSpec) *teraskyv1alpha1.ConversionWebhookServer {
	return &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			Namespace:   "operator-ns",
			Certificate: teraskyv1alpha1.CertificateSpec{IssuerRef: teraskyv1alpha1.CertificateIssuerRef{Name: "ca-issuer"}},
			Rollout:     rollout,
		},
	}
}

func reconcileToDeployment(t *testing.T, server *teraskyv1alpha1.ConversionWebhookServer) appsv1.Deployment {
	t.Helper()
	c := newFakeClient(server).Build()
	r := &ConversionWebhookServerReconciler{
		Client: c, Scheme: newScheme(),
		DefaultNamespace: "operator-ns", DefaultImage: "test/image:v1",
		EnableXRDSupport: true, EnableCRDSupport: true,
	}
	if _, err := reconcileCWS(t, r, server.Name); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var dep appsv1.Deployment
	if err := r.Get(context.Background(), types.NamespacedName{Name: server.Name + "-webhook-server", Namespace: "operator-ns"}, &dep); err != nil {
		t.Fatalf("expected a Deployment: %v", err)
	}
	return dep
}

// An instance that says nothing about rollout must still get the safe
// behaviour — including one created before spec.rollout existed, which is
// why the controller defaults rather than relying only on the CRD markers.
func TestRollout_DefaultsWithoutASpec(t *testing.T) {
	dep := reconcileToDeployment(t, serverWithRollout(nil))
	pod := dep.Spec.Template.Spec

	if pod.TerminationGracePeriodSeconds == nil || *pod.TerminationGracePeriodSeconds != 45 {
		t.Errorf("terminationGracePeriodSeconds = %v, want 45", pod.TerminationGracePeriodSeconds)
	}
	if len(pod.Containers) != 1 {
		t.Fatalf("expected one container, got %d", len(pod.Containers))
	}
	lc := pod.Containers[0].Lifecycle
	if lc == nil || lc.PreStop == nil || lc.PreStop.Sleep == nil {
		t.Fatalf("no preStop sleep hook: %+v — every rolling update then races Endpoints removal", lc)
	}
	if lc.PreStop.Sleep.Seconds != 5 {
		t.Errorf("preStop sleep = %ds, want 5", lc.PreStop.Sleep.Seconds)
	}
	// Exec would need a shell in the image, and the image is distroless.
	if lc.PreStop.Exec != nil {
		t.Error("preStop must not be an Exec: there is no shell in the distroless image")
	}

	if dep.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType || dep.Spec.Strategy.RollingUpdate == nil {
		t.Fatalf("strategy = %+v", dep.Spec.Strategy)
	}
	if got := dep.Spec.Strategy.RollingUpdate.MaxUnavailable; got == nil || got.IntValue() != 0 {
		t.Errorf("maxUnavailable = %v, want 0", got)
	}
	if got := dep.Spec.Strategy.RollingUpdate.MaxSurge; got == nil || got.IntValue() != 1 {
		t.Errorf("maxSurge = %v, want 1 — with maxUnavailable 0 a rollout cannot progress otherwise", got)
	}

	if len(pod.TopologySpreadConstraints) != 1 {
		t.Fatalf("expected one default spread constraint, got %+v", pod.TopologySpreadConstraints)
	}
	tsc := pod.TopologySpreadConstraints[0]
	if tsc.TopologyKey != corev1.LabelHostname {
		t.Errorf("topologyKey = %q", tsc.TopologyKey)
	}
	// Hard spreading would make a single-node cluster unschedulable — an
	// outage caused by the anti-outage setting.
	if tsc.WhenUnsatisfiable != corev1.ScheduleAnyway {
		t.Errorf("whenUnsatisfiable = %q, want ScheduleAnyway", tsc.WhenUnsatisfiable)
	}
	if tsc.LabelSelector == nil || tsc.LabelSelector.MatchLabels["app.kubernetes.io/instance"] != "srv" {
		t.Errorf("spread constraint does not select this instance's pods: %+v", tsc.LabelSelector)
	}
}

func TestRollout_ExplicitValuesWin(t *testing.T) {
	preStop := int32(12)
	grace := int64(90)
	mu := intstr.FromString("25%")
	ms := intstr.FromInt32(3)
	dep := reconcileToDeployment(t, serverWithRollout(&teraskyv1alpha1.RolloutSpec{
		PreStopSleepSeconds:           &preStop,
		TerminationGracePeriodSeconds: &grace,
		MaxUnavailable:                &mu,
		MaxSurge:                      &ms,
	}))
	pod := dep.Spec.Template.Spec
	if *pod.TerminationGracePeriodSeconds != 90 {
		t.Errorf("grace = %d", *pod.TerminationGracePeriodSeconds)
	}
	if pod.Containers[0].Lifecycle.PreStop.Sleep.Seconds != 12 {
		t.Errorf("preStop = %d", pod.Containers[0].Lifecycle.PreStop.Sleep.Seconds)
	}
	if got := dep.Spec.Strategy.RollingUpdate.MaxUnavailable.String(); got != "25%" {
		t.Errorf("maxUnavailable = %q", got)
	}
	if got := dep.Spec.Strategy.RollingUpdate.MaxSurge.IntValue(); got != 3 {
		t.Errorf("maxSurge = %d", got)
	}
}

func TestRollout_PreStopCanBeDisabled(t *testing.T) {
	zero := int32(0)
	dep := reconcileToDeployment(t, serverWithRollout(&teraskyv1alpha1.RolloutSpec{PreStopSleepSeconds: &zero}))
	if lc := dep.Spec.Template.Spec.Containers[0].Lifecycle; lc != nil && lc.PreStop != nil {
		t.Errorf("preStopSleepSeconds=0 must remove the hook, got %+v", lc.PreStop)
	}
}

// Appending our default to the operator's own constraints would add one
// they did not ask for and cannot remove.
func TestRollout_UserSpreadConstraintsSuppressTheDefault(t *testing.T) {
	server := serverWithRollout(nil)
	server.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
		MaxSkew:           2,
		TopologyKey:       "topology.kubernetes.io/zone",
		WhenUnsatisfiable: corev1.DoNotSchedule,
	}}
	dep := reconcileToDeployment(t, server)
	tscs := dep.Spec.Template.Spec.TopologySpreadConstraints
	if len(tscs) != 1 {
		t.Fatalf("expected only the operator's own constraint, got %+v", tscs)
	}
	if tscs[0].TopologyKey != "topology.kubernetes.io/zone" {
		t.Errorf("topologyKey = %q", tscs[0].TopologyKey)
	}
}

func TestRollout_DefaultSpreadCanBeTurnedOff(t *testing.T) {
	off := false
	dep := reconcileToDeployment(t, serverWithRollout(&teraskyv1alpha1.RolloutSpec{DefaultTopologySpread: &off}))
	if n := len(dep.Spec.Template.Spec.TopologySpreadConstraints); n != 0 {
		t.Errorf("expected no spread constraints, got %d", n)
	}
}
