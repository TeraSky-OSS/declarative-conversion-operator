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
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// UnmappedFieldPolicy controls what happens when a hub or spoke field is
// left unclaimed by any rule and has no identical counterpart on the other
// side. The conservative default (Error) means "unknown means assume
// lossy, never silently pass."
// +kubebuilder:validation:Enum=Error;Warn
type UnmappedFieldPolicy string

const (
	UnmappedFieldPolicyError UnmappedFieldPolicy = "Error"
	UnmappedFieldPolicyWarn  UnmappedFieldPolicy = "Warn"
)

// DriftPolicy controls what happens when the live XRD's schema no longer
// matches what an already-Applied XRDConversionConfig last validated
// against.
// +kubebuilder:validation:Enum=KeepServingStale;FailClosed
type DriftPolicy string

const (
	// DriftPolicyKeepServingStale keeps serving the last known-good
	// compiled plan and marks the config Stale, rather than risking an
	// outage by unpatching a working conversion webhook. This is the
	// conservative default.
	DriftPolicyKeepServingStale DriftPolicy = "KeepServingStale"
	// DriftPolicyFailClosed stops serving conversions for the XRD as soon
	// as drift is detected, for organizations that prefer to fail loud
	// immediately over serving a plan validated against a stale schema.
	DriftPolicyFailClosed DriftPolicy = "FailClosed"
)

// UnknownKeyPolicy controls behavior when a map-like field (FieldsToMap /
// MapToFields) contains a key a rule didn't declare.
// +kubebuilder:validation:Enum=Error;Drop
type UnknownKeyPolicy string

const (
	UnknownKeyPolicyError UnknownKeyPolicy = "Error"
	UnknownKeyPolicyDrop  UnknownKeyPolicy = "Drop"
)

// Side identifies which of the two schemas in a hub<->spoke pair a field
// belongs to.
// +kubebuilder:validation:Enum=Hub;Spoke
type Side string

const (
	SideHub   Side = "Hub"
	SideSpoke Side = "Spoke"
)

// Strategy names one of the engine's built-in conversion strategies.
// +kubebuilder:validation:Enum=FieldRename;ScalarToObject;ObjectToScalar;SingletonArrayToObject;ObjectToSingletonArray;FieldsToMap;MapToFields;ToAnnotation;ToLabel;FromAnnotation;FromLabel;EnumRemap;DefaultValue;Constant;Delete;JSONPatch;ForEach;TypeCoerce;ScalarToFields;FieldsToScalar;ArrayToMapByKey;MapToArrayByKey;NumericScale;ListJoin;ListSplit
type Strategy string

const (
	StrategyFieldRename            Strategy = "FieldRename"
	StrategyScalarToObject         Strategy = "ScalarToObject"
	StrategyObjectToScalar         Strategy = "ObjectToScalar"
	StrategySingletonArrayToObject Strategy = "SingletonArrayToObject"
	StrategyObjectToSingletonArray Strategy = "ObjectToSingletonArray"
	StrategyFieldsToMap            Strategy = "FieldsToMap"
	StrategyMapToFields            Strategy = "MapToFields"
	StrategyToAnnotation           Strategy = "ToAnnotation"
	StrategyToLabel                Strategy = "ToLabel"
	StrategyFromAnnotation         Strategy = "FromAnnotation"
	StrategyFromLabel              Strategy = "FromLabel"
	StrategyEnumRemap              Strategy = "EnumRemap"
	StrategyDefaultValue           Strategy = "DefaultValue"
	StrategyConstant               Strategy = "Constant"
	StrategyDelete                 Strategy = "Delete"
	StrategyJSONPatch              Strategy = "JSONPatch"
	StrategyForEach                Strategy = "ForEach"
	StrategyTypeCoerce             Strategy = "TypeCoerce"
	StrategyScalarToFields         Strategy = "ScalarToFields"
	StrategyFieldsToScalar         Strategy = "FieldsToScalar"
	StrategyArrayToMapByKey        Strategy = "ArrayToMapByKey"
	StrategyMapToArrayByKey        Strategy = "MapToArrayByKey"
	StrategyNumericScale           Strategy = "NumericScale"
	StrategyListJoin               Strategy = "ListJoin"
	StrategyListSplit              Strategy = "ListSplit"
)

// TargetXRDRef identifies the Crossplane CompositeResourceDefinition this
// config applies to, by its metadata.name (which for XRDs is always
// "<plural>.<group>" — the same uniqueness guarantee native CRDs offer).
type TargetXRDRef struct {
	Name string `json:"name"`
}

