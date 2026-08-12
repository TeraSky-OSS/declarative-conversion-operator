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
	"encoding/json"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applyautoscalingv2 "k8s.io/client-go/applyconfigurations/autoscaling/v2"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	applymetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	applypolicyv1 "k8s.io/client-go/applyconfigurations/policy/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/assign"
)

// CertificateGroupVersionKind identifies a cert-manager Certificate.
// cert-manager isn't vendored as a Go dependency here, for the same reason
// pkg/xrdadapter avoids vendoring Crossplane: the shape this operator needs
// is small and stable, and unstructured SSA avoids tracking cert-manager's
// release cadence as a compile-time dependency.
var CertificateGroupVersionKind = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}

const (
	webhookServerConversionPort = 9443
	webhookServerMetricsPort    = 8443
	defaultWebhookServerImage   = "ghcr.io/terasky-oss/declarative-conversion-webhook-server:latest"
	defaultServiceAccountName   = "declarative-conversion-webhook-server"
)

// ConversionWebhookServerReconciler reconciles a ConversionWebhookServer.
type ConversionWebhookServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// DefaultNamespace is used for instances that don't set spec.namespace
	// — normally the operator's own install namespace.
	DefaultNamespace string
	// DefaultImage is the webhook-server image used when an instance
	// doesn't override spec.image.
	DefaultImage string
	// EnableXRDSupport/EnableCRDSupport mirror the manager's own
	// --enable-xrd-support/--enable-crd-support flags and are baked into
	// every webhook-server Deployment this reconciler creates, so each
	// replica's own manager (a separate process/binary — see
	// internal/webhookserver.Reconciler) watches exactly the same set of
	// resource kinds the operator itself does. Keeping the two in lock-step
	// matters because watching a GVK whose CRD doesn't exist (Crossplane's
	// CompositeResourceDefinition, when XRD support is disabled) is fatal
	// for a webhook-server pod exactly the same way it is for the manager.
	EnableXRDSupport bool
	EnableCRDSupport bool
}

// +kubebuilder:rbac:groups=terasky.com,resources=conversionwebhookservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=terasky.com,resources=conversionwebhookservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=terasky.com,resources=conversionwebhookservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=terasky.com,resources=xrdconversionconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=terasky.com,resources=crdconversionconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *ConversionWebhookServerReconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	var server teraskyv1alpha1.ConversionWebhookServer
	if err := r.Get(ctx, req.NamespacedName, &server); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !server.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &server)
	}

	if !controllerutil.ContainsFinalizer(&server, teraskyv1alpha1.ConversionWebhookServerFinalizer) {
		controllerutil.AddFinalizer(&server, teraskyv1alpha1.ConversionWebhookServerFinalizer)
		if err := r.Update(ctx, &server); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	return r.reconcileNormal(ctx, &server)
}

