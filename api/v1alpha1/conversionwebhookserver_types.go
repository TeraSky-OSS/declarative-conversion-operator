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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// ImageSpec overrides the container image used for a ConversionWebhookServer
// instance's pods. Leave unset to use the operator's own default (set via
// Helm values / manager flags).
type ImageSpec struct {
	// +optional
	Repository string `json:"repository,omitempty"`
	// +optional
	Tag string `json:"tag,omitempty"`
	// +optional
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// AutoscalingSpec configures a HorizontalPodAutoscaler for the instance.
// Mutually exclusive with a fixed Replicas count once set — the HPA owns
// the replica count from that point on.
type AutoscalingSpec struct {
	// +kubebuilder:validation:Minimum=1
	MinReplicas int32 `json:"minReplicas"`
	// +kubebuilder:validation:Minimum=1
	MaxReplicas int32 `json:"maxReplicas"`
	// +optional
	// +kubebuilder:default=75
	TargetCPUUtilizationPercentage int32 `json:"targetCPUUtilizationPercentage,omitempty"`
}

// CertificateSpec configures the cert-manager Certificate issued for this
// instance's webhook TLS.
type CertificateSpec struct {
	IssuerRef CertificateIssuerRef `json:"issuerRef"`
	// +optional
	DNSNames []string `json:"dnsNames,omitempty"`
	// +optional
	Duration *metav1.Duration `json:"duration,omitempty"`
	// +optional
	RenewBefore *metav1.Duration `json:"renewBefore,omitempty"`
}

// CertificateIssuerRef references a cert-manager Issuer or ClusterIssuer.
type CertificateIssuerRef struct {
	Name string `json:"name"`
	// +optional
	// +kubebuilder:default=ClusterIssuer
	// +kubebuilder:validation:Enum=Issuer;ClusterIssuer
	Kind string `json:"kind,omitempty"`
}

// ServiceSpec configures the Service fronting this instance's pods.
type ServiceSpec struct {
	// +optional
	// +kubebuilder:default=ClusterIP
	Type corev1.ServiceType `json:"type,omitempty"`
	// +optional
	// +kubebuilder:default=443
	Port int32 `json:"port,omitempty"`
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// PodDisruptionBudgetSpec configures a PDB for this instance's pods.
type PodDisruptionBudgetSpec struct {
	// +optional
	MinAvailable *intstr.IntOrString `json:"minAvailable,omitempty"`
	// +optional
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`
}

// ConversionWebhookServerSpec defines the desired state of a deployable
// conversion webhook server instance.
type ConversionWebhookServerSpec struct {
	// Default marks this instance as the fallback target for
	// XRDConversionConfigs that don't set spec.webhookServerRef. At most
	// one instance may be default at a time; the admission webhook
	// rejects setting a second one.
	// +optional
	Default bool `json:"default,omitempty"`

	// Namespace is where this instance's Deployment/Service/etc. are
	// created. Defaults to the operator's own install namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// +optional
	// +kubebuilder:default=2
	Replicas *int32 `json:"replicas,omitempty"`
	// +optional
	Autoscaling *AutoscalingSpec `json:"autoscaling,omitempty"`

	// +optional
	Image *ImageSpec `json:"image,omitempty"`
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	Certificate CertificateSpec `json:"certificate"`
	// +optional
	Service ServiceSpec `json:"service,omitempty"`
	// +optional
	PodDisruptionBudget *PodDisruptionBudgetSpec `json:"podDisruptionBudget,omitempty"`
}

// AssignedConfigRef is one XRDConversionConfig the resolver currently
// assigns to this instance. This reflects DESIRED assignment as computed
// by the shared resolver, not proof that every replica has actually loaded
// it — per-replica actual state lives in each pod's own metrics and
// /debug/registry endpoint.
type AssignedConfigRef struct {
	Name    string `json:"name"`
	XRDName string `json:"xrdName"`
	// +optional
	Phase string `json:"phase,omitempty"`
}

// ConversionWebhookServerStatus is the observed state of a
// ConversionWebhookServer instance.
type ConversionWebhookServerStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	// +optional
	Replicas int32 `json:"replicas,omitempty"`
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// +optional
	AssignedConfigs []AssignedConfigRef `json:"assignedConfigs,omitempty"`
}

// Condition type constants for ConversionWebhookServer.
const (
	CWSConditionAvailable        = "Available"
	CWSConditionCertificateReady = "CertificateReady"
	CWSConditionServiceReady     = "ServiceReady"
	CWSConditionDeletionBlocked  = "DeletionBlocked"
	CWSConditionDefaultConflict  = "DefaultConflict"
)

// Finalizer used to block deletion of a ConversionWebhookServer while any
// XRDConversionConfig still resolves to it (explicitly or as default).
const ConversionWebhookServerFinalizer = "conversion.terasky.com/protect-in-use"

// AllowForceDeleteAnnotation is the explicit break-glass annotation that
// lets an admin delete a ConversionWebhookServer even though
// XRDConversionConfigs still depend on it, checked live at the moment of
// the delete reconcile.
const AllowForceDeleteAnnotation = "conversion.terasky.com/allow-force-delete"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Default",type=boolean,JSONPath=".spec.default"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=".status.endpoint"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// ConversionWebhookServer describes a deployable, independently scalable
// instance of the shared conversion webhook runtime.
type ConversionWebhookServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConversionWebhookServerSpec   `json:"spec,omitempty"`
	Status ConversionWebhookServerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ConversionWebhookServerList contains a list of ConversionWebhookServer.
type ConversionWebhookServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ConversionWebhookServer `json:"items"`
}