// WebhookServerRef names a ConversionWebhookServer instance to serve this
// config's conversions. Omit to use whichever instance is marked default.
type WebhookServerRef struct {
	Name string `json:"name"`
}

// FieldRenameParams renames a field, preserving its value and shape.
type FieldRenameParams struct {
	HubPath   string `json:"hubPath"`
	SpokePath string `json:"spokePath"`
}

// ScalarToObjectParams: the hub field is a scalar; the spoke field is an
// object wrapping that scalar under Key.
type ScalarToObjectParams struct {
	HubPath   string `json:"hubPath"`
	SpokePath string `json:"spokePath"`
	Key       string `json:"key"`
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	DefaultsForOtherKeys map[string]extv1.JSON `json:"defaultsForOtherKeys,omitempty"`
}

// ObjectToScalarParams: the hub field is an object; the spoke field is a
// scalar extracted from the hub object's Key.
type ObjectToScalarParams struct {
	HubPath   string `json:"hubPath"`
	SpokePath string `json:"spokePath"`
	Key       string `json:"key"`
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	DefaultsForOtherKeys map[string]extv1.JSON `json:"defaultsForOtherKeys,omitempty"`
}

// SingletonArrayToObjectParams: the hub field is an array; the spoke field
// is an object taken from the array's first element.
type SingletonArrayToObjectParams struct {
	HubPath   string `json:"hubPath"`
	SpokePath string `json:"spokePath"`
}

// ObjectToSingletonArrayParams: the hub field is an object; the spoke field
// is a single-element array wrapping it.
type ObjectToSingletonArrayParams struct {
	HubPath   string `json:"hubPath"`
	SpokePath string `json:"spokePath"`
}

// FieldsToMapParams: the hub has several sibling fields; the spoke
// aggregates them into a single map.
type FieldsToMapParams struct {
	HubPaths     []string `json:"hubPaths"`
	SpokeMapPath string   `json:"spokeMapPath"`
	// +optional
	KeyNames map[string]string `json:"keyNames,omitempty"`
	// +optional
	// +kubebuilder:default=Error
	OnUnknownSpokeKey UnknownKeyPolicy `json:"onUnknownSpokeKey,omitempty"`
}

// MapToFieldsParams is the structural mirror of FieldsToMapParams.
type MapToFieldsParams struct {
	HubMapPath string   `json:"hubMapPath"`
	SpokePaths []string `json:"spokePaths"`
	// +optional
	KeyNames map[string]string `json:"keyNames,omitempty"`
	// +optional
	// +kubebuilder:default=Error
	OnUnknownHubKey UnknownKeyPolicy `json:"onUnknownHubKey,omitempty"`
}

// ToMetadataParams backs both ToAnnotation and ToLabel.
type ToMetadataParams struct {
	HubPath string `json:"hubPath"`
	Key     string `json:"key"`
	// +optional
	// +kubebuilder:validation:Enum=JSON;String
	// +kubebuilder:default=JSON
	Serialization string `json:"serialization,omitempty"`
	// +optional
	RestoreOnReverse bool `json:"restoreOnReverse,omitempty"`
}

// FromMetadataParams backs both FromAnnotation and FromLabel — the inverse
// geometry of ToMetadataParams. The schema field lives on the spoke; hub
// metadata holds the stash key.
//
// For FromLabel, serialization must be String (JSON produces quoted values
// that are not valid Kubernetes label values). The admission webhook and
// engine reject serialization=JSON for FromLabel; the default when unset is
// String. FromAnnotation still defaults to JSON.
type FromMetadataParams struct {
	SpokePath string `json:"spokePath"`
	Key       string `json:"key"`
	// +optional
	// +kubebuilder:validation:Enum=JSON;String
	// +kubebuilder:default=JSON
	Serialization string `json:"serialization,omitempty"`
	// +optional
	StashOnReverse bool `json:"stashOnReverse,omitempty"`
}

// EnumValueMapping pairs one hub enum value with its spoke equivalent.
type EnumValueMapping struct {
	Hub   string `json:"hub"`
	Spoke string `json:"spoke"`
}

// EnumRemapParams bidirectionally maps a scalar field's enumerated values.
type EnumRemapParams struct {
	Path    string             `json:"path"`
	Mapping []EnumValueMapping `json:"mapping"`
	// +optional
	// +kubebuilder:default=Error
	OnUnmappedHubValue UnknownKeyPolicy `json:"onUnmappedHubValue,omitempty"`
	// +optional
	// +kubebuilder:default=Error
	OnUnmappedSpokeValue UnknownKeyPolicy `json:"onUnmappedSpokeValue,omitempty"`
}

