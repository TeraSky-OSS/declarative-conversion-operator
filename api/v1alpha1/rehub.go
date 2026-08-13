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
	"fmt"
	"sort"
)

// HubPathMap maps a path on the old hub to the corresponding path on the
// new hub, derived from the promoted spoke's rules (old hubPath → spokePath
// of the version that becomes the new hub).
type HubPathMap map[string]string

// InvertRule returns the hub-promotion inverse of a single ConversionRule.
// Paths and paired strategies are swapped so the rule is valid when the
// previous hub becomes a spoke of the new hub.
func InvertRule(r ConversionRule) (ConversionRule, error) {
	out := ConversionRule{
		AcknowledgeLossy: r.AcknowledgeLossy,
		Reason:           r.Reason,
		When:             r.When.DeepCopy(),
	}
	switch r.Strategy {
	case StrategyFieldRename:
		if r.FieldRename == nil {
			return out, fmt.Errorf("FieldRename: missing params")
		}
		out.Strategy = StrategyFieldRename
		out.FieldRename = &FieldRenameParams{HubPath: r.FieldRename.SpokePath, SpokePath: r.FieldRename.HubPath}
	case StrategyScalarToObject:
		if r.ScalarToObject == nil {
			return out, fmt.Errorf("ScalarToObject: missing params")
		}
		out.Strategy = StrategyObjectToScalar
		out.ObjectToScalar = &ObjectToScalarParams{
			HubPath: r.ScalarToObject.SpokePath, SpokePath: r.ScalarToObject.HubPath,
			Key: r.ScalarToObject.Key, DefaultsForOtherKeys: r.ScalarToObject.DefaultsForOtherKeys,
		}
	case StrategyObjectToScalar:
		if r.ObjectToScalar == nil {
			return out, fmt.Errorf("ObjectToScalar: missing params")
		}
		out.Strategy = StrategyScalarToObject
		out.ScalarToObject = &ScalarToObjectParams{
			HubPath: r.ObjectToScalar.SpokePath, SpokePath: r.ObjectToScalar.HubPath,
			Key: r.ObjectToScalar.Key, DefaultsForOtherKeys: r.ObjectToScalar.DefaultsForOtherKeys,
		}
	case StrategySingletonArrayToObject:
		if r.SingletonArrayToObject == nil {
			return out, fmt.Errorf("SingletonArrayToObject: missing params")
		}
		out.Strategy = StrategyObjectToSingletonArray
		out.ObjectToSingletonArray = &ObjectToSingletonArrayParams{
			HubPath: r.SingletonArrayToObject.SpokePath, SpokePath: r.SingletonArrayToObject.HubPath,
		}
	case StrategyObjectToSingletonArray:
		if r.ObjectToSingletonArray == nil {
			return out, fmt.Errorf("ObjectToSingletonArray: missing params")
		}
		out.Strategy = StrategySingletonArrayToObject
		out.SingletonArrayToObject = &SingletonArrayToObjectParams{
			HubPath: r.ObjectToSingletonArray.SpokePath, SpokePath: r.ObjectToSingletonArray.HubPath,
		}
	case StrategyFieldsToMap:
		if r.FieldsToMap == nil {
			return out, fmt.Errorf("FieldsToMap: missing params")
		}
		out.Strategy = StrategyMapToFields
		out.MapToFields = &MapToFieldsParams{
			HubMapPath: r.FieldsToMap.SpokeMapPath, SpokePaths: append([]string(nil), r.FieldsToMap.HubPaths...),
			KeyNames:        invertKeyNames(r.FieldsToMap.HubPaths, r.FieldsToMap.KeyNames),
			OnUnknownHubKey: r.FieldsToMap.OnUnknownSpokeKey,
		}
	case StrategyMapToFields:
		if r.MapToFields == nil {
			return out, fmt.Errorf("MapToFields: missing params")
		}
		out.Strategy = StrategyFieldsToMap
		out.FieldsToMap = &FieldsToMapParams{
			HubPaths: append([]string(nil), r.MapToFields.SpokePaths...), SpokeMapPath: r.MapToFields.HubMapPath,
			KeyNames:          invertKeyNames(r.MapToFields.SpokePaths, r.MapToFields.KeyNames),
			OnUnknownSpokeKey: r.MapToFields.OnUnknownHubKey,
		}
	case StrategyToAnnotation:
		if r.ToAnnotation == nil {
			return out, fmt.Errorf("ToAnnotation: missing params")
		}
		out.Strategy = StrategyFromAnnotation
		out.FromAnnotation = &FromMetadataParams{
			SpokePath: r.ToAnnotation.HubPath, Key: r.ToAnnotation.Key,
			Serialization: r.ToAnnotation.Serialization, StashOnReverse: r.ToAnnotation.RestoreOnReverse,
		}
	case StrategyToLabel:
		if r.ToLabel == nil {
			return out, fmt.Errorf("ToLabel: missing params")
		}
		// FromLabel is String-only; ToLabel may still carry JSON (or the
		// CRD default). Refuse JSON so rehub does not emit an unschedulable rule.
		ser := r.ToLabel.Serialization
		if ser == "JSON" {
			return out, fmt.Errorf("ToLabel with serialization=JSON cannot invert to FromLabel; use String")
		}
		if ser == "" {
			ser = "String"
		}
		out.Strategy = StrategyFromLabel
		out.FromLabel = &FromLabelParams{
			SpokePath: r.ToLabel.HubPath, Key: r.ToLabel.Key,
			Serialization: ser, StashOnReverse: r.ToLabel.RestoreOnReverse,
		}
	case StrategyFromAnnotation:
		if r.FromAnnotation == nil {
			return out, fmt.Errorf("FromAnnotation: missing params")
		}
		out.Strategy = StrategyToAnnotation
		out.ToAnnotation = &ToMetadataParams{
			HubPath: r.FromAnnotation.SpokePath, Key: r.FromAnnotation.Key,
			Serialization: r.FromAnnotation.Serialization, RestoreOnReverse: r.FromAnnotation.StashOnReverse,
		}
	case StrategyFromLabel:
		if r.FromLabel == nil {
			return out, fmt.Errorf("FromLabel: missing params")
		}
		out.Strategy = StrategyToLabel
		out.ToLabel = &ToMetadataParams{
			HubPath: r.FromLabel.SpokePath, Key: r.FromLabel.Key,
			Serialization: r.FromLabel.Serialization, RestoreOnReverse: r.FromLabel.StashOnReverse,
		}
	case StrategyEnumRemap:
		if r.EnumRemap == nil {
			return out, fmt.Errorf("EnumRemap: missing params")
		}
		out.Strategy = StrategyEnumRemap
		mapping := make([]EnumValueMapping, len(r.EnumRemap.Mapping))
		for i, m := range r.EnumRemap.Mapping {
			mapping[i] = EnumValueMapping{Hub: m.Spoke, Spoke: m.Hub}
		}
		out.EnumRemap = &EnumRemapParams{
			Path: r.EnumRemap.Path, Mapping: mapping,
			OnUnmappedHubValue: r.EnumRemap.OnUnmappedSpokeValue, OnUnmappedSpokeValue: r.EnumRemap.OnUnmappedHubValue,
		}
	case StrategyDefaultValue:
		if r.DefaultValue == nil {
			return out, fmt.Errorf("DefaultValue: missing params")
		}
		out.Strategy = StrategyDefaultValue
		out.DefaultValue = &DefaultValueParams{Path: r.DefaultValue.Path, ExistsOn: flipSide(r.DefaultValue.ExistsOn), Default: r.DefaultValue.Default}
	case StrategyConstant:
		if r.Constant == nil {
			return out, fmt.Errorf("constant rule: missing params")
		}
		out.Strategy = StrategyConstant
		out.Constant = &ConstantParams{Path: r.Constant.Path, ExistsOn: flipSide(r.Constant.ExistsOn), Value: r.Constant.Value}
	case StrategyDelete:
		if r.Delete == nil {
			return out, fmt.Errorf("delete rule: missing params")
		}
		out.Strategy = StrategyDelete
		out.Delete = &DeleteParams{Path: r.Delete.Path, ExistsOn: flipSide(r.Delete.ExistsOn)}
	case StrategyJSONPatch:
		if r.JSONPatch == nil {
			return out, fmt.Errorf("JSONPatch: missing params")
		}
		out.Strategy = StrategyJSONPatch
		out.JSONPatch = &JSONPatchParams{
			HubToSpoke:       append([]JSONPatchOp(nil), r.JSONPatch.SpokeToHub...),
			SpokeToHub:       append([]JSONPatchOp(nil), r.JSONPatch.HubToSpoke...),
			LosslessOverride: r.JSONPatch.LosslessOverride,
		}
	case StrategyForEach:
		if r.ForEach == nil {
			return out, fmt.Errorf("ForEach: missing params")
		}
		nested := make([]ConversionRule, 0, len(r.ForEach.Rules))
		for i, nr := range r.ForEach.Rules {
			inv, err := InvertRule(nr)
			if err != nil {
				return out, fmt.Errorf("ForEach rules[%d]: %w", i, err)
			}
			nested = append(nested, inv)
		}
		out.Strategy = StrategyForEach
		out.ForEach = &ForEachParams{
			HubItemsPath: r.ForEach.SpokeItemsPath, SpokeItemsPath: r.ForEach.HubItemsPath, Rules: nested,
		}
	case StrategyTypeCoerce:
		if r.TypeCoerce == nil {
			return out, fmt.Errorf("TypeCoerce: missing params")
		}
		out.Strategy = StrategyTypeCoerce
		out.TypeCoerce = &TypeCoerceParams{Path: r.TypeCoerce.Path, OnFractionalInteger: r.TypeCoerce.OnFractionalInteger}
	case StrategyScalarToFields:
		if r.ScalarToFields == nil {
			return out, fmt.Errorf("ScalarToFields: missing params")
		}
		out.Strategy = StrategyFieldsToScalar
		out.FieldsToScalar = &FieldsToScalarParams{
			HubFields: cloneStringMap(r.ScalarToFields.SpokeFields), SpokePath: r.ScalarToFields.HubPath,
			Pattern: r.ScalarToFields.Pattern, JoinTemplate: r.ScalarToFields.JoinTemplate,
			LosslessOverride: r.ScalarToFields.LosslessOverride,
		}
	case StrategyFieldsToScalar:
		if r.FieldsToScalar == nil {
			return out, fmt.Errorf("FieldsToScalar: missing params")
		}
		out.Strategy = StrategyScalarToFields
		out.ScalarToFields = &ScalarToFieldsParams{
			HubPath: r.FieldsToScalar.SpokePath, SpokeFields: cloneStringMap(r.FieldsToScalar.HubFields),
			Pattern: r.FieldsToScalar.Pattern, JoinTemplate: r.FieldsToScalar.JoinTemplate,
			LosslessOverride: r.FieldsToScalar.LosslessOverride,
		}
	case StrategyArrayToMapByKey:
		if r.ArrayToMapByKey == nil {
			return out, fmt.Errorf("ArrayToMapByKey: missing params")
		}
		out.Strategy = StrategyMapToArrayByKey
		out.MapToArrayByKey = &MapToArrayByKeyParams{
			HubPath: r.ArrayToMapByKey.SpokePath, SpokePath: r.ArrayToMapByKey.HubPath, KeyField: r.ArrayToMapByKey.KeyField,
		}
	case StrategyMapToArrayByKey:
		if r.MapToArrayByKey == nil {
			return out, fmt.Errorf("MapToArrayByKey: missing params")
		}
		out.Strategy = StrategyArrayToMapByKey
		out.ArrayToMapByKey = &ArrayToMapByKeyParams{
			HubPath: r.MapToArrayByKey.SpokePath, SpokePath: r.MapToArrayByKey.HubPath, KeyField: r.MapToArrayByKey.KeyField,
		}
	case StrategyNumericScale:
		if r.NumericScale == nil {
			return out, fmt.Errorf("NumericScale: missing params")
		}
		out.Strategy = StrategyNumericScale
		out.NumericScale = &NumericScaleParams{
			HubPath: r.NumericScale.SpokePath, SpokePath: r.NumericScale.HubPath, Factor: r.NumericScale.Factor,
		}
	case StrategyListJoin:
		if r.ListJoin == nil {
			return out, fmt.Errorf("ListJoin: missing params")
		}
		out.Strategy = StrategyListSplit
		out.ListSplit = &ListSplitParams{
			HubPath: r.ListJoin.SpokePath, SpokePath: r.ListJoin.HubPath, Separator: r.ListJoin.Separator,
		}
	case StrategyListSplit:
		if r.ListSplit == nil {
			return out, fmt.Errorf("ListSplit: missing params")
		}
		out.Strategy = StrategyListJoin
		out.ListJoin = &ListJoinParams{
			HubPath: r.ListSplit.SpokePath, SpokePath: r.ListSplit.HubPath, Separator: r.ListSplit.Separator,
		}
	case StrategyQuantity:
		if r.Quantity == nil {
			return out, fmt.Errorf("Quantity: missing params")
		}
		out.Strategy = StrategyQuantity
		out.Quantity = &QuantityParams{
			HubPath: r.Quantity.SpokePath, SpokePath: r.Quantity.HubPath,
		}
	case StrategyDuration:
		if r.Duration == nil {
			return out, fmt.Errorf("Duration: missing params")
		}
		out.Strategy = StrategyDuration
		out.Duration = &DurationParams{
			HubPath: r.Duration.SpokePath, SpokePath: r.Duration.HubPath,
		}
	case StrategyMapKeyRename:
		if r.MapKeyRename == nil {
			return out, fmt.Errorf("MapKeyRename: missing params")
		}
		rev := make(map[string]string, len(r.MapKeyRename.Renames))
		for hubKey, spokeKey := range r.MapKeyRename.Renames {
			rev[spokeKey] = hubKey
		}
		out.Strategy = StrategyMapKeyRename
		out.MapKeyRename = &MapKeyRenameParams{
			HubPath: r.MapKeyRename.SpokePath, SpokePath: r.MapKeyRename.HubPath, Renames: rev,
		}
	default:
		return out, fmt.Errorf("unsupported strategy %q", r.Strategy)
	}
	return out, nil
}

