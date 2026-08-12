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
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func reconcileCWS(t *testing.T, r *ConversionWebhookServerReconciler, name string) (reconcile.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
}

func getCWS(t *testing.T, r *ConversionWebhookServerReconciler, name string) *teraskyv1alpha1.ConversionWebhookServer {
	t.Helper()
	var s teraskyv1alpha1.ConversionWebhookServer
	if err := r.Get(context.Background(), types.NamespacedName{Name: name}, &s); err != nil {
		t.Fatalf("getting ConversionWebhookServer %q: %v", name, err)
	}
	return &s
}

func TestCWSReconcile_ExtraArgs_AppendedToDeployment(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			Namespace: "operator-ns",
			Certificate: teraskyv1alpha1.CertificateSpec{
				IssuerRef: teraskyv1alpha1.CertificateIssuerRef{Name: "ca-issuer"},
			},
			ExtraArgs: []string{"--cert-reload-interval=1m", "--zap-devel=true"},
		},
	}
	c := newFakeClient(server).Build()
	r := &ConversionWebhookServerReconciler{
		Client:           c,
		Scheme:           newScheme(),
		DefaultNamespace: "operator-ns",
		DefaultImage:     "test/image:v1",
		EnableXRDSupport: true,
		EnableCRDSupport: false,
	}

	if _, err := reconcileCWS(t, r, "srv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var dep appsv1.Deployment
	if err := r.Get(context.Background(), types.NamespacedName{Name: "srv-webhook-server", Namespace: "operator-ns"}, &dep); err != nil {
		t.Fatalf("expected a Deployment to be created: %v", err)
	}
	if len(dep.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(dep.Spec.Template.Spec.Containers))
	}
	got := dep.Spec.Template.Spec.Containers[0].Args
	wantPrefix := []string{
		"--webhook-server-name=srv",
		"--tls-cert-dir=/tls",
		"--conversion-bind-address=:9443",
		"--metrics-bind-address=:8443",
		"--enable-xrd-support=true",
		"--enable-crd-support=false",
	}
	want := append(append([]string{}, wantPrefix...), "--cert-reload-interval=1m", "--zap-devel=true")
	if len(got) != len(want) {
		t.Fatalf("args length: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args[%d]=%q, want %q (full got=%v)", i, got[i], want[i], got)
		}
	}
}

func TestCWSReconcile_ExtraArgs_RejectsManagedFlag(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			Namespace: "operator-ns",
			Certificate: teraskyv1alpha1.CertificateSpec{
				IssuerRef: teraskyv1alpha1.CertificateIssuerRef{Name: "ca-issuer"},
			},
			ExtraArgs: []string{"--tls-cert-dir", "/evil"},
		},
	}
	c := newFakeClient(server).Build()
	r := &ConversionWebhookServerReconciler{
		Client:           c,
		Scheme:           newScheme(),
		DefaultNamespace: "operator-ns",
		DefaultImage:     "test/image:v1",
	}

	if _, err := reconcileCWS(t, r, "srv"); err == nil {
		t.Fatal("expected reconcile to fail when ExtraArgs override a managed flag")
	}
}