// DefaultValueParams injects a default for a field that exists only on one
// side (typically new in a later version) when converting into that side.
type DefaultValueParams struct {
	Path     string `json:"path"`
	ExistsOn Side   `json:"existsOn"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Default extv1.JSON `json:"default"`
}

// ConstantParams forces a field to a fixed value on the side it exists on.
type ConstantParams struct {
	Path     string `json:"path"`
	ExistsOn Side   `json:"existsOn"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Value extv1.JSON `json:"value"`
}

// DeleteParams intentionally drops a field that exists on only one side.
// Always lossy on the direction the field exists; always requires
// AcknowledgeLossy.
type DeleteParams struct {
	Path     string `json:"path"`
	ExistsOn Side   `json:"existsOn"`
}

// JSONPatchOp is one RFC 6902 JSON Patch operation.
type JSONPatchOp struct {
	// +kubebuilder:validation:Enum=add;remove;replace;move;copy;test
	Op   string `json:"op"`
	Path string `json:"path"`
	// +optional
	From string `json:"from,omitempty"`
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Value *extv1.JSON `json:"value,omitempty"`
}

// JSONPatchParams is the escape hatch: raw JSON Patch documents applied per
// direction. The engine cannot statically verify field coverage for these,
// so they are always treated as lossy unless LosslessOverride is set.
type JSONPatchParams struct {
	// +optional
	HubToSpoke []JSONPatchOp `json:"hubToSpoke,omitempty"`
	// +optional
	SpokeToHub []JSONPatchOp `json:"spokeToHub,omitempty"`
	// +optional
	LosslessOverride bool `json:"losslessOverride,omitempty"`
}

