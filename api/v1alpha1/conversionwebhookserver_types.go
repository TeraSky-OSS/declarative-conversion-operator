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
	// Digest, when set, pins the image by content digest and takes
	// precedence over Tag (rendered as repository@digest). Prefer a full
	// digest reference such as "sha256:...".
	// +optional
	Digest string `json:"digest,omitempty"`
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

	// ExtraArgs are additional container arguments appended after the
	// operator-managed flags (--webhook-server-name, --tls-cert-dir,
	// bind addresses, feature toggles, and --cache-label-selector).
	// Use for optional webhook-server flags such as --cert-reload-interval
	// or zap logging options. Admission and reconcile reject ExtraArgs that
	// name those managed flags (--flag=value or --flag value); overriding
	// them would break identity, TLS, feature wiring, or cache scoping.
	// Webhook-server pods are configured via this CR (not Helm
	// conversionWebhookServer.* Deployment keys).
	// +optional
	ExtraArgs []string `json:"extraArgs,omitempty"`

	// ExtraEnv is appended to the webhook-server container environment.
	// +optional
	ExtraEnv []corev1.EnvVar `json:"extraEnv,omitempty"`

	// ExtraVolumes is appended after the operator-managed tls and tmp volumes.
	// +optional
	ExtraVolumes []corev1.Volume `json:"extraVolumes,omitempty"`

	// ExtraVolumeMounts is appended after the operator-managed tls and tmp mounts.
	// +optional
	ExtraVolumeMounts []corev1.VolumeMount `json:"extraVolumeMounts,omitempty"`

	// TopologySpreadConstraints is applied to this instance's pods.
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// PodLabels is merged onto the webhook-server pod template. Keys that
	// the controller uses for the Deployment selector
	// (app.kubernetes.io/name, instance, managed-by) are ignored so a
	// mis-set label cannot break rolling updates.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// PodAnnotations is set on the webhook-server pod template.
	// +optional
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`

	// CacheSelector, when set, is passed to webhook-server replicas as
	// --cache-label-selector and scopes their informer caches for
	// XRDConversionConfig and CRDConversionConfig to matching objects.
	// Unset watches every config (the default). Use this for multi-tenant
	// or very-large-cluster deployments where a CWS instance should only
	// compile a labeled subset.
	// +optional
	CacheSelector *metav1.LabelSelector `json:"cacheSelector,omitempty"`

	// StartupProbe bounds how long a replica may take to compile every
	// assigned plan before the kubelet restarts it. See StartupProbeSpec:
	// without one, the liveness probe's 30 s is the whole cold-start
	// budget.
	// +optional
	StartupProbe *StartupProbeSpec `json:"startupProbe,omitempty"`

	// Rollout controls how a replica leaves service during a rolling
	// update or a node drain. The defaults are chosen so that a rollout
	// causes zero failed conversions; see RolloutSpec.
	// +optional
	Rollout *RolloutSpec `json:"rollout,omitempty"`

	Certificate CertificateSpec `json:"certificate"`
	// +optional
	Service ServiceSpec `json:"service,omitempty"`
	// +optional
	PodDisruptionBudget *PodDisruptionBudgetSpec `json:"podDisruptionBudget,omitempty"`
}

// RolloutSpec is the set of knobs that decide whether a rolling update of
// the webhook-server is invisible or breaks every write to every target it
// serves.
//
// A conversion webhook is on the apiserver's admission path, so a replica
// that stops listening before the apiserver stops being told about it
// produces connection-refused errors on writes that have nothing to do with
// the deployment. The race is between Endpoints propagation (kube-proxy,
// EndpointSlice controller, and the apiserver's own resolution) and the
// container's exit — and the container always wins unless something makes
// it wait.
//
// The four values here are one set, not four independent knobs:
//
//	preStopSleepSeconds (5)                          — wait for Endpoints removal
//	  + the webhook-server's graceful shutdown (30s)  — finish in-flight reviews
//	  < terminationGracePeriodSeconds (45)            — or the kubelet SIGKILLs mid-review
//
// Changing one without the others is how a rollout that looked safe starts
// dropping requests. The validating webhook rejects a combination that does
// not satisfy the inequality above.
type RolloutSpec struct {
	// PreStopSleepSeconds is how long the container sleeps after receiving
	// the termination signal before the process is asked to stop, giving
	// Endpoints removal time to reach every apiserver. Set to 0 to disable
	// the hook entirely — only correct if something else in the cluster
	// already guarantees the ordering.
	// +optional
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=300
	PreStopSleepSeconds *int32 `json:"preStopSleepSeconds,omitempty"`

	// TerminationGracePeriodSeconds must exceed PreStopSleepSeconds plus
	// the webhook-server's own shutdown timeout, or the kubelet sends
	// SIGKILL while a ConversionReview is still being answered — which the
	// apiserver reports as a failed write.
	// +optional
	// +kubebuilder:default=45
	// +kubebuilder:validation:Minimum=1
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`

	// MaxUnavailable is the Deployment's rolling-update maxUnavailable.
	// The default of 0 is deliberate and stricter than Kubernetes' own
	// 25%: with a 2-replica default, 25% rounds down to 0 anyway, but an
	// operator who scales to 4 would silently start taking a replica out
	// of service ahead of its replacement being ready.
	// +optional
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`

	// MaxSurge is the Deployment's rolling-update maxSurge. Defaults to 1:
	// with MaxUnavailable at 0, a surge of at least 1 is required or the
	// rollout cannot make progress at all.
	// +optional
	MaxSurge *intstr.IntOrString `json:"maxSurge,omitempty"`

	// DefaultTopologySpread, when true (the default), adds a soft
	// (ScheduleAnyway) spread constraint across kubernetes.io/hostname so
	// replicas do not all land on one node — which would make a single
	// node drain a full conversion outage.
	//
	// Soft rather than DoNotSchedule on purpose: a hard constraint turns a
	// single-node cluster (kind, a small edge cluster, a cordoned
	// majority) into an unschedulable Deployment, and an outage caused by
	// the anti-outage setting is the worse failure. Set to false to manage
	// spreading entirely through TopologySpreadConstraints or Affinity.
	// +optional
	// +kubebuilder:default=true
	DefaultTopologySpread *bool `json:"defaultTopologySpread,omitempty"`
}

// StartupProbeSpec configures the webhook-server's startupProbe: the
// budget a replica gets to finish its cold start before the kubelet gives
// up on it.
//
// A replica does not listen on any port until its registry has compiled
// every assigned plan, so until then the liveness and readiness probes
// both fail with connection-refused. Without a startupProbe the liveness
// probe's own 3 × 10 s is therefore the entire cold-start budget, and a
// replica holding enough targets to exceed it is killed and restarted
// forever — the slower the cold start, the more certainly it never
// finishes one. A startupProbe suspends the other two until it succeeds,
// which is exactly the semantics wanted here.
//
// PeriodSeconds × FailureThreshold is the budget. The defaults give five
// minutes, against a measured cold start of well under a second for a
// thousand 50-leaf targets (see docs/operations/capacity.md) — the margin
// is for informer cache sync on a large cluster, which dominates and is
// not this operator's to control. Erring long is deliberate: an
// over-tight threshold turns a slow start into a crash loop, while an
// over-long one only delays the restart of a pod that is not taking
// traffic anyway.
type StartupProbeSpec struct {
	// Enabled turns the startupProbe off. Only correct if something else
	// guarantees the cold start fits inside the liveness budget.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// +optional
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	PeriodSeconds *int32 `json:"periodSeconds,omitempty"`

	// +optional
	// +kubebuilder:default=60
	// +kubebuilder:validation:Minimum=1
	FailureThreshold *int32 `json:"failureThreshold,omitempty"`
}

// Startup-probe defaults, mirrored from the kubebuilder markers on
// StartupProbeSpec so the controller can reason about the values an unset
// field will actually produce rather than about the literal nil.
const (
	DefaultStartupProbePeriodSeconds    int32 = 5
	DefaultStartupProbeFailureThreshold int32 = 60
)

// StartupProbeEnabled reports whether the webhook-server's startupProbe
// should be rendered, with the same default the CRD carries.
func (s *ConversionWebhookServerSpec) StartupProbeEnabled() bool {
	if s.StartupProbe == nil || s.StartupProbe.Enabled == nil {
		return true
	}
	return *s.StartupProbe.Enabled
}

// StartupProbeTiming returns the period and failure threshold the
// startupProbe should be rendered with. Their product is the cold-start
// budget.
func (s *ConversionWebhookServerSpec) StartupProbeTiming() (periodSeconds, failureThreshold int32) {
	periodSeconds, failureThreshold = DefaultStartupProbePeriodSeconds, DefaultStartupProbeFailureThreshold
	if s.StartupProbe == nil {
		return periodSeconds, failureThreshold
	}
	if s.StartupProbe.PeriodSeconds != nil {
		periodSeconds = *s.StartupProbe.PeriodSeconds
	}
	if s.StartupProbe.FailureThreshold != nil {
		failureThreshold = *s.StartupProbe.FailureThreshold
	}
	return periodSeconds, failureThreshold
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