func TestCWSReconcile_SecurityContext_PSSRestricted(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			Namespace: "operator-ns",
			Certificate: teraskyv1alpha1.CertificateSpec{
				IssuerRef: teraskyv1alpha1.CertificateIssuerRef{Name: "ca-issuer"},
			},
		},
	}
	c := newFakeClient(server).Build()
	r := &ConversionWebhookServerReconciler{Client: c, Scheme: newScheme(), DefaultNamespace: "operator-ns", DefaultImage: "test/image:v1"}

	if _, err := reconcileCWS(t, r, "srv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var dep appsv1.Deployment
	if err := r.Get(context.Background(), types.NamespacedName{Name: "srv-webhook-server", Namespace: "operator-ns"}, &dep); err != nil {
		t.Fatalf("expected a Deployment: %v", err)
	}
	podSC := dep.Spec.Template.Spec.SecurityContext
	if podSC == nil || podSC.RunAsNonRoot == nil || !*podSC.RunAsNonRoot {
		t.Fatalf("expected pod runAsNonRoot=true, got %#v", podSC)
	}
	if podSC.RunAsUser == nil || *podSC.RunAsUser != 65532 {
		t.Fatalf("expected pod runAsUser=65532, got %#v", podSC.RunAsUser)
	}
	if podSC.SeccompProfile == nil || podSC.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("expected pod seccomp RuntimeDefault, got %#v", podSC.SeccompProfile)
	}

	if len(dep.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(dep.Spec.Template.Spec.Containers))
	}
	ctr := dep.Spec.Template.Spec.Containers[0]
	ctrSC := ctr.SecurityContext
	if ctrSC == nil {
		t.Fatal("expected container securityContext")
	}
	if ctrSC.AllowPrivilegeEscalation == nil || *ctrSC.AllowPrivilegeEscalation {
		t.Fatalf("expected allowPrivilegeEscalation=false")
	}
	if ctrSC.ReadOnlyRootFilesystem == nil || !*ctrSC.ReadOnlyRootFilesystem {
		t.Fatalf("expected readOnlyRootFilesystem=true")
	}
	if ctrSC.Capabilities == nil || len(ctrSC.Capabilities.Drop) != 1 || ctrSC.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("expected capabilities.drop=[ALL], got %#v", ctrSC.Capabilities)
	}

	hasTmp := false
	for _, m := range ctr.VolumeMounts {
		if m.Name == "tmp" && m.MountPath == "/tmp" {
			hasTmp = true
		}
	}
	if !hasTmp {
		t.Fatal("expected /tmp emptyDir volumeMount for read-only root FS")
	}
	hasTmpVol := false
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Name == "tmp" && v.EmptyDir != nil {
			hasTmpVol = true
		}
	}
	if !hasTmpVol {
		t.Fatal("expected tmp emptyDir volume")
	}
}

func TestCWSReconcile_HappyPath_CreatesChildResources(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			Namespace: "operator-ns",
			Certificate: teraskyv1alpha1.CertificateSpec{
				IssuerRef: teraskyv1alpha1.CertificateIssuerRef{Name: "ca-issuer"},
			},
		},
	}
	c := newFakeClient(server).Build()
	r := &ConversionWebhookServerReconciler{Client: c, Scheme: newScheme(), DefaultNamespace: "operator-ns", DefaultImage: "test/image:v1"}

	if _, err := reconcileCWS(t, r, "srv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCWS(t, r, "srv")
	if !controllerutil.ContainsFinalizer(got, teraskyv1alpha1.ConversionWebhookServerFinalizer) {
		t.Fatalf("expected the finalizer to be added")
	}

	var svc corev1.Service
	if err := r.Get(context.Background(), types.NamespacedName{Name: "srv-webhook-server", Namespace: "operator-ns"}, &svc); err != nil {
		t.Fatalf("expected a Service to be created: %v", err)
	}
	if len(svc.Spec.Ports) != 2 {
		t.Fatalf("expected 2 service ports (conversion+metrics), got %d", len(svc.Spec.Ports))
	}

	var dep appsv1.Deployment
	if err := r.Get(context.Background(), types.NamespacedName{Name: "srv-webhook-server", Namespace: "operator-ns"}, &dep); err != nil {
		t.Fatalf("expected a Deployment to be created: %v", err)
	}
	if len(dep.Spec.Template.Spec.Containers) != 1 || dep.Spec.Template.Spec.Containers[0].Image != "test/image:v1" {
		t.Fatalf("unexpected deployment containers: %+v", dep.Spec.Template.Spec.Containers)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 2 {
		t.Fatalf("expected the default replica count of 2 when autoscaling is unset, got %v", dep.Spec.Replicas)
	}

	var cert unstructured.Unstructured
	cert.SetGroupVersionKind(CertificateGroupVersionKind)
	if err := r.Get(context.Background(), types.NamespacedName{Name: "srv-webhook-server-cert", Namespace: "operator-ns"}, &cert); err != nil {
		t.Fatalf("expected a Certificate to be created: %v", err)
	}

	// No HPA/PDB should exist: neither Autoscaling nor PodDisruptionBudget was set.
	var hpa autoscalingv2.HorizontalPodAutoscaler
	err := r.Get(context.Background(), types.NamespacedName{Name: "srv-webhook-server", Namespace: "operator-ns"}, &hpa)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected no HPA to exist, got err=%v", err)
	}
	var pdb policyv1.PodDisruptionBudget
	err = r.Get(context.Background(), types.NamespacedName{Name: "srv-webhook-server", Namespace: "operator-ns"}, &pdb)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected no PDB to exist, got err=%v", err)
	}

	// updateStatus should reflect reality: Service exists (ServiceReady
	// True), no cert Secret ever materializes under the fake client
	// (cert-manager isn't running), so CertificateReady stays False, and
	// the Deployment fake client never simulates a controller marking it
	// Available.
	if !meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.CWSConditionServiceReady) {
		t.Fatalf("expected ServiceReady=True, got %+v", got.Status.Conditions)
	}
	requireConditionFalse(t, got.Status.Conditions, teraskyv1alpha1.CWSConditionCertificateReady, "without a real cert-manager")
	requireConditionFalse(t, got.Status.Conditions, teraskyv1alpha1.CWSConditionAvailable, "since the fake client never marks the Deployment Available")
}