func (r *ConversionWebhookServerReconciler) reconcileNormal(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer) (ctrl.Result, error) {
	orig := server.DeepCopy()
	namespace := server.Spec.Namespace
	if namespace == "" {
		namespace = r.DefaultNamespace
	}

	if err := r.checkDefaultConflict(ctx, server); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileCertificate(ctx, server, namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling certificate: %w", err)
	}
	if err := r.reconcileService(ctx, server, namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling service: %w", err)
	}
	if err := r.reconcileDeployment(ctx, server, namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling deployment: %w", err)
	}
	if err := r.reconcileHPA(ctx, server, namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling HPA: %w", err)
	}
	if err := r.reconcilePDB(ctx, server, namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling PDB: %w", err)
	}

	if err := r.updateStatus(ctx, server, namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	if err := r.Status().Patch(ctx, server, client.MergeFrom(orig)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching status: %w", err)
	}
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// checkDefaultConflict flags (but does not auto-fix) the case where more
// than one instance is marked default — a state the admission webhook
// should normally prevent, but direct edits or restores can still produce.
func (r *ConversionWebhookServerReconciler) checkDefaultConflict(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer) error {
	if !server.Spec.Default {
		meta.RemoveStatusCondition(&server.Status.Conditions, teraskyv1alpha1.CWSConditionDefaultConflict)
		return nil
	}
	var all teraskyv1alpha1.ConversionWebhookServerList
	if err := r.List(ctx, &all); err != nil {
		return err
	}
	var defaults []string
	for _, s := range all.Items {
		if s.Spec.Default {
			defaults = append(defaults, s.Name)
		}
	}
	if len(defaults) > 1 {
		sort.Strings(defaults)
		meta.SetStatusCondition(&server.Status.Conditions, metav1.Condition{
			Type: teraskyv1alpha1.CWSConditionDefaultConflict, Status: metav1.ConditionTrue, Reason: "MultipleDefaults",
			Message: fmt.Sprintf("multiple ConversionWebhookServer instances are marked default: %v; this must be resolved manually", defaults),
		})
	} else {
		meta.RemoveStatusCondition(&server.Status.Conditions, teraskyv1alpha1.CWSConditionDefaultConflict)
	}
	return nil
}

func (r *ConversionWebhookServerReconciler) reconcileCertificate(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer, namespace string) error {
	dnsNames := server.Spec.Certificate.DNSNames
	if len(dnsNames) == 0 {
		svc := cwsServiceName(server.Name)
		dnsNames = []string{
			svc,
			fmt.Sprintf("%s.%s", svc, namespace),
			fmt.Sprintf("%s.%s.svc", svc, namespace),
			fmt.Sprintf("%s.%s.svc.cluster.local", svc, namespace),
		}
	}
	spec := map[string]any{
		"secretName": cwsCertificateSecretName(server.Name),
		"dnsNames":   toAnySlice(dnsNames),
		"issuerRef": map[string]any{
			"name": server.Spec.Certificate.IssuerRef.Name,
			"kind": orString(server.Spec.Certificate.IssuerRef.Kind, "ClusterIssuer"),
		},
	}
	if server.Spec.Certificate.Duration != nil {
		spec["duration"] = server.Spec.Certificate.Duration.Duration.String()
	}
	if server.Spec.Certificate.RenewBefore != nil {
		spec["renewBefore"] = server.Spec.Certificate.RenewBefore.Duration.String()
	}

	cert := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": CertificateGroupVersionKind.GroupVersion().String(),
		"kind":       CertificateGroupVersionKind.Kind,
		"metadata": map[string]any{
			"name":      cwsCertificateName(server.Name),
			"namespace": namespace,
		},
		"spec": spec,
	}}
	if err := controllerutil.SetControllerReference(server, cert, r.Scheme); err != nil {
		return err
	}
	return r.Apply(ctx, client.ApplyConfigurationFromUnstructured(cert), client.ForceOwnership, client.FieldOwner(FieldOwner))
}

// ownerReferenceApplyConfiguration builds the owner reference this
// controller sets on every child resource it manages, matching exactly
// what controllerutil.SetControllerReference would produce for a
// cluster-scoped ConversionWebhookServer owning a namespaced dependent.
func ownerReferenceApplyConfiguration(server *teraskyv1alpha1.ConversionWebhookServer) *applymetav1.OwnerReferenceApplyConfiguration {
	return applymetav1.OwnerReference().
		WithAPIVersion(teraskyv1alpha1.GroupVersion.String()).
		WithKind("ConversionWebhookServer").
		WithName(server.Name).
		WithUID(server.UID).
		WithController(true).
		WithBlockOwnerDeletion(true)
}

// viaJSON converts a concrete API type (e.g. corev1.Toleration, as stored
// verbatim in a CRD spec field) into its ApplyConfiguration equivalent by
// round-tripping through JSON. This is safe here because ApplyConfiguration
// types are, by design, structurally identical to their API counterparts
// (the same fields, the same json tags, only made into pointers for
// optionality) — the encoding that goes over the wire to the API server is
// the same either way.
func viaJSON[Dst any](src any) (*Dst, error) {
	b, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}
	var dst Dst
	if err := json.Unmarshal(b, &dst); err != nil {
		return nil, err
	}
	return &dst, nil
}