// InvertRules inverts every rule in rules.
func InvertRules(rules []ConversionRule) ([]ConversionRule, error) {
	out := make([]ConversionRule, 0, len(rules))
	for i, r := range rules {
		inv, err := InvertRule(r)
		if err != nil {
			return nil, fmt.Errorf("rules[%d]: %w", i, err)
		}
		out = append(out, inv)
	}
	return out, nil
}

// RehubSpokes rewrites hubVersion and spokes so newHub (a current spoke)
// becomes the hub. oldHub becomes a spoke whose rules are the invert of
// newHub's former spoke rules; every other spoke is composed through the
// old-hub → new-hub path map. spokeLeaves maps version → leaf path set
// (used to lift previously auto-covered renames).
func RehubSpokes(oldHub, newHub string, spokes []SpokeVersionRules, spokeLeaves map[string]map[string]bool) (string, []SpokeVersionRules, error) {
	if newHub == "" {
		return "", nil, fmt.Errorf("--to is required: name the version that becomes the new hub")
	}
	if newHub == oldHub {
		return "", nil, fmt.Errorf("--to %q is already the hub", newHub)
	}
	var promote *SpokeVersionRules
	others := make([]SpokeVersionRules, 0, len(spokes))
	for i := range spokes {
		s := spokes[i]
		if s.Version == newHub {
			cp := s
			promote = &cp
			continue
		}
		if s.Version == oldHub {
			return "", nil, fmt.Errorf("spoke %q equals the current hub %q; spokes must not include the hub", s.Version, oldHub)
		}
		others = append(others, s)
	}
	if promote == nil {
		return "", nil, fmt.Errorf("--to %q is not a current spoke (add it as a spoke before promoting)", newHub)
	}

	inverted, err := InvertRules(promote.Rules)
	if err != nil {
		return "", nil, fmt.Errorf("inverting spoke %q: %w", newHub, err)
	}
	pathMap, err := PathMapFromPromoteRules(promote.Rules)
	if err != nil {
		return "", nil, fmt.Errorf("building hub path map from spoke %q: %w", newHub, err)
	}

	out := make([]SpokeVersionRules, 0, 1+len(others))
	out = append(out, SpokeVersionRules{Version: oldHub, Rules: inverted})

	// Stable order: preserve relative order of remaining spokes from input.
	for _, s := range others {
		leaves := spokeLeaves[s.Version]
		if leaves == nil {
			leaves = map[string]bool{}
		}
		composed, err := ComposeSpokeRules(s.Rules, pathMap, leaves)
		if err != nil {
			return "", nil, fmt.Errorf("composing spoke %q for new hub %q: %w", s.Version, newHub, err)
		}
		out = append(out, SpokeVersionRules{Version: s.Version, Rules: composed})
	}
	return newHub, out, nil
}

