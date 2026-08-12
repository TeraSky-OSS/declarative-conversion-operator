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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CRDConversionConfig is the native-Kubernetes-CRD counterpart of
// XRDConversionConfig: same Strategy/ConversionRule/SpokeVersionRules
// vocabulary (deliberately reused verbatim, not duplicated — a rule that
// renames a field means the same thing regardless of which kind of
// resource it's converting), targeting a plain apiextensions.k8s.io/v1
// CustomResourceDefinition instead of a Crossplane
// CompositeResourceDefinition. See pkg/crdadapter for the schema source
// this reads from, and internal/controller/crdconversionconfig_controller.go
// for the reconciler, which follows the exact same validate -> resolve ->
// health-gate -> patch ordering as XRDConversionConfig's.

// TargetCRDRef identifies the CustomResourceDefinition this config applies
// to, by its metadata.name (always "<plural>.<group>", the same convention
// XRDs use).
type TargetCRDRef struct {
	Name string `json:"name"`
}

// CRDConversionConfigSpec defines the desired conversion configuration for
// one target CustomResourceDefinition.
type CRDConversionConfigSpec struct {
	TargetCRD  TargetCRDRef `json:"targetCRD"`
	HubVersion string       `json:"hubVersion"`
	// +kubebuilder:validation:MinItems=1
	Spokes []SpokeVersionRules `json:"spokes"`

	// +optional
	WebhookServerRef *WebhookServerRef `json:"webhookServerRef,omitempty"`

	// +optional
	// +kubebuilder:default={"v1"}
	ConversionReviewVersions []string `json:"conversionReviewVersions,omitempty"`

	// +optional
	// +kubebuilder:default=Error
	UnmappedFieldPolicy UnmappedFieldPolicy `json:"unmappedFieldPolicy,omitempty"`
	// UnmappedFieldReason is a human-readable justification for leaving
	// one or more hub/spoke fields unclaimed. Required (and enforced by
	// the admission webhook and convctl validate) when UnmappedFieldPolicy
	// is Warn; ignored when the policy is Error (the default).
	// +optional
	UnmappedFieldReason string `json:"unmappedFieldReason,omitempty"`

	// +optional
	// +kubebuilder:default=KeepServingStale
	DriftPolicy DriftPolicy `json:"driftPolicy,omitempty"`
}

// CRDConversionConfigStatus is the observed state of a CRDConversionConfig.
// Structurally identical to XRDConversionConfigStatus (see there for field
// documentation); kept as a separate type rather than a shared one so each
// CRD's status subresource schema stays independently evolvable.
type CRDConversionConfigStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	ObservedCRDGeneration int64 `json:"observedCRDGeneration,omitempty"`
	// +optional
	SchemaHash string `json:"schemaHash,omitempty"`
	// +kubebuilder:validation:Enum=Pending;Validating;Validated;Invalid;Applied;Stale;Reverting;Failed
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	// +optional
	AssignedWebhookServer string `json:"assignedWebhookServer,omitempty"`
	// +optional
	WebhookPath string `json:"webhookPath,omitempty"`
	// +optional
	WebhookURL string `json:"webhookURL,omitempty"`
	// +optional
	OverallLossless bool `json:"overallLossless,omitempty"`
	// +optional
	SpokeStatuses []SpokeConversionStatus `json:"spokeStatuses,omitempty"`
	// +optional
	LastAppliedPlanHash string `json:"lastAppliedPlanHash,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// WebhookServerRefField implements internal/assign's generic ConfigLike
// constraint — see XRDConversionConfig.WebhookServerRefField.
func (c *CRDConversionConfig) WebhookServerRefField() *WebhookServerRef {
	return c.Spec.WebhookServerRef
}

// ConditionCRDHealthy mirrors ConditionXRDHealthy for the native-CRD
// target: True once the target CustomResourceDefinition exists and its
// own Established condition is True.
const ConditionCRDHealthy = "CRDHealthy"

// Finalizer used to run the safe-revert flow before a CRDConversionConfig
// is actually deleted. Distinct from XRDConversionConfigFinalizer purely
// so `kubectl describe` output is unambiguous about which controller owns
// a given finalizer entry — finalizers are scoped per-object regardless.
const CRDConversionConfigFinalizer = "conversion.terasky.com/safe-revert-crd"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="CRD",type=string,JSONPath=".spec.targetCRD.name"
// +kubebuilder:printcolumn:name="Hub",type=string,JSONPath=".spec.hubVersion"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Lossless",type=boolean,JSONPath=".status.overallLossless"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// CRDConversionConfig declares how to convert a plain Kubernetes
// CustomResourceDefinition's spoke versions to and from its hub version
// using the same built-in declarative strategies XRDConversionConfig uses.
type CRDConversionConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CRDConversionConfigSpec   `json:"spec,omitempty"`
	Status CRDConversionConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CRDConversionConfigList contains a list of CRDConversionConfig.
type CRDConversionConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CRDConversionConfig `json:"items"`
}