func orString(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (r *ConversionWebhookServerReconciler) reconcileService(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer, namespace string) error {
	port := server.Spec.Service.Port
	if port == 0 {
		port = 443
	}
	svcType := server.Spec.Service.Type
	if svcType == "" {
		svcType = corev1.ServiceTypeClusterIP
	}
	svc := applycorev1.Service(cwsServiceName(server.Name), namespace).
		WithLabels(podLabels(server.Name)).
		WithAnnotations(server.Spec.Service.Annotations).
		WithOwnerReferences(ownerReferenceApplyConfiguration(server)).
		WithSpec(applycorev1.ServiceSpec().
			WithType(svcType).
			WithSelector(podLabels(server.Name)).
			WithPorts(
				applycorev1.ServicePort().WithName("conversion").WithPort(port).WithTargetPort(intstr.FromInt32(webhookServerConversionPort)),
				applycorev1.ServicePort().WithName("metrics").WithPort(webhookServerMetricsPort).WithTargetPort(intstr.FromInt32(webhookServerMetricsPort)),
			))
	return r.Apply(ctx, svc, client.ForceOwnership, client.FieldOwner(FieldOwner))
}

func podLabels(server string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "declarative-conversion-webhook-server",
		"app.kubernetes.io/instance":   server,
		"app.kubernetes.io/managed-by": "declarative-conversion-operator",
	}
}

func (r *ConversionWebhookServerReconciler) reconcileDeployment(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer, namespace string) error {
	image := r.DefaultImage
	if image == "" {
		image = defaultWebhookServerImage
	}
	var pullPolicy corev1.PullPolicy
	if server.Spec.Image != nil {
		if server.Spec.Image.Repository != "" && server.Spec.Image.Tag != "" {
			image = fmt.Sprintf("%s:%s", server.Spec.Image.Repository, server.Spec.Image.Tag)
		}
		pullPolicy = server.Spec.Image.PullPolicy
	}

	saName := server.Spec.ServiceAccountName
	if saName == "" {
		saName = defaultServiceAccountName
	}

	var replicas *int32
	if server.Spec.Autoscaling == nil {
		replicas = server.Spec.Replicas
		if replicas == nil {
			two := int32(2)
			replicas = &two
		}
	}
	// When Autoscaling is set, replicas is left nil so the HPA owns it and
	// this controller never fights it on every reconcile.

	// Operator-managed flags first so identity/TLS/bind/feature wiring is
	// always present; ExtraArgs are appended for optional flags only.
	// Reject managed-flag overrides here too so a bypassed admission webhook
	// cannot still rewrite identity/TLS/bind/feature args on the Deployment.
	if err := teraskyv1alpha1.ValidateWebhookServerExtraArgs(server.Spec.ExtraArgs); err != nil {
		return err
	}
	args := []string{
		fmt.Sprintf("--webhook-server-name=%s", server.Name),
		"--tls-cert-dir=/tls",
		fmt.Sprintf("--conversion-bind-address=:%d", webhookServerConversionPort),
		fmt.Sprintf("--metrics-bind-address=:%d", webhookServerMetricsPort),
		fmt.Sprintf("--enable-xrd-support=%t", r.EnableXRDSupport),
		fmt.Sprintf("--enable-crd-support=%t", r.EnableCRDSupport),
	}
	args = append(args, server.Spec.ExtraArgs...)

	container := applycorev1.Container().
		WithName("webhook-server").
		WithImage(image).
		WithArgs(args...).
		WithPorts(
			applycorev1.ContainerPort().WithName("conversion").WithContainerPort(webhookServerConversionPort),
			applycorev1.ContainerPort().WithName("metrics").WithContainerPort(webhookServerMetricsPort),
		).
		WithVolumeMounts(applycorev1.VolumeMount().WithName("tls").WithMountPath("/tls").WithReadOnly(true)).
		WithReadinessProbe(applycorev1.Probe().
			WithHTTPGet(applycorev1.HTTPGetAction().WithPath("/readyz").WithPort(intstr.FromInt32(webhookServerMetricsPort)).WithScheme(corev1.URISchemeHTTP)).
			WithPeriodSeconds(5).WithFailureThreshold(3)).
		WithLivenessProbe(applycorev1.Probe().
			WithHTTPGet(applycorev1.HTTPGetAction().WithPath("/healthz").WithPort(intstr.FromInt32(webhookServerMetricsPort)).WithScheme(corev1.URISchemeHTTP)).
			WithPeriodSeconds(10).WithFailureThreshold(3)).
		WithResources(applycorev1.ResourceRequirements().
			WithRequests(server.Spec.Resources.Requests).
			WithLimits(server.Spec.Resources.Limits))
	if pullPolicy != "" {
		container = container.WithImagePullPolicy(pullPolicy)
	}

	podSpec := applycorev1.PodSpec().
		WithServiceAccountName(saName).
		WithContainers(container).
		WithVolumes(applycorev1.Volume().WithName("tls").
			WithSecret(applycorev1.SecretVolumeSource().WithSecretName(cwsCertificateSecretName(server.Name))))
	if len(server.Spec.NodeSelector) > 0 {
		podSpec = podSpec.WithNodeSelector(server.Spec.NodeSelector)
	}
	if server.Spec.PriorityClassName != "" {
		podSpec = podSpec.WithPriorityClassName(server.Spec.PriorityClassName)
	}
	// Tolerations/Affinity are converted from their concrete API types (as
	// stored verbatim in spec) to ApplyConfigurations via a JSON round trip
	// — see viaJSON's doc comment for why that's safe here.
	for _, t := range server.Spec.Tolerations {
		tc, err := viaJSON[applycorev1.TolerationApplyConfiguration](t)
		if err != nil {
			return fmt.Errorf("converting toleration: %w", err)
		}
		podSpec = podSpec.WithTolerations(tc)
	}
	if server.Spec.Affinity != nil {
		ac, err := viaJSON[applycorev1.AffinityApplyConfiguration](server.Spec.Affinity)
		if err != nil {
			return fmt.Errorf("converting affinity: %w", err)
		}
		podSpec = podSpec.WithAffinity(ac)
	}

	depSpec := applyappsv1.DeploymentSpec().
		WithSelector(applymetav1.LabelSelector().WithMatchLabels(podLabels(server.Name))).
		WithTemplate(applycorev1.PodTemplateSpec().
			WithLabels(podLabels(server.Name)).
			WithSpec(podSpec))
	if replicas != nil {
		depSpec = depSpec.WithReplicas(*replicas)
	}

	dep := applyappsv1.Deployment(cwsDeploymentName(server.Name), namespace).
		WithLabels(podLabels(server.Name)).
		WithOwnerReferences(ownerReferenceApplyConfiguration(server)).
		WithSpec(depSpec)
	return r.Apply(ctx, dep, client.ForceOwnership, client.FieldOwner(FieldOwner))
}