// requireConditionFalse fails the test unless conditionType is present with
// Status == False — unlike a bare !meta.IsStatusConditionTrue check, this
// also catches the condition being absent or Unknown, either of which would
// otherwise slip through a merely-not-True assertion.
func requireConditionFalse(t *testing.T, conditions []metav1.Condition, conditionType, why string) {
	t.Helper()
	c := meta.FindStatusCondition(conditions, conditionType)
	if c == nil {
		t.Fatalf("expected condition %s to be present and False (%s), but it was absent: %+v", conditionType, why, conditions)
	}
	if c.Status != metav1.ConditionFalse {
		t.Fatalf("expected condition %s=False (%s), got %s", conditionType, why, c.Status)
	}
}

func TestCWSReconcile_Autoscaling_CreatesHPA_NoStaticReplicas(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			Namespace:   "operator-ns",
			Certificate: teraskyv1alpha1.CertificateSpec{IssuerRef: teraskyv1alpha1.CertificateIssuerRef{Name: "ca-issuer"}},
			Autoscaling: &teraskyv1alpha1.AutoscalingSpec{MinReplicas: 2, MaxReplicas: 5},
		},
	}
	c := newFakeClient(server).Build()
	r := &ConversionWebhookServerReconciler{Client: c, Scheme: newScheme(), DefaultNamespace: "operator-ns"}

	if _, err := reconcileCWS(t, r, "srv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var hpa autoscalingv2.HorizontalPodAutoscaler
	if err := r.Get(context.Background(), types.NamespacedName{Name: "srv-webhook-server", Namespace: "operator-ns"}, &hpa); err != nil {
		t.Fatalf("expected an HPA to be created: %v", err)
	}
	if hpa.Spec.MinReplicas == nil || *hpa.Spec.MinReplicas != 2 || hpa.Spec.MaxReplicas != 5 {
		t.Fatalf("unexpected HPA spec: %+v", hpa.Spec)
	}
}

func TestCWSReconcile_PodDisruptionBudget_Created(t *testing.T) {
	minAvail := intstr.FromInt32(1)
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			Namespace:           "operator-ns",
			Certificate:         teraskyv1alpha1.CertificateSpec{IssuerRef: teraskyv1alpha1.CertificateIssuerRef{Name: "ca-issuer"}},
			PodDisruptionBudget: &teraskyv1alpha1.PodDisruptionBudgetSpec{MinAvailable: &minAvail},
		},
	}
	c := newFakeClient(server).Build()
	r := &ConversionWebhookServerReconciler{Client: c, Scheme: newScheme(), DefaultNamespace: "operator-ns"}

	if _, err := reconcileCWS(t, r, "srv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var pdb policyv1.PodDisruptionBudget
	if err := r.Get(context.Background(), types.NamespacedName{Name: "srv-webhook-server", Namespace: "operator-ns"}, &pdb); err != nil {
		t.Fatalf("expected a PDB to be created: %v", err)
	}
	if pdb.Spec.MinAvailable == nil || pdb.Spec.MinAvailable.IntVal != 1 {
		t.Fatalf("unexpected PDB spec: %+v", pdb.Spec)
	}
}

