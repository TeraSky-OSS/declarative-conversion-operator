/*
Copyright 2026 The xrd-conversion-operator Authors.

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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	teraskyv1alpha1 "github.com/vrabbi/xrd-conversion-operator/api/v1alpha1"
	"github.com/vrabbi/xrd-conversion-operator/internal/assign"
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
	defaultWebhookServerImage   = "ghcr.io/vrabbi/xrd-conversion-webhook-server:latest"
	defaultServiceAccountName   = "xrd-conversion-webhook-server"
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
}

// +kubebuilder:rbac:groups=terasky.com,resources=conversionwebhookservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=terasky.com,resources=conversionwebhookservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=terasky.com,resources=conversionwebhookservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=terasky.com,resources=xrdconversionconfigs,verbs=get;list;watch
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
	return r.Patch(ctx, cert, client.Apply, client.ForceOwnership, client.FieldOwner(FieldOwner))
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
	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: cwsServiceName(server.Name), Namespace: namespace, Annotations: server.Spec.Service.Annotations, Labels: podLabels(server.Name)},
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: podLabels(server.Name),
			Ports: []corev1.ServicePort{
				{Name: "conversion", Port: port, TargetPort: intstr.FromInt32(webhookServerConversionPort)},
				{Name: "metrics", Port: webhookServerMetricsPort, TargetPort: intstr.FromInt32(webhookServerMetricsPort)},
			},
		},
	}
	if err := controllerutil.SetControllerReference(server, svc, r.Scheme); err != nil {
		return err
	}
	return r.Patch(ctx, svc, client.Apply, client.ForceOwnership, client.FieldOwner(FieldOwner))
}

func podLabels(server string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "xrd-conversion-webhook-server",
		"app.kubernetes.io/instance":   server,
		"app.kubernetes.io/managed-by": "xrd-conversion-operator",
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

	dep := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: cwsDeploymentName(server.Name), Namespace: namespace, Labels: podLabels(server.Name)},
		Spec: appsv1.DeploymentSpec{
			Replicas: replicas,
			Selector: &metav1.LabelSelector{MatchLabels: podLabels(server.Name)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels(server.Name)},
				Spec: corev1.PodSpec{
					ServiceAccountName: saName,
					NodeSelector:       server.Spec.NodeSelector,
					Tolerations:        server.Spec.Tolerations,
					Affinity:           server.Spec.Affinity,
					PriorityClassName:  server.Spec.PriorityClassName,
					Containers: []corev1.Container{{
						Name:            "webhook-server",
						Image:           image,
						ImagePullPolicy: pullPolicy,
						Args: []string{
							fmt.Sprintf("--webhook-server-name=%s", server.Name),
							fmt.Sprintf("--tls-cert-dir=%s", "/tls"),
							fmt.Sprintf("--conversion-port=%d", webhookServerConversionPort),
							fmt.Sprintf("--metrics-port=%d", webhookServerMetricsPort),
						},
						Ports: []corev1.ContainerPort{
							{Name: "conversion", ContainerPort: webhookServerConversionPort},
							{Name: "metrics", ContainerPort: webhookServerMetricsPort},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "tls", MountPath: "/tls", ReadOnly: true}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler:     corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt32(webhookServerMetricsPort), Scheme: corev1.URISchemeHTTP}},
							PeriodSeconds:    5,
							FailureThreshold: 3,
						},
						LivenessProbe: &corev1.Probe{
							ProbeHandler:     corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(webhookServerMetricsPort), Scheme: corev1.URISchemeHTTP}},
							PeriodSeconds:    10,
							FailureThreshold: 3,
						},
						Resources: server.Spec.Resources,
					}},
					Volumes: []corev1.Volume{{
						Name: "tls",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: cwsCertificateSecretName(server.Name)},
						},
					}},
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(server, dep, r.Scheme); err != nil {
		return err
	}
	return r.Patch(ctx, dep, client.Apply, client.ForceOwnership, client.FieldOwner(FieldOwner))
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
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		TypeMeta:   metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"},
		ObjectMeta: metav1.ObjectMeta{Name: cwsHPAName(server.Name), Namespace: namespace},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: cwsDeploymentName(server.Name)},
			MinReplicas:    &server.Spec.Autoscaling.MinReplicas,
			MaxReplicas:    server.Spec.Autoscaling.MaxReplicas,
			Metrics: []autoscalingv2.MetricSpec{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{
					Name:   corev1.ResourceCPU,
					Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &target},
				},
			}},
		},
	}
	if err := controllerutil.SetControllerReference(server, hpa, r.Scheme); err != nil {
		return err
	}
	return r.Patch(ctx, hpa, client.Apply, client.ForceOwnership, client.FieldOwner(FieldOwner))
}

func (r *ConversionWebhookServerReconciler) reconcilePDB(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer, namespace string) error {
	if server.Spec.PodDisruptionBudget == nil {
		existing := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: cwsPDBName(server.Name), Namespace: namespace}}
		return client.IgnoreNotFound(r.Delete(ctx, existing))
	}
	pdb := &policyv1.PodDisruptionBudget{
		TypeMeta:   metav1.TypeMeta{APIVersion: "policy/v1", Kind: "PodDisruptionBudget"},
		ObjectMeta: metav1.ObjectMeta{Name: cwsPDBName(server.Name), Namespace: namespace},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable:   server.Spec.PodDisruptionBudget.MinAvailable,
			MaxUnavailable: server.Spec.PodDisruptionBudget.MaxUnavailable,
			Selector:       &metav1.LabelSelector{MatchLabels: podLabels(server.Name)},
		},
	}
	if err := controllerutil.SetControllerReference(server, pdb, r.Scheme); err != nil {
		return err
	}
	return r.Patch(ctx, pdb, client.Apply, client.ForceOwnership, client.FieldOwner(FieldOwner))
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

	var configs teraskyv1alpha1.XRDConversionConfigList
	if err := r.List(ctx, &configs); err != nil {
		return err
	}
	var allServers teraskyv1alpha1.ConversionWebhookServerList
	if err := r.List(ctx, &allServers); err != nil {
		return err
	}
	assigned := assign.ConfigsAssignedTo(configs.Items, allServers.Items, server.Name)
	refs := make([]teraskyv1alpha1.AssignedConfigRef, 0, len(assigned))
	for _, c := range assigned {
		refs = append(refs, teraskyv1alpha1.AssignedConfigRef{Name: c.Name, XRDName: c.Spec.TargetXRD.Name, Phase: c.Status.Phase})
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
		var configs teraskyv1alpha1.XRDConversionConfigList
		if err := r.List(ctx, &configs); err != nil {
			return ctrl.Result{}, err
		}
		var allServers teraskyv1alpha1.ConversionWebhookServerList
		if err := r.List(ctx, &allServers); err != nil {
			return ctrl.Result{}, err
		}
		dependents := assign.ConfigsAssignedTo(configs.Items, allServers.Items, server.Name)
		if len(dependents) > 0 {
			names := make([]string, 0, len(dependents))
			for _, c := range dependents {
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
				Message: fmt.Sprintf("%d XRDConversionConfig(s) still resolve to this instance%s: %v. Reassign them or add annotation %q=\"true\" to force.", len(dependents), suffix, names, teraskyv1alpha1.AllowForceDeleteAnnotation),
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