func (r *ConversionWebhookServerReconciler) reconcileHPA(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer, namespace string) error {
	if server.Spec.Autoscaling == nil {
		existing := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: cwsHPAName(server.Name), Namespace: namespace}}
		return client.IgnoreNotFound(r.Delete(ctx, existing))
	}
	target := server.Spec.Autoscaling.TargetCPUUtilizationPercentage
	if target == 0 {
		target = 75
	}
	hpa := applyautoscalingv2.HorizontalPodAutoscaler(cwsHPAName(server.Name), namespace).
		WithOwnerReferences(ownerReferenceApplyConfiguration(server)).
		WithSpec(applyautoscalingv2.HorizontalPodAutoscalerSpec().
			WithScaleTargetRef(applyautoscalingv2.CrossVersionObjectReference().
				WithAPIVersion("apps/v1").WithKind("Deployment").WithName(cwsDeploymentName(server.Name))).
			WithMinReplicas(server.Spec.Autoscaling.MinReplicas).
			WithMaxReplicas(server.Spec.Autoscaling.MaxReplicas).
			WithMetrics(applyautoscalingv2.MetricSpec().
				WithType(autoscalingv2.ResourceMetricSourceType).
				WithResource(applyautoscalingv2.ResourceMetricSource().
					WithName(corev1.ResourceCPU).
					WithTarget(applyautoscalingv2.MetricTarget().
						WithType(autoscalingv2.UtilizationMetricType).
						WithAverageUtilization(target)))))
	return r.Apply(ctx, hpa, client.ForceOwnership, client.FieldOwner(FieldOwner))
}