func TestCWSReconcile_TogglingAutoscalingOff_DeletesExistingHPA(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			Namespace:   "operator-ns",
			Certificate: teraskyv1alpha1.CertificateSpec{IssuerRef: teraskyv1alpha1.CertificateIssuerRef{Name: "ca-issuer"}},
		},
	}
	existingHPA := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: "srv-webhook-server", Namespace: "operator-ns"}}
	c := newFakeClient(server, existingHPA).Build()
	r := &ConversionWebhookServerReconciler{Client: c, Scheme: newScheme(), DefaultNamespace: "operator-ns"}

	if _, err := reconcileCWS(t, r, "srv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var hpa autoscalingv2.HorizontalPodAutoscaler
	err := r.Get(context.Background(), types.NamespacedName{Name: "srv-webhook-server", Namespace: "operator-ns"}, &hpa)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected the pre-existing HPA to be deleted once Autoscaling is unset, got err=%v", err)
	}
}

func TestCWSReconcile_DefaultConflict_SetsCondition(t *testing.T) {
	other := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "other"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: true, Namespace: "operator-ns"},
	}
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			Default: true, Namespace: "operator-ns",
			Certificate: teraskyv1alpha1.CertificateSpec{IssuerRef: teraskyv1alpha1.CertificateIssuerRef{Name: "ca-issuer"}},
		},
	}
	c := newFakeClient(other, server).Build()
	r := &ConversionWebhookServerReconciler{Client: c, Scheme: newScheme(), DefaultNamespace: "operator-ns"}

	if _, err := reconcileCWS(t, r, "srv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := getCWS(t, r, "srv")
	if !meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.CWSConditionDefaultConflict) {
		t.Fatalf("expected DefaultConflict=True when two instances are marked default, got %+v", got.Status.Conditions)
	}
}

func TestCWSReconcile_Delete_BlockedByDependentConfigs(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Namespace: "operator-ns"},
	}
	controllerutil.AddFinalizer(server, teraskyv1alpha1.ConversionWebhookServerFinalizer)
	now := metav1.Now()
	server.DeletionTimestamp = &now

	depCfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	depCfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv"}

	c := newFakeClient(server, depCfg).Build()
	r := &ConversionWebhookServerReconciler{Client: c, Scheme: newScheme(), DefaultNamespace: "operator-ns"}

	res, err := reconcileCWS(t, r, "srv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a requeue while deletion is blocked")
	}
	got := getCWS(t, r, "srv")
	if !controllerutil.ContainsFinalizer(got, teraskyv1alpha1.ConversionWebhookServerFinalizer) {
		t.Fatalf("finalizer must not be removed while a config still depends on this server")
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, teraskyv1alpha1.CWSConditionDeletionBlocked) {
		t.Fatalf("expected DeletionBlocked=True")
	}
}

func TestCWSReconcile_Delete_ForceAnnotationBypassesBlock(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv", Annotations: map[string]string{teraskyv1alpha1.AllowForceDeleteAnnotation: "true"}},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Namespace: "operator-ns"},
	}
	controllerutil.AddFinalizer(server, teraskyv1alpha1.ConversionWebhookServerFinalizer)
	now := metav1.Now()
	server.DeletionTimestamp = &now

	depCfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	depCfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv"}

	c := newFakeClient(server, depCfg).Build()
	r := &ConversionWebhookServerReconciler{Client: c, Scheme: newScheme(), DefaultNamespace: "operator-ns"}

	if _, err := reconcileCWS(t, r, "srv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got teraskyv1alpha1.ConversionWebhookServer
	err := r.Get(context.Background(), types.NamespacedName{Name: "srv"}, &got)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected the server to be gone once force-deleted, got err=%v obj=%+v", err, got)
	}
}