// PathMapFromPromoteRules builds old-hub → new-hub path mappings from the
// rules that described the promoted version while it was still a spoke.
func PathMapFromPromoteRules(rules []ConversionRule) (HubPathMap, error) {
	m := HubPathMap{}
	for i, r := range rules {
		pairs, err := hubSpokePathPairs(r)
		if err != nil {
			return nil, fmt.Errorf("rules[%d]: %w", i, err)
		}
		for _, p := range pairs {
			if prev, ok := m[p.hub]; ok && prev != p.spoke {
				return nil, fmt.Errorf("conflicting hub path map for %q: %q vs %q", p.hub, prev, p.spoke)
			}
			m[p.hub] = p.spoke
		}
	}
	return m, nil
}

type pathPair struct{ hub, spoke string }

func hubSpokePathPairs(r ConversionRule) ([]pathPair, error) {
	switch r.Strategy {
	case StrategyFieldRename:
		if r.FieldRename == nil {
			return nil, fmt.Errorf("FieldRename: missing params")
		}
		return []pathPair{{r.FieldRename.HubPath, r.FieldRename.SpokePath}}, nil
	case StrategyScalarToObject:
		if r.ScalarToObject == nil {
			return nil, fmt.Errorf("ScalarToObject: missing params")
		}
		return []pathPair{{r.ScalarToObject.HubPath, r.ScalarToObject.SpokePath}}, nil
	case StrategyObjectToScalar:
		if r.ObjectToScalar == nil {
			return nil, fmt.Errorf("ObjectToScalar: missing params")
		}
		return []pathPair{{r.ObjectToScalar.HubPath, r.ObjectToScalar.SpokePath}}, nil
	case StrategySingletonArrayToObject:
		if r.SingletonArrayToObject == nil {
			return nil, fmt.Errorf("SingletonArrayToObject: missing params")
		}
		return []pathPair{{r.SingletonArrayToObject.HubPath, r.SingletonArrayToObject.SpokePath}}, nil
	case StrategyObjectToSingletonArray:
		if r.ObjectToSingletonArray == nil {
			return nil, fmt.Errorf("ObjectToSingletonArray: missing params")
		}
		return []pathPair{{r.ObjectToSingletonArray.HubPath, r.ObjectToSingletonArray.SpokePath}}, nil
	case StrategyTypeCoerce:
		if r.TypeCoerce == nil {
			return nil, fmt.Errorf("TypeCoerce: missing params")
		}
		return []pathPair{{r.TypeCoerce.Path, r.TypeCoerce.Path}}, nil
	case StrategyEnumRemap:
		if r.EnumRemap == nil {
			return nil, fmt.Errorf("EnumRemap: missing params")
		}
		return []pathPair{{r.EnumRemap.Path, r.EnumRemap.Path}}, nil
	case StrategyNumericScale:
		if r.NumericScale == nil {
			return nil, fmt.Errorf("NumericScale: missing params")
		}
		return []pathPair{{r.NumericScale.HubPath, r.NumericScale.SpokePath}}, nil
	case StrategyListJoin:
		if r.ListJoin == nil {
			return nil, fmt.Errorf("ListJoin: missing params")
		}
		return []pathPair{{r.ListJoin.HubPath, r.ListJoin.SpokePath}}, nil
	case StrategyListSplit:
		if r.ListSplit == nil {
			return nil, fmt.Errorf("ListSplit: missing params")
		}
		return []pathPair{{r.ListSplit.HubPath, r.ListSplit.SpokePath}}, nil
	case StrategyQuantity:
		if r.Quantity == nil {
			return nil, fmt.Errorf("Quantity: missing params")
		}
		return []pathPair{{r.Quantity.HubPath, r.Quantity.SpokePath}}, nil
	case StrategyDuration:
		if r.Duration == nil {
			return nil, fmt.Errorf("Duration: missing params")
		}
		return []pathPair{{r.Duration.HubPath, r.Duration.SpokePath}}, nil
	case StrategyMapKeyRename:
		if r.MapKeyRename == nil {
			return nil, fmt.Errorf("MapKeyRename: missing params")
		}
		return []pathPair{{r.MapKeyRename.HubPath, r.MapKeyRename.SpokePath}}, nil
	case StrategyArrayToMapByKey:
		if r.ArrayToMapByKey == nil {
			return nil, fmt.Errorf("ArrayToMapByKey: missing params")
		}
		return []pathPair{{r.ArrayToMapByKey.HubPath, r.ArrayToMapByKey.SpokePath}}, nil
	case StrategyMapToArrayByKey:
		if r.MapToArrayByKey == nil {
			return nil, fmt.Errorf("MapToArrayByKey: missing params")
		}
		return []pathPair{{r.MapToArrayByKey.HubPath, r.MapToArrayByKey.SpokePath}}, nil
	case StrategyScalarToFields:
		if r.ScalarToFields == nil {
			return nil, fmt.Errorf("ScalarToFields: missing params")
		}
		// Multi-path: map the hub scalar to itself for rewrite of other
		// rules that reference it; spoke fields are not old-hub paths.
		return []pathPair{{r.ScalarToFields.HubPath, r.ScalarToFields.HubPath}}, nil
	case StrategyFieldsToScalar:
		if r.FieldsToScalar == nil {
			return nil, fmt.Errorf("FieldsToScalar: missing params")
		}
		pairs := make([]pathPair, 0, len(r.FieldsToScalar.HubFields))
		for _, hp := range r.FieldsToScalar.HubFields {
			pairs = append(pairs, pathPair{hp, hp})
		}
		return pairs, nil
	case StrategyFieldsToMap:
		if r.FieldsToMap == nil {
			return nil, fmt.Errorf("FieldsToMap: missing params")
		}
		pairs := make([]pathPair, 0, len(r.FieldsToMap.HubPaths))
		for _, hp := range r.FieldsToMap.HubPaths {
			pairs = append(pairs, pathPair{hp, hp})
		}
		return pairs, nil
	case StrategyMapToFields:
		if r.MapToFields == nil {
			return nil, fmt.Errorf("MapToFields: missing params")
		}
		return []pathPair{{r.MapToFields.HubMapPath, r.MapToFields.HubMapPath}}, nil
	case StrategyToAnnotation, StrategyToLabel, StrategyFromAnnotation, StrategyFromLabel:
		// Metadata stash does not remap schema paths between hubs.
		return nil, nil
	case StrategyDefaultValue, StrategyConstant, StrategyDelete:
		return nil, nil
	case StrategyJSONPatch:
		return nil, fmt.Errorf("JSONPatch cannot contribute to a hub path map; remaining spokes that depend on it must be rewritten by hand")
	case StrategyForEach:
		if r.ForEach == nil {
			return nil, fmt.Errorf("ForEach: missing params")
		}
		return []pathPair{{r.ForEach.HubItemsPath, r.ForEach.SpokeItemsPath}}, nil
	default:
		return nil, fmt.Errorf("unsupported strategy %q", r.Strategy)
	}
}

