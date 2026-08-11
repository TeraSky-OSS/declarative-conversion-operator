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

package engine

// Strategy names one of the engine's built-in conversion strategies.
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

// UnmappedFieldPolicy controls what happens when a field exists in a hub or
// spoke schema but no rule (and no identical-shape auto-match) accounts for
// it. The engine defaults to Error — "unknown means assume lossy, never
// silently pass" — matching the conservative-by-default requirement.
type UnmappedFieldPolicy string

const (
	UnmappedFieldPolicyError UnmappedFieldPolicy = "Error"
	UnmappedFieldPolicyWarn  UnmappedFieldPolicy = "Warn"
)

// UnknownKeyPolicy controls behavior when a map-like field (FieldsToMap /
// MapToFields) contains a key the rule didn't declare.
type UnknownKeyPolicy string

const (
	// UnknownKeyError means an unexpected key must cause the conversion to
	// fail loudly at runtime rather than silently drop data.
	UnknownKeyError UnknownKeyPolicy = "Error"
	// UnknownKeyDrop means an unexpected key is silently dropped; this is
	// always treated as lossy and requires AcknowledgeLossy.
	UnknownKeyDrop UnknownKeyPolicy = "Drop"
)

// Rule is one declarative conversion rule between a hub and a spoke schema.
// Exactly one of the strategy-specific Params fields (reached via Params)
// is populated, matching the Strategy field.
type Rule struct {
	Strategy Strategy
	Params   RuleParams

	// AcknowledgeLossy must be true if this rule is lossy in any direction,
	// or Compile reports a validation error (fail-closed default posture).
	AcknowledgeLossy bool
	Reason           string

	// SourceIndex back-references the originating api/v1alpha1 rule index,
	// so status/report entries can be correlated to spec.spokes[i].rules[j].
	SourceIndex int
}

// RuleParams is implemented by every strategy's parameter struct.
type RuleParams interface{ isRuleParams() }

type FieldRenameParams struct {
	HubPath, SpokePath FieldPath
}

func (FieldRenameParams) isRuleParams() {}

// ScalarToObjectParams: the hub field is a scalar; the spoke field is an
// object wrapping that scalar under Key. DefaultsForOtherKeys fills any
// other declared keys of the spoke object when converting hub->spoke.
type ScalarToObjectParams struct {
	HubPath, SpokePath   FieldPath
	Key                  string
	DefaultsForOtherKeys map[string]any
}

func (ScalarToObjectParams) isRuleParams() {}

// ObjectToScalarParams: the hub field is an object; the spoke field is a
// scalar extracted from the hub object's Key. DefaultsForOtherKeys fills any
// other keys of the hub object when converting spoke->hub.
type ObjectToScalarParams struct {
	HubPath, SpokePath   FieldPath
	Key                  string
	DefaultsForOtherKeys map[string]any
}

func (ObjectToScalarParams) isRuleParams() {}

// SingletonArrayToObjectParams: the hub field is an array; the spoke field
// is an object taken from the array's first element. The array->object
// direction is lossy whenever the array could hold more than one element.
type SingletonArrayToObjectParams struct {
	HubPath, SpokePath FieldPath
}

func (SingletonArrayToObjectParams) isRuleParams() {}

// ObjectToSingletonArrayParams: the hub field is an object; the spoke field
// is a single-element array wrapping it.
type ObjectToSingletonArrayParams struct {
	HubPath, SpokePath FieldPath
}

func (ObjectToSingletonArrayParams) isRuleParams() {}

// FieldsToMapParams: the hub has several sibling fields; the spoke
// aggregates them into a single map keyed by KeyNames (falling back to the
// field's own last path segment).
type FieldsToMapParams struct {
	HubPaths          []FieldPath
	SpokeMapPath      FieldPath
	KeyNames          map[string]string // hubPath.String() -> map key
	OnUnknownSpokeKey UnknownKeyPolicy  // default Error
}

func (FieldsToMapParams) isRuleParams() {}

// MapToFieldsParams is the structural mirror of FieldsToMapParams: the hub
// has a single map field; the spoke has several sibling fields.
type MapToFieldsParams struct {
	HubMapPath      FieldPath
	SpokePaths      []FieldPath
	KeyNames        map[string]string // spokePath.String() -> map key
	OnUnknownHubKey UnknownKeyPolicy  // default Error
}

func (MapToFieldsParams) isRuleParams() {}

// ToMetadataParams backs both ToAnnotation and ToLabel: the hub field's
// value is stashed into a metadata annotation/label when converting to the
// spoke (which lacks the field), and restored from it when converting back
// to the hub, if RestoreOnReverse is set.
type ToMetadataParams struct {
	HubPath          FieldPath
	Key              string
	Serialization    string // "JSON" (default) | "String"
	RestoreOnReverse bool
}

func (ToMetadataParams) isRuleParams() {}

// EnumRemapParams bidirectionally maps a scalar field's enumerated values
// between hub and spoke vocabularies.
type EnumRemapParams struct {
	Path                 FieldPath // same path on both sides
	Mapping              []EnumValueMapping
	OnUnmappedHubValue   UnknownKeyPolicy
	OnUnmappedSpokeValue UnknownKeyPolicy
}

func (EnumRemapParams) isRuleParams() {}

type EnumValueMapping struct {
	Hub, Spoke string
}

// DefaultValueParams injects a default for a field that exists only in one
// of the two schemas (typically new in a later version) when converting
// into that schema, and drops it (lossy, needs acknowledgement) going back.
type DefaultValueParams struct {
	Path     FieldPath
	ExistsOn Side // Hub or Spoke: which schema actually declares this field
	Default  any
}

func (DefaultValueParams) isRuleParams() {}