func (r *ConversionWebhookServerReconciler) reconcilePDB(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer, namespace string) error {
	if server.Spec.PodDisruptionBudget == nil {
		existing := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: cwsPDBName(server.Name), Namespace: namespace}}
		return client.IgnoreNotFound(r.Delete(ctx, existing))
	}
	pdbSpec := applypolicyv1.PodDisruptionBudgetSpec().
		WithSelector(applymetav1.LabelSelector().WithMatchLabels(podLabels(server.Name)))
	if server.Spec.PodDisruptionBudget.MinAvailable != nil {
		pdbSpec = pdbSpec.WithMinAvailable(*server.Spec.PodDisruptionBudget.MinAvailable)
	}
	if server.Spec.PodDisruptionBudget.MaxUnavailable != nil {
		pdbSpec = pdbSpec.WithMaxUnavailable(*server.Spec.PodDisruptionBudget.MaxUnavailable)
	}
	pdb := applypolicyv1.PodDisruptionBudget(cwsPDBName(server.Name), namespace).
		WithOwnerReferences(ownerReferenceApplyConfiguration(server)).
		WithSpec(pdbSpec)
	return r.Apply(ctx, pdb, client.ForceOwnership, client.FieldOwner(FieldOwner))
}

func (r *ConversionWebhookServerReconciler) updateStatus(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer, namespace string) error {
	server.Status.ObservedGeneration = server.Generation

	var dep appsv1.Deployment
	depErr := r.Get(ctx, types.NamespacedName{Name: cwsDeploymentName(server.Name), Namespace: namespace}, &dep)
	available := depErr == nil && deploymentAvailable(&dep)
	if depErr == nil {
		server.Status.Replicas = dep.Status.Replicas
		server.Status.ReadyReplicas = dep.Status.ReadyReplicas
	}
	meta.SetStatusCondition(&server.Status.Conditions, availableCondition(available, depErr))

	var svc corev1.Service
	svcErr := r.Get(ctx, types.NamespacedName{Name: cwsServiceName(server.Name), Namespace: namespace}, &svc)
	meta.SetStatusCondition(&server.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.CWSConditionServiceReady, Status: boolStatus(svcErr == nil), Reason: reasonFor(svcErr == nil, "ServiceReady", "ServiceMissing"),
		Message: fmt.Sprintf("service lookup error: %v", svcErr),
	})
	if svcErr == nil {
		port := server.Spec.Service.Port
		if port == 0 {
			port = 443
		}
		server.Status.Endpoint = fmt.Sprintf("https://%s.%s.svc:%d", cwsServiceName(server.Name), namespace, port)
	}

	var secret corev1.Secret
	secErr := r.Get(ctx, types.NamespacedName{Name: cwsCertificateSecretName(server.Name), Namespace: namespace}, &secret)
	certReady := secErr == nil && (len(secret.Data["tls.crt"]) > 0)
	meta.SetStatusCondition(&server.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.CWSConditionCertificateReady, Status: boolStatus(certReady), Reason: reasonFor(certReady, "CertificateReady", "CertificateNotReady"),
		Message: certMessage(secErr, certReady),
	})

	var xrdConfigs teraskyv1alpha1.XRDConversionConfigList
	if err := r.List(ctx, &xrdConfigs); err != nil {
		return err
	}
	var crdConfigs teraskyv1alpha1.CRDConversionConfigList
	if err := r.List(ctx, &crdConfigs); err != nil {
		return err
	}
	var allServers teraskyv1alpha1.ConversionWebhookServerList
	if err := r.List(ctx, &allServers); err != nil {
		return err
	}
	assignedXRD := assign.ConfigsAssignedTo(xrdConfigs.Items, allServers.Items, server.Name)
	assignedCRD := assign.ConfigsAssignedTo(crdConfigs.Items, allServers.Items, server.Name)
	refs := make([]teraskyv1alpha1.AssignedConfigRef, 0, len(assignedXRD)+len(assignedCRD))
	for _, c := range assignedXRD {
		refs = append(refs, teraskyv1alpha1.AssignedConfigRef{Name: c.Name, XRDName: c.Spec.TargetXRD.Name, Phase: c.Status.Phase})
	}
	for _, c := range assignedCRD {
		refs = append(refs, teraskyv1alpha1.AssignedConfigRef{Name: c.Name, XRDName: c.Spec.TargetCRD.Name, Phase: c.Status.Phase})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	server.Status.AssignedConfigs = refs

	return nil
}