func TestCWSReconcile_Delete_NoDependents_RemovesFinalizer(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Namespace: "operator-ns"},
	}
	controllerutil.AddFinalizer(server, teraskyv1alpha1.ConversionWebhookServerFinalizer)
	now := metav1.Now()
	server.DeletionTimestamp = &now

	c := newFakeClient(server).Build()
	r := &ConversionWebhookServerReconciler{Client: c, Scheme: newScheme(), DefaultNamespace: "operator-ns"}

	if _, err := reconcileCWS(t, r, "srv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got teraskyv1alpha1.ConversionWebhookServer
	err := r.Get(context.Background(), types.NamespacedName{Name: "srv"}, &got)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected the server to be gone once its finalizer is removed, got err=%v obj=%+v", err, got)
	}
}

func TestCWSReconcile_NotFound_IsIgnored(t *testing.T) {
	c := newFakeClient().Build()
	r := &ConversionWebhookServerReconciler{Client: c, Scheme: newScheme(), DefaultNamespace: "operator-ns"}
	if _, err := reconcileCWS(t, r, "does-not-exist"); err != nil {
		t.Fatalf("expected a NotFound Get to be swallowed, got %v", err)
	}
}

// --- pure helper functions, no client needed ---

func TestDeploymentAvailable(t *testing.T) {
	dep := &appsv1.Deployment{}
	if deploymentAvailable(dep) {
		t.Fatalf("expected false with no conditions")
	}
	dep.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse}}
	if deploymentAvailable(dep) {
		t.Fatalf("expected false when the Available condition is False")
	}
	dep.Status.Conditions[0].Status = corev1.ConditionTrue
	if !deploymentAvailable(dep) {
		t.Fatalf("expected true when the Available condition is True")
	}
}

func TestAvailableCondition(t *testing.T) {
	c := availableCondition(true, nil)
	if c.Status != metav1.ConditionTrue || c.Reason != "DeploymentAvailable" {
		t.Fatalf("unexpected condition for available=true: %+v", c)
	}
	c = availableCondition(false, nil)
	if c.Status != metav1.ConditionFalse || c.Reason != "DeploymentNotAvailable" {
		t.Fatalf("unexpected condition for available=false, no error: %+v", c)
	}
	c = availableCondition(false, errors.New("boom"))
	if c.Message == "" || c.Status != metav1.ConditionFalse {
		t.Fatalf("expected a message surfacing the lookup error: %+v", c)
	}
}

func TestCertMessage(t *testing.T) {
	if got := certMessage(errors.New("boom"), false); got == "" {
		t.Fatalf("expected a non-empty message on lookup error")
	}
	if got := certMessage(nil, true); got == "" {
		t.Fatalf("expected a non-empty message when ready")
	}
	if got := certMessage(nil, false); got == "" {
		t.Fatalf("expected a non-empty message when not ready")
	}
}

func TestBoolStatusAndReasonFor(t *testing.T) {
	if boolStatus(true) != metav1.ConditionTrue || boolStatus(false) != metav1.ConditionFalse {
		t.Fatalf("boolStatus mismatch")
	}
	if reasonFor(true, "yes", "no") != "yes" || reasonFor(false, "yes", "no") != "no" {
		t.Fatalf("reasonFor mismatch")
	}
}

func TestOrString(t *testing.T) {
	if orString("", "default") != "default" {
		t.Fatalf("expected the default when empty")
	}
	if orString("set", "default") != "set" {
		t.Fatalf("expected the explicit value to win")
	}
}

func TestPodLabels(t *testing.T) {
	labels := podLabels("srv")
	if labels["app.kubernetes.io/instance"] != "srv" {
		t.Fatalf("unexpected labels: %+v", labels)
	}
}

func TestNamingHelpers(t *testing.T) {
	if cwsDeploymentName("srv") != "srv-webhook-server" {
		t.Fatalf("unexpected deployment name")
	}
	if cwsCertificateSecretName("srv") != "srv-webhook-server-tls" {
		t.Fatalf("unexpected secret name")
	}
}