// Side identifies which of the two schemas in a hub<->spoke pair a
// DefaultValueParams/DeleteParams field belongs to.
type Side string

const (
	SideHub   Side = "Hub"
	SideSpoke Side = "Spoke"
)

// ConstantParams forces a field to a fixed value on one direction.
type ConstantParams struct {
	Path     FieldPath
	ExistsOn Side
	Value    any
}

func (ConstantParams) isRuleParams() {}

// DeleteParams intentionally drops a field that exists on only one side.
// Always lossy; always requires AcknowledgeLossy.
type DeleteParams struct {
	Path     FieldPath
	ExistsOn Side
}

func (DeleteParams) isRuleParams() {}

// JSONPatchParams is the escape hatch: raw RFC 6902 JSON Patch documents
// applied per direction. The engine cannot statically reason about what an
// arbitrary patch does, so it is always treated as lossy unless the author
// explicitly asserts LosslessOverride.
type JSONPatchParams struct {
	HubToSpoke       []JSONPatchOp
	SpokeToHub       []JSONPatchOp
	LosslessOverride bool
}

func (JSONPatchParams) isRuleParams() {}

type JSONPatchOp struct {
	Op    string // add|remove|replace|move|copy|test
	Path  string
	From  string
	Value any
}

// ForEachParams applies a nested rule set to each element of a hub array and
// a corresponding spoke array. Nested rule paths are relative to a single
// array element. Requires strict positional (same length/order)
// correspondence between the two arrays at conversion time.
type ForEachParams struct {
	HubItemsPath, SpokeItemsPath FieldPath
	Rules                        []Rule
}

func (ForEachParams) isRuleParams() {}

// TypeCoerceParams converts a scalar field's JSON type (string, integer,
// number, or boolean) between whatever the hub and spoke schemas each
// declare at Path — e.g. a field that was a string in one version and
// becomes an integer in another. Both directions are always treated as
// lossless: canonically-formatted values round-trip exactly, though a
// malformed value (a non-numeric string being coerced to a number, say)
// is a runtime conversion error rather than a lossiness concern.
type TypeCoerceParams struct {
	Path FieldPath // same path on both sides
}

func (TypeCoerceParams) isRuleParams() {}

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
	HubPath          FieldPath
	Pattern          string
	SpokeFields      map[string]FieldPath // capture group name -> spoke path
	JoinTemplate     string
	LosslessOverride bool
}

func (ScalarToFieldsParams) isRuleParams() {}

// FieldsToScalarParams is the structural mirror of ScalarToFieldsParams:
// several hub sibling fields are joined into a single spoke scalar via
// JoinTemplate, and split back into the hub fields via Pattern.
type FieldsToScalarParams struct {
	HubFields        map[string]FieldPath // capture group name -> hub path
	Pattern          string
	SpokePath        FieldPath
	JoinTemplate     string
	LosslessOverride bool
}

func (FieldsToScalarParams) isRuleParams() {}

// ArrayToMapByKeyParams: the hub field is an array of objects; the spoke
// field is a map from each object's KeyField value to the rest of that
// object (KeyField itself becomes the map key and is not duplicated into
// the value). This is the common "list-map" versus "map" API-evolution
// pattern. Array->map is lossless provided every element has a unique,
// present KeyField value (a duplicate or missing key is a runtime error,
// not silent data loss); map->array is always treated as lossy, since Go
// map iteration has no stable order and the reconstructed array is
// emitted sorted by key, which may not match whatever order the original
// array had.
type ArrayToMapByKeyParams struct {
	HubPath, SpokePath FieldPath
	KeyField           string
}

func (ArrayToMapByKeyParams) isRuleParams() {}

// MapToArrayByKeyParams is the structural mirror of ArrayToMapByKeyParams:
// the hub field is the map, the spoke field is the array.
type MapToArrayByKeyParams struct {
	HubPath, SpokePath FieldPath
	KeyField           string
}

func (MapToArrayByKeyParams) isRuleParams() {}

// NumericScaleParams rescales a numeric field by a fixed factor between
// hub and spoke (hubValue == spokeValue * Factor) — e.g. converting
// stored megabytes to a displayed gigabyte field. HubPath and SpokePath
// may be equal if only the scale changes, or different if the field is
// renamed at the same time. A direction that lands on a schema field
// declared as an integer is treated as lossy (the division/multiplication
// may not land on a whole number for every possible input), even though
// any individual sample value might happen to divide evenly.
type NumericScaleParams struct {
	HubPath, SpokePath FieldPath
	Factor             float64
}

func (NumericScaleParams) isRuleParams() {}

// ListJoinParams: the hub field is an array of scalars; the spoke field is
// a single string produced by joining the array's (string-coerced)
// elements with Separator, and split back into an array of the hub item
// type on the reverse direction. Always treated as lossless — an element
// that happens to contain Separator as a substring will fail to
// round-trip and is correctly surfaced as an unacknowledged loss by
// xrdconvctl test, since that is a genuine data problem, not an expected
// characteristic of this strategy.
type ListJoinParams struct {
	HubPath, SpokePath FieldPath
	Separator          string
}

func (ListJoinParams) isRuleParams() {}

// ListSplitParams is the structural mirror of ListJoinParams: the hub
// field is the delimited string, the spoke field is the array.
type ListSplitParams struct {
	HubPath, SpokePath FieldPath
	Separator          string
}

func (ListSplitParams) isRuleParams() {}

// RuleSet is every rule declared for one hub<->spoke version pair.
type RuleSet struct {
	HubVersion, SpokeVersion string
	Rules                    []Rule
	UnmappedFieldPolicy      UnmappedFieldPolicy
}