// ForEachParams applies a nested rule list to each element of a hub array
// and the corresponding spoke array. Nested rule paths are relative to a
// single array element. Nesting is capped at depth 1.
type ForEachParams struct {
	HubItemsPath   string `json:"hubItemsPath"`
	SpokeItemsPath string `json:"spokeItemsPath"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Rules []ConversionRule `json:"rules"`
}

// TypeCoerceParams converts a scalar field's JSON type (string, integer,
// number, or boolean) between whatever the hub and spoke schemas each
// declare at Path — e.g. a field that was a string in one version and
// becomes an integer in another.
type TypeCoerceParams struct {
	Path string `json:"path"`
}

// ScalarToFieldsParams: the hub field is a single scalar string; Pattern
// (a regexp with named capture groups) decomposes it into SpokeFields
// (capture group name -> spoke path), each coerced to that field's
// declared schema type. JoinTemplate (a Go text/template referencing the
// same group names, e.g. "{{.value}}{{.unit}}") reassembles the spoke
// fields back into the hub scalar for the reverse direction. The engine
// cannot verify that Pattern and JoinTemplate are true inverses of each
// other, so this is always treated as lossy unless LosslessOverride is
// set.
type ScalarToFieldsParams struct {
	HubPath string `json:"hubPath"`
	// Pattern is a regexp with named capture groups, e.g.
	// "^(?P<value>\\d+)(?P<unit>[A-Za-z]+)$".
	Pattern string `json:"pattern"`
	// SpokeFields maps each named capture group to the spoke path it fills.
	SpokeFields map[string]string `json:"spokeFields"`
	// JoinTemplate is a Go text/template referencing the same group names
	// as Pattern, e.g. "{{.value}}{{.unit}}".
	JoinTemplate string `json:"joinTemplate"`
	// +optional
	LosslessOverride bool `json:"losslessOverride,omitempty"`
}

// FieldsToScalarParams is the structural mirror of ScalarToFieldsParams:
// several hub sibling fields are joined into a single spoke scalar via
// JoinTemplate, and split back into the hub fields via Pattern.
type FieldsToScalarParams struct {
	// HubFields maps each named capture group to the hub path it reads from.
	HubFields    map[string]string `json:"hubFields"`
	Pattern      string            `json:"pattern"`
	SpokePath    string            `json:"spokePath"`
	JoinTemplate string            `json:"joinTemplate"`
	// +optional
	LosslessOverride bool `json:"losslessOverride,omitempty"`
}

// ArrayToMapByKeyParams: the hub field is an array of objects; the spoke
// field is a map from each object's KeyField value to the rest of that
// object (KeyField itself becomes the map key and is not duplicated into
// the value) — the common "list-map" versus "map" API-evolution pattern.
type ArrayToMapByKeyParams struct {
	HubPath   string `json:"hubPath"`
	SpokePath string `json:"spokePath"`
	KeyField  string `json:"keyField"`
}

// MapToArrayByKeyParams is the structural mirror of ArrayToMapByKeyParams:
// the hub field is the map, the spoke field is the array.
type MapToArrayByKeyParams struct {
	HubPath   string `json:"hubPath"`
	SpokePath string `json:"spokePath"`
	KeyField  string `json:"keyField"`
}

// NumericScaleParams rescales a numeric field by a fixed factor between
// hub and spoke (hubValue == spokeValue * Factor) — e.g. converting
// stored megabytes to a displayed gigabyte field.
type NumericScaleParams struct {
	HubPath   string  `json:"hubPath"`
	SpokePath string  `json:"spokePath"`
	Factor    float64 `json:"factor"`
}

// ListJoinParams: the hub field is an array of scalars; the spoke field is
// a single string produced by joining the array's (string-coerced)
// elements with Separator, and split back into an array on the reverse
// direction.
type ListJoinParams struct {
	HubPath   string `json:"hubPath"`
	SpokePath string `json:"spokePath"`
	Separator string `json:"separator"`
}

// ListSplitParams is the structural mirror of ListJoinParams: the hub
// field is the delimited string, the spoke field is the array.
type ListSplitParams struct {
	HubPath   string `json:"hubPath"`
	SpokePath string `json:"spokePath"`
	Separator string `json:"separator"`
}

// ConversionRule is one declarative conversion rule between the hub version
// and a spoke version. Exactly one of the strategy-specific fields below
// should be set, matching Strategy.
type ConversionRule struct {
	Strategy Strategy `json:"strategy"`

	// +optional
	FieldRename *FieldRenameParams `json:"fieldRename,omitempty"`
	// +optional
	ScalarToObject *ScalarToObjectParams `json:"scalarToObject,omitempty"`
	// +optional
	ObjectToScalar *ObjectToScalarParams `json:"objectToScalar,omitempty"`
	// +optional
	SingletonArrayToObject *SingletonArrayToObjectParams `json:"singletonArrayToObject,omitempty"`
	// +optional
	ObjectToSingletonArray *ObjectToSingletonArrayParams `json:"objectToSingletonArray,omitempty"`
	// +optional
	FieldsToMap *FieldsToMapParams `json:"fieldsToMap,omitempty"`
	// +optional
	MapToFields *MapToFieldsParams `json:"mapToFields,omitempty"`
	// +optional
	ToAnnotation *ToMetadataParams `json:"toAnnotation,omitempty"`
	// +optional
	ToLabel *ToMetadataParams `json:"toLabel,omitempty"`
	// +optional
	FromAnnotation *FromMetadataParams `json:"fromAnnotation,omitempty"`
	// +optional
	FromLabel *FromMetadataParams `json:"fromLabel,omitempty"`
	// +optional
	EnumRemap *EnumRemapParams `json:"enumRemap,omitempty"`
	// +optional
	DefaultValue *DefaultValueParams `json:"defaultValue,omitempty"`
	// +optional
	Constant *ConstantParams `json:"constant,omitempty"`
	// +optional
	Delete *DeleteParams `json:"delete,omitempty"`
	// +optional
	JSONPatch *JSONPatchParams `json:"jsonPatch,omitempty"`
	// +optional
	ForEach *ForEachParams `json:"forEach,omitempty"`
	// +optional
	TypeCoerce *TypeCoerceParams `json:"typeCoerce,omitempty"`
	// +optional
	ScalarToFields *ScalarToFieldsParams `json:"scalarToFields,omitempty"`
	// +optional
	FieldsToScalar *FieldsToScalarParams `json:"fieldsToScalar,omitempty"`
	// +optional
	ArrayToMapByKey *ArrayToMapByKeyParams `json:"arrayToMapByKey,omitempty"`
	// +optional
	MapToArrayByKey *MapToArrayByKeyParams `json:"mapToArrayByKey,omitempty"`
	// +optional
	NumericScale *NumericScaleParams `json:"numericScale,omitempty"`
	// +optional
	ListJoin *ListJoinParams `json:"listJoin,omitempty"`
	// +optional
	ListSplit *ListSplitParams `json:"listSplit,omitempty"`

	// AcknowledgeLossy must be true if this rule is lossy in any
	// direction, or validation fails (fail-closed default posture).
	// +optional
	AcknowledgeLossy bool `json:"acknowledgeLossy,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
}