func deploymentAvailable(dep *appsv1.Deployment) bool {
	for _, c := range dep.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func availableCondition(available bool, err error) metav1.Condition {
	msg := "deployment is Available"
	if !available {
		msg = "deployment is not yet Available"
		if err != nil {
			msg = fmt.Sprintf("deployment lookup error: %v", err)
		}
	}
	return metav1.Condition{Type: teraskyv1alpha1.CWSConditionAvailable, Status: boolStatus(available), Reason: reasonFor(available, "DeploymentAvailable", "DeploymentNotAvailable"), Message: msg}
}

func certMessage(err error, ready bool) string {
	if err != nil {
		return fmt.Sprintf("certificate secret lookup error: %v", err)
	}
	if ready {
		return "certificate secret contains a TLS certificate"
	}
	return "certificate secret does not yet contain a TLS certificate"
}

func boolStatus(b bool) metav1.ConditionStatus {
	if b {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func reasonFor(b bool, trueReason, falseReason string) string {
	if b {
		return trueReason
	}
	return falseReason
}

// reconcileDelete blocks deletion while any XRDConversionConfig still
// resolves to this instance (explicitly, or via default fallback), unless
// the explicit force-delete annotation is present at this reconcile.
func (r *ConversionWebhookServerReconciler) reconcileDelete(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(server, teraskyv1alpha1.ConversionWebhookServerFinalizer) {
		return ctrl.Result{}, nil
	}

	forceDelete := server.Annotations[teraskyv1alpha1.AllowForceDeleteAnnotation] == "true"
	if !forceDelete {
		var xrdConfigs teraskyv1alpha1.XRDConversionConfigList
		if err := r.List(ctx, &xrdConfigs); err != nil {
			return ctrl.Result{}, err
		}
		var crdConfigs teraskyv1alpha1.CRDConversionConfigList
		if err := r.List(ctx, &crdConfigs); err != nil {
			return ctrl.Result{}, err
		}
		var allServers teraskyv1alpha1.ConversionWebhookServerList
		if err := r.List(ctx, &allServers); err != nil {
			return ctrl.Result{}, err
		}
		dependentXRD := assign.ConfigsAssignedTo(xrdConfigs.Items, allServers.Items, server.Name)
		dependentCRD := assign.ConfigsAssignedTo(crdConfigs.Items, allServers.Items, server.Name)
		if len(dependentXRD)+len(dependentCRD) > 0 {
			names := make([]string, 0, len(dependentXRD)+len(dependentCRD))
			for _, c := range dependentXRD {
				names = append(names, c.Name)
			}
			for _, c := range dependentCRD {
				names = append(names, c.Name)
			}
			sort.Strings(names)
			orig := server.DeepCopy()
			suffix := ""
			if server.Spec.Default {
				suffix = " (this is the DEFAULT instance — configs with no explicit webhookServerRef depend on it too)"
			}
			meta.SetStatusCondition(&server.Status.Conditions, metav1.Condition{
				Type: teraskyv1alpha1.CWSConditionDeletionBlocked, Status: metav1.ConditionTrue, Reason: "ConfigsStillAssigned",
				Message: fmt.Sprintf("%d config(s) still resolve to this instance%s: %v. Reassign them or add annotation %q=\"true\" to force.", len(dependentXRD)+len(dependentCRD), suffix, names, teraskyv1alpha1.AllowForceDeleteAnnotation),
			})
			if err := r.Status().Patch(ctx, server, client.MergeFrom(orig)); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}

	controllerutil.RemoveFinalizer(server, teraskyv1alpha1.ConversionWebhookServerFinalizer)
	if err := r.Update(ctx, server); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager wires up watches: the primary ConversionWebhookServer
// resource, its owned child resources (so external edits/deletes to them
// self-heal), and XRDConversionConfig (any change re-evaluates every
// server's status.assignedConfigs).
func (r *ConversionWebhookServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&teraskyv1alpha1.ConversionWebhookServer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&autoscalingv2.HorizontalPodAutoscaler{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Watches(&teraskyv1alpha1.XRDConversionConfig{}, handler.EnqueueRequestsFromMapFunc(enqueueAllServers(r.Client))).
		Named("conversionwebhookserver").
		Complete(r)
}

func enqueueAllServers(c client.Client) func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(ctx context.Context, _ client.Object) []reconcile.Request {
		var list teraskyv1alpha1.ConversionWebhookServerList
		if err := c.List(ctx, &list); err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(list.Items))
		for _, s := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: s.Name}})
		}
		return reqs
	}
}