// RewriteHubPaths rewrites hub-side paths in a rule through m (old hub → new hub).
func RewriteHubPaths(r ConversionRule, m HubPathMap) (ConversionRule, error) {
	mapPath := func(p string) (string, error) {
		if p == "" {
			return p, nil
		}
		if next, ok := m[p]; ok {
			return next, nil
		}
		// Identity if unmapped: field kept the same path on the new hub.
		return p, nil
	}
	out := r
	switch r.Strategy {
	case StrategyFieldRename:
		if r.FieldRename == nil {
			return out, fmt.Errorf("FieldRename: missing params")
		}
		hp, err := mapPath(r.FieldRename.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.FieldRename
		cp.HubPath = hp
		out.FieldRename = &cp
	case StrategyScalarToObject:
		if r.ScalarToObject == nil {
			return out, fmt.Errorf("ScalarToObject: missing params")
		}
		hp, err := mapPath(r.ScalarToObject.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.ScalarToObject
		cp.HubPath = hp
		out.ScalarToObject = &cp
	case StrategyObjectToScalar:
		if r.ObjectToScalar == nil {
			return out, fmt.Errorf("ObjectToScalar: missing params")
		}
		hp, err := mapPath(r.ObjectToScalar.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.ObjectToScalar
		cp.HubPath = hp
		out.ObjectToScalar = &cp
	case StrategySingletonArrayToObject:
		if r.SingletonArrayToObject == nil {
			return out, fmt.Errorf("SingletonArrayToObject: missing params")
		}
		hp, err := mapPath(r.SingletonArrayToObject.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.SingletonArrayToObject
		cp.HubPath = hp
		out.SingletonArrayToObject = &cp
	case StrategyObjectToSingletonArray:
		if r.ObjectToSingletonArray == nil {
			return out, fmt.Errorf("ObjectToSingletonArray: missing params")
		}
		hp, err := mapPath(r.ObjectToSingletonArray.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.ObjectToSingletonArray
		cp.HubPath = hp
		out.ObjectToSingletonArray = &cp
	case StrategyFieldsToMap:
		if r.FieldsToMap == nil {
			return out, fmt.Errorf("FieldsToMap: missing params")
		}
		paths := make([]string, len(r.FieldsToMap.HubPaths))
		for i, p := range r.FieldsToMap.HubPaths {
			np, err := mapPath(p)
			if err != nil {
				return out, err
			}
			paths[i] = np
		}
		cp := *r.FieldsToMap
		cp.HubPaths = paths
		out.FieldsToMap = &cp
	case StrategyMapToFields:
		if r.MapToFields == nil {
			return out, fmt.Errorf("MapToFields: missing params")
		}
		hp, err := mapPath(r.MapToFields.HubMapPath)
		if err != nil {
			return out, err
		}
		cp := *r.MapToFields
		cp.HubMapPath = hp
		out.MapToFields = &cp
	case StrategyToAnnotation:
		if r.ToAnnotation == nil {
			return out, fmt.Errorf("ToAnnotation: missing params")
		}
		hp, err := mapPath(r.ToAnnotation.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.ToAnnotation
		cp.HubPath = hp
		out.ToAnnotation = &cp
	case StrategyToLabel:
		if r.ToLabel == nil {
			return out, fmt.Errorf("ToLabel: missing params")
		}
		hp, err := mapPath(r.ToLabel.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.ToLabel
		cp.HubPath = hp
		out.ToLabel = &cp
	case StrategyFromAnnotation, StrategyFromLabel:
		// Spoke-side field; hub metadata key is not a schema path rewrite.
		return out, nil
	case StrategyEnumRemap:
		if r.EnumRemap == nil {
			return out, fmt.Errorf("EnumRemap: missing params")
		}
		hp, err := mapPath(r.EnumRemap.Path)
		if err != nil {
			return out, err
		}
		cp := *r.EnumRemap
		cp.Path = hp
		out.EnumRemap = &cp
	case StrategyDefaultValue:
		if r.DefaultValue == nil {
			return out, fmt.Errorf("DefaultValue: missing params")
		}
		if r.DefaultValue.ExistsOn == SideHub {
			hp, err := mapPath(r.DefaultValue.Path)
			if err != nil {
				return out, err
			}
			cp := *r.DefaultValue
			cp.Path = hp
			out.DefaultValue = &cp
		}
	case StrategyConstant:
		if r.Constant == nil {
			return out, fmt.Errorf("constant rule: missing params")
		}
		if r.Constant.ExistsOn == SideHub {
			hp, err := mapPath(r.Constant.Path)
			if err != nil {
				return out, err
			}
			cp := *r.Constant
			cp.Path = hp
			out.Constant = &cp
		}
	case StrategyDelete:
		if r.Delete == nil {
			return out, fmt.Errorf("delete rule: missing params")
		}
		if r.Delete.ExistsOn == SideHub {
			hp, err := mapPath(r.Delete.Path)
			if err != nil {
				return out, err
			}
			cp := *r.Delete
			cp.Path = hp
			out.Delete = &cp
		}
	case StrategyJSONPatch:
		return out, fmt.Errorf("JSONPatch hub-path rewrite is not supported; rewrite remaining-spoke JSONPatch rules by hand")
	case StrategyForEach:
		if r.ForEach == nil {
			return out, fmt.Errorf("ForEach: missing params")
		}
		hp, err := mapPath(r.ForEach.HubItemsPath)
		if err != nil {
			return out, err
		}
		nested := append([]ConversionRule(nil), r.ForEach.Rules...)
		cp := *r.ForEach
		cp.HubItemsPath = hp
		cp.Rules = nested
		out.ForEach = &cp
	case StrategyTypeCoerce:
		if r.TypeCoerce == nil {
			return out, fmt.Errorf("TypeCoerce: missing params")
		}
		hp, err := mapPath(r.TypeCoerce.Path)
		if err != nil {
			return out, err
		}
		out.TypeCoerce = &TypeCoerceParams{Path: hp, OnFractionalInteger: r.TypeCoerce.OnFractionalInteger}
	case StrategyScalarToFields:
		if r.ScalarToFields == nil {
			return out, fmt.Errorf("ScalarToFields: missing params")
		}
		hp, err := mapPath(r.ScalarToFields.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.ScalarToFields
		cp.HubPath = hp
		out.ScalarToFields = &cp
	case StrategyFieldsToScalar:
		if r.FieldsToScalar == nil {
			return out, fmt.Errorf("FieldsToScalar: missing params")
		}
		fields := map[string]string{}
		for k, hp := range r.FieldsToScalar.HubFields {
			np, err := mapPath(hp)
			if err != nil {
				return out, err
			}
			fields[k] = np
		}
		cp := *r.FieldsToScalar
		cp.HubFields = fields
		out.FieldsToScalar = &cp
	case StrategyArrayToMapByKey:
		if r.ArrayToMapByKey == nil {
			return out, fmt.Errorf("ArrayToMapByKey: missing params")
		}
		hp, err := mapPath(r.ArrayToMapByKey.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.ArrayToMapByKey
		cp.HubPath = hp
		out.ArrayToMapByKey = &cp
	case StrategyMapToArrayByKey:
		if r.MapToArrayByKey == nil {
			return out, fmt.Errorf("MapToArrayByKey: missing params")
		}
		hp, err := mapPath(r.MapToArrayByKey.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.MapToArrayByKey
		cp.HubPath = hp
		out.MapToArrayByKey = &cp
	case StrategyNumericScale:
		if r.NumericScale == nil {
			return out, fmt.Errorf("NumericScale: missing params")
		}
		hp, err := mapPath(r.NumericScale.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.NumericScale
		cp.HubPath = hp
		out.NumericScale = &cp
	case StrategyListJoin:
		if r.ListJoin == nil {
			return out, fmt.Errorf("ListJoin: missing params")
		}
		hp, err := mapPath(r.ListJoin.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.ListJoin
		cp.HubPath = hp
		out.ListJoin = &cp
	case StrategyListSplit:
		if r.ListSplit == nil {
			return out, fmt.Errorf("ListSplit: missing params")
		}
		hp, err := mapPath(r.ListSplit.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.ListSplit
		cp.HubPath = hp
		out.ListSplit = &cp
	case StrategyQuantity:
		if r.Quantity == nil {
			return out, fmt.Errorf("Quantity: missing params")
		}
		hp, err := mapPath(r.Quantity.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.Quantity
		cp.HubPath = hp
		out.Quantity = &cp
	case StrategyDuration:
		if r.Duration == nil {
			return out, fmt.Errorf("Duration: missing params")
		}
		hp, err := mapPath(r.Duration.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.Duration
		cp.HubPath = hp
		out.Duration = &cp
	case StrategyMapKeyRename:
		if r.MapKeyRename == nil {
			return out, fmt.Errorf("MapKeyRename: missing params")
		}
		hp, err := mapPath(r.MapKeyRename.HubPath)
		if err != nil {
			return out, err
		}
		cp := *r.MapKeyRename
		cp.HubPath = hp
		out.MapKeyRename = &cp
	default:
		return out, fmt.Errorf("unsupported strategy %q", r.Strategy)
	}
	return out, nil
}

// ComposeSpokeRules rewrites an existing spoke's rules for a new hub using
// pathMap (old hub → new hub). spokeLeafPaths are leaf paths present on the
// spoke schema; when a remapped old-hub path was previously auto-covered
// (identical on old hub and spoke), a FieldRename is synthesized.
func ComposeSpokeRules(rules []ConversionRule, pathMap HubPathMap, spokeLeafPaths map[string]bool) ([]ConversionRule, error) {
	out := make([]ConversionRule, 0, len(rules)+len(pathMap))
	claimedSpoke := map[string]bool{}
	for i, r := range rules {
		rw, err := RewriteHubPaths(r, pathMap)
		if err != nil {
			return nil, fmt.Errorf("rules[%d]: %w", i, err)
		}
		out = append(out, rw)
		for _, sp := range spokePathsOf(rw) {
			claimedSpoke[sp] = true
		}
	}
	// Lift auto-covered old-hub paths that the promote rules remapped.
	var keys []string
	for k := range pathMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, oldHub := range keys {
		newHub := pathMap[oldHub]
		if oldHub == newHub {
			continue
		}
		if !spokeLeafPaths[oldHub] {
			continue
		}
		if claimedSpoke[oldHub] {
			continue
		}
		out = append(out, ConversionRule{
			Strategy: StrategyFieldRename,
			FieldRename: &FieldRenameParams{
				HubPath:   newHub,
				SpokePath: oldHub,
			},
		})
		claimedSpoke[oldHub] = true
	}
	return out, nil
}

func spokePathsOf(r ConversionRule) []string {
	switch r.Strategy {
	case StrategyFieldRename:
		if r.FieldRename != nil {
			return []string{r.FieldRename.SpokePath}
		}
	case StrategyFromAnnotation:
		if r.FromAnnotation != nil {
			return []string{r.FromAnnotation.SpokePath}
		}
	case StrategyFromLabel:
		if r.FromLabel != nil {
			return []string{r.FromLabel.SpokePath}
		}
	case StrategyScalarToObject:
		if r.ScalarToObject != nil {
			return []string{r.ScalarToObject.SpokePath}
		}
	case StrategyObjectToScalar:
		if r.ObjectToScalar != nil {
			return []string{r.ObjectToScalar.SpokePath}
		}
	case StrategySingletonArrayToObject:
		if r.SingletonArrayToObject != nil {
			return []string{r.SingletonArrayToObject.SpokePath}
		}
	case StrategyObjectToSingletonArray:
		if r.ObjectToSingletonArray != nil {
			return []string{r.ObjectToSingletonArray.SpokePath}
		}
	case StrategyFieldsToMap:
		if r.FieldsToMap != nil {
			return []string{r.FieldsToMap.SpokeMapPath}
		}
	case StrategyMapToFields:
		if r.MapToFields != nil {
			return append([]string(nil), r.MapToFields.SpokePaths...)
		}
	case StrategyScalarToFields:
		if r.ScalarToFields != nil {
			out := make([]string, 0, len(r.ScalarToFields.SpokeFields))
			for _, p := range r.ScalarToFields.SpokeFields {
				out = append(out, p)
			}
			return out
		}
	case StrategyFieldsToScalar:
		if r.FieldsToScalar != nil {
			return []string{r.FieldsToScalar.SpokePath}
		}
	case StrategyArrayToMapByKey:
		if r.ArrayToMapByKey != nil {
			return []string{r.ArrayToMapByKey.SpokePath}
		}
	case StrategyMapToArrayByKey:
		if r.MapToArrayByKey != nil {
			return []string{r.MapToArrayByKey.SpokePath}
		}
	case StrategyNumericScale:
		if r.NumericScale != nil {
			return []string{r.NumericScale.SpokePath}
		}
	case StrategyListJoin:
		if r.ListJoin != nil {
			return []string{r.ListJoin.SpokePath}
		}
	case StrategyListSplit:
		if r.ListSplit != nil {
			return []string{r.ListSplit.SpokePath}
		}
	case StrategyQuantity:
		if r.Quantity != nil {
			return []string{r.Quantity.SpokePath}
		}
	case StrategyDuration:
		if r.Duration != nil {
			return []string{r.Duration.SpokePath}
		}
	case StrategyMapKeyRename:
		if r.MapKeyRename != nil {
			return []string{r.MapKeyRename.SpokePath}
		}
	case StrategyTypeCoerce:
		if r.TypeCoerce != nil {
			return []string{r.TypeCoerce.Path}
		}
	case StrategyEnumRemap:
		if r.EnumRemap != nil {
			return []string{r.EnumRemap.Path}
		}
	case StrategyDefaultValue:
		if r.DefaultValue != nil && r.DefaultValue.ExistsOn == SideSpoke {
			return []string{r.DefaultValue.Path}
		}
	case StrategyConstant:
		if r.Constant != nil && r.Constant.ExistsOn == SideSpoke {
			return []string{r.Constant.Path}
		}
	case StrategyDelete:
		if r.Delete != nil && r.Delete.ExistsOn == SideSpoke {
			return []string{r.Delete.Path}
		}
	case StrategyForEach:
		if r.ForEach != nil {
			return []string{r.ForEach.SpokeItemsPath}
		}
	case StrategyToAnnotation, StrategyToLabel, StrategyJSONPatch:
		// No spoke schema path claimed (metadata-only or opaque patches).
		return nil
	}
	return nil
}

func flipSide(s Side) Side {
	if s == SideHub {
		return SideSpoke
	}
	return SideHub
}

func invertKeyNames(paths []string, keyNames map[string]string) map[string]string {
	if keyNames == nil {
		return nil
	}
	out := map[string]string{}
	for _, p := range paths {
		if v, ok := keyNames[p]; ok {
			out[p] = v
		}
	}
	return out
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