// WebhookServerRefField implements internal/assign's generic ConfigLike
// constraint, letting the shared resolver operate over both
// XRDConversionConfig and CRDConversionConfig without either type needing
// to know about the other.
func (c *XRDConversionConfig) WebhookServerRefField() *WebhookServerRef {
	return c.Spec.WebhookServerRef
}

// SpokeVersionRules is every rule declared for one spoke version.
type SpokeVersionRules struct {
	Version string           `json:"version"`
	Rules   []ConversionRule `json:"rules,omitempty"`
}

// XRDConversionConfigSpec defines the desired conversion configuration for
// one target Crossplane XRD.
type XRDConversionConfigSpec struct {
	TargetXRD  TargetXRDRef `json:"targetXRD"`
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

// LosslessVerdict records whether a mapping is lossless in each direction
// independently.
type LosslessVerdict struct {
	HubToSpoke bool `json:"hubToSpoke"`
	SpokeToHub bool `json:"spokeToHub"`
}

// RuleResult reports the outcome of resolving and analyzing a single rule.
type RuleResult struct {
	Index      int             `json:"index"`
	Strategy   Strategy        `json:"strategy"`
	HubPaths   []string        `json:"hubPaths,omitempty"`
	SpokePaths []string        `json:"spokePaths,omitempty"`
	Lossless   LosslessVerdict `json:"lossless"`
	// +optional
	Errors []string `json:"errors,omitempty"`
	// +optional
	Warnings []string `json:"warnings,omitempty"`
}

// SpokeConversionStatus is the analysis outcome for one spoke version.
type SpokeConversionStatus struct {
	Version  string          `json:"version"`
	Lossless LosslessVerdict `json:"lossless"`
	// +optional
	FieldsUncoveredHub []string `json:"fieldsUncoveredHub,omitempty"`
	// +optional
	FieldsUncoveredSpoke []string `json:"fieldsUncoveredSpoke,omitempty"`
	// +optional
	RuleResults []RuleResult `json:"ruleResults,omitempty"`
	// +optional
	Errors []string `json:"errors,omitempty"`
	// +optional
	Warnings []string `json:"warnings,omitempty"`
}

// XRDConversionConfigStatus is the observed state of an XRDConversionConfig.
type XRDConversionConfigStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	ObservedXRDGeneration int64 `json:"observedXRDGeneration,omitempty"`
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

// Condition type constants for XRDConversionConfig.
const (
	ConditionValidated          = "Validated"
	ConditionXRDHealthy         = "XRDHealthy"
	ConditionWebhookServerReady = "WebhookServerReady"
	ConditionApplied            = "Applied"
	ConditionStale              = "Stale"
	ConditionDeletionBlocked    = "DeletionBlocked"

	// ConditionApplied reasons used by FailClosed drift handling.
	ReasonReverted     = "Reverted"
	ReasonRevertFailed = "RevertFailed"
)

// Phase constants for XRDConversionConfigStatus.Phase.
const (
	PhasePending    = "Pending"
	PhaseValidating = "Validating"
	PhaseValidated  = "Validated"
	PhaseInvalid    = "Invalid"
	PhaseApplied    = "Applied"
	PhaseStale      = "Stale"
	PhaseReverting  = "Reverting"
	PhaseFailed     = "Failed"
)

// Finalizer used to run the safe-revert flow before an XRDConversionConfig
// is actually deleted.
const XRDConversionConfigFinalizer = "conversion.terasky.com/safe-revert"

// AllowUnsafeDeleteAnnotation is the explicit break-glass annotation that
// lets an admin delete an XRDConversionConfig even though the target XRD
// still has more than one served version (checked live at the moment of
// the delete reconcile, not historically set).
const AllowUnsafeDeleteAnnotation = "conversion.terasky.com/allow-unsafe-delete"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="XRD",type=string,JSONPath=".spec.targetXRD.name"
// +kubebuilder:printcolumn:name="Hub",type=string,JSONPath=".spec.hubVersion"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Lossless",type=boolean,JSONPath=".status.overallLossless"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// XRDConversionConfig declares how to convert a Crossplane
// CompositeResourceDefinition's spoke versions to and from its hub version
// using built-in declarative strategies.
type XRDConversionConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   XRDConversionConfigSpec   `json:"spec,omitempty"`
	Status XRDConversionConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// XRDConversionConfigList contains a list of XRDConversionConfig.
type XRDConversionConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []XRDConversionConfig `json:"items"`
}
