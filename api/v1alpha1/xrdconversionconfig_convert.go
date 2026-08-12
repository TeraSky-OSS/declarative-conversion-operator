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

// This file is the only place that knows how to translate the CRD-facing
// ConversionRule union into pkg/engine's generic Rule types. Keeping the
// translation here (rather than in pkg/engine) preserves the seam that
// keeps the engine agnostic of any particular CRD shape.

import (
	"encoding/json"
	"fmt"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// ToRuleSets translates every spoke's declarative rules into engine
// RuleSets, ready to hand to engine.Analyze/engine.Compile alongside a
// SchemaSource.
func (c *XRDConversionConfig) ToRuleSets() ([]engine.RuleSet, error) {
	return spokesToRuleSets(c.Spec.HubVersion, c.Spec.Spokes, c.Spec.UnmappedFieldPolicy, c.Spec.UnmappedFieldReason)
}

// ToRuleSets is CRDConversionConfig's counterpart to
// XRDConversionConfig.ToRuleSets, sharing the exact same translation —
// ConversionRule and its strategy-specific params carry no notion of
// whether they're converting an XRD or a native CRD.
func (c *CRDConversionConfig) ToRuleSets() ([]engine.RuleSet, error) {
	return spokesToRuleSets(c.Spec.HubVersion, c.Spec.Spokes, c.Spec.UnmappedFieldPolicy, c.Spec.UnmappedFieldReason)
}

// spokesToRuleSets is the shared implementation behind both
// ToRuleSets methods above.
func spokesToRuleSets(hubVersion string, spokes []SpokeVersionRules, policy UnmappedFieldPolicy, reason string) ([]engine.RuleSet, error) {
	out := make([]engine.RuleSet, 0, len(spokes))
	for _, spoke := range spokes {
		rules, err := convertRules(spoke.Rules)
		if err != nil {
			return nil, fmt.Errorf("spoke %q: %w", spoke.Version, err)
		}
		out = append(out, engine.RuleSet{
			HubVersion:          hubVersion,
			SpokeVersion:        spoke.Version,
			Rules:               rules,
			UnmappedFieldPolicy: engine.UnmappedFieldPolicy(orDefault(string(policy), string(UnmappedFieldPolicyError))),
			UnmappedFieldReason: reason,
		})
	}
	return out, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func convertRules(rules []ConversionRule) ([]engine.Rule, error) {
	out := make([]engine.Rule, 0, len(rules))
	for i, r := range rules {
		params, err := convertParams(r)
		if err != nil {
			return nil, fmt.Errorf("rule %d (%s): %w", i, r.Strategy, err)
		}
		out = append(out, engine.Rule{
			Strategy:         engine.Strategy(r.Strategy),
			Params:           params,
			AcknowledgeLossy: r.AcknowledgeLossy,
			Reason:           r.Reason,
			SourceIndex:      i,
		})
	}
	return out, nil
}

func convertParams(r ConversionRule) (engine.RuleParams, error) {
	switch r.Strategy {
	case StrategyFieldRename:
		if r.FieldRename == nil {
			return nil, fmt.Errorf("requires fieldRename params")
		}
		return engine.FieldRenameParams{
			HubPath:   engine.ParsePath(r.FieldRename.HubPath),
			SpokePath: engine.ParsePath(r.FieldRename.SpokePath),
		}, nil

	case StrategyScalarToObject:
		if r.ScalarToObject == nil {
			return nil, fmt.Errorf("requires scalarToObject params")
		}
		defaults, err := jsonMapToAny(r.ScalarToObject.DefaultsForOtherKeys)
		if err != nil {
			return nil, err
		}
		return engine.ScalarToObjectParams{
			HubPath: engine.ParsePath(r.ScalarToObject.HubPath), SpokePath: engine.ParsePath(r.ScalarToObject.SpokePath),
			Key: r.ScalarToObject.Key, DefaultsForOtherKeys: defaults,
		}, nil

	case StrategyObjectToScalar:
		if r.ObjectToScalar == nil {
			return nil, fmt.Errorf("requires objectToScalar params")
		}
		defaults, err := jsonMapToAny(r.ObjectToScalar.DefaultsForOtherKeys)
		if err != nil {
			return nil, err
		}
		return engine.ObjectToScalarParams{
			HubPath: engine.ParsePath(r.ObjectToScalar.HubPath), SpokePath: engine.ParsePath(r.ObjectToScalar.SpokePath),
			Key: r.ObjectToScalar.Key, DefaultsForOtherKeys: defaults,
		}, nil

	case StrategySingletonArrayToObject:
		if r.SingletonArrayToObject == nil {
			return nil, fmt.Errorf("requires singletonArrayToObject params")
		}
		return engine.SingletonArrayToObjectParams{
			HubPath: engine.ParsePath(r.SingletonArrayToObject.HubPath), SpokePath: engine.ParsePath(r.SingletonArrayToObject.SpokePath),
		}, nil

	case StrategyObjectToSingletonArray:
		if r.ObjectToSingletonArray == nil {
			return nil, fmt.Errorf("requires objectToSingletonArray params")
		}
		return engine.ObjectToSingletonArrayParams{
			HubPath: engine.ParsePath(r.ObjectToSingletonArray.HubPath), SpokePath: engine.ParsePath(r.ObjectToSingletonArray.SpokePath),
		}, nil

	case StrategyFieldsToMap:
		if r.FieldsToMap == nil {
			return nil, fmt.Errorf("requires fieldsToMap params")
		}
		return engine.FieldsToMapParams{
			HubPaths:          parsePaths(r.FieldsToMap.HubPaths),
			SpokeMapPath:      engine.ParsePath(r.FieldsToMap.SpokeMapPath),
			KeyNames:          rekeyByPath(r.FieldsToMap.HubPaths, r.FieldsToMap.KeyNames),
			OnUnknownSpokeKey: engine.UnknownKeyPolicy(orDefault(string(r.FieldsToMap.OnUnknownSpokeKey), string(UnknownKeyPolicyError))),
		}, nil

	case StrategyMapToFields:
		if r.MapToFields == nil {
			return nil, fmt.Errorf("requires mapToFields params")
		}
		return engine.MapToFieldsParams{
			HubMapPath:      engine.ParsePath(r.MapToFields.HubMapPath),
			SpokePaths:      parsePaths(r.MapToFields.SpokePaths),
			KeyNames:        rekeyByPath(r.MapToFields.SpokePaths, r.MapToFields.KeyNames),
			OnUnknownHubKey: engine.UnknownKeyPolicy(orDefault(string(r.MapToFields.OnUnknownHubKey), string(UnknownKeyPolicyError))),
		}, nil

	case StrategyToAnnotation, StrategyToLabel:
		p := r.ToAnnotation
		if r.Strategy == StrategyToLabel {
			p = r.ToLabel
		}
		if p == nil {
			return nil, fmt.Errorf("requires %s params", r.Strategy)
		}
		return engine.ToMetadataParams{
			HubPath: engine.ParsePath(p.HubPath), Key: p.Key,
			Serialization: orDefault(p.Serialization, "JSON"), RestoreOnReverse: p.RestoreOnReverse,
		}, nil

	case StrategyEnumRemap:
		if r.EnumRemap == nil {
			return nil, fmt.Errorf("requires enumRemap params")
		}
		mapping := make([]engine.EnumValueMapping, 0, len(r.EnumRemap.Mapping))
		for _, m := range r.EnumRemap.Mapping {
			mapping = append(mapping, engine.EnumValueMapping{Hub: m.Hub, Spoke: m.Spoke})
		}
		return engine.EnumRemapParams{
			Path: engine.ParsePath(r.EnumRemap.Path), Mapping: mapping,
			OnUnmappedHubValue:   engine.UnknownKeyPolicy(orDefault(string(r.EnumRemap.OnUnmappedHubValue), string(UnknownKeyPolicyError))),
			OnUnmappedSpokeValue: engine.UnknownKeyPolicy(orDefault(string(r.EnumRemap.OnUnmappedSpokeValue), string(UnknownKeyPolicyError))),
		}, nil

	case StrategyDefaultValue:
		if r.DefaultValue == nil {
			return nil, fmt.Errorf("requires defaultValue params")
		}
		val, err := jsonToAny(r.DefaultValue.Default)
		if err != nil {
			return nil, err
		}
		return engine.DefaultValueParams{
			Path: engine.ParsePath(r.DefaultValue.Path), ExistsOn: engine.Side(r.DefaultValue.ExistsOn), Default: val,
		}, nil

	case StrategyConstant:
		if r.Constant == nil {
			return nil, fmt.Errorf("requires constant params")
		}
		val, err := jsonToAny(r.Constant.Value)
		if err != nil {
			return nil, err
		}
		return engine.ConstantParams{
			Path: engine.ParsePath(r.Constant.Path), ExistsOn: engine.Side(r.Constant.ExistsOn), Value: val,
		}, nil

	case StrategyDelete:
		if r.Delete == nil {
			return nil, fmt.Errorf("requires delete params")
		}
		return engine.DeleteParams{Path: engine.ParsePath(r.Delete.Path), ExistsOn: engine.Side(r.Delete.ExistsOn)}, nil

	case StrategyJSONPatch:
		if r.JSONPatch == nil {
			return nil, fmt.Errorf("requires jsonPatch params")
		}
		h2s, err := convertPatchOps(r.JSONPatch.HubToSpoke)
		if err != nil {
			return nil, err
		}
		s2h, err := convertPatchOps(r.JSONPatch.SpokeToHub)
		if err != nil {
			return nil, err
		}
		return engine.JSONPatchParams{HubToSpoke: h2s, SpokeToHub: s2h, LosslessOverride: r.JSONPatch.LosslessOverride}, nil

	case StrategyForEach:
		if r.ForEach == nil {
			return nil, fmt.Errorf("requires forEach params")
		}
		nested, err := convertRules(r.ForEach.Rules)
		if err != nil {
			return nil, fmt.Errorf("nested rules: %w", err)
		}
		return engine.ForEachParams{
			HubItemsPath: engine.ParsePath(r.ForEach.HubItemsPath), SpokeItemsPath: engine.ParsePath(r.ForEach.SpokeItemsPath),
			Rules: nested,
		}, nil

	case StrategyTypeCoerce:
		if r.TypeCoerce == nil {
			return nil, fmt.Errorf("requires typeCoerce params")
		}
		return engine.TypeCoerceParams{Path: engine.ParsePath(r.TypeCoerce.Path)}, nil

	case StrategyScalarToFields:
		if r.ScalarToFields == nil {
			return nil, fmt.Errorf("requires scalarToFields params")
		}
		return engine.ScalarToFieldsParams{
			HubPath:          engine.ParsePath(r.ScalarToFields.HubPath),
			Pattern:          r.ScalarToFields.Pattern,
			SpokeFields:      parsePathMap(r.ScalarToFields.SpokeFields),
			JoinTemplate:     r.ScalarToFields.JoinTemplate,
			LosslessOverride: r.ScalarToFields.LosslessOverride,
		}, nil

	case StrategyFieldsToScalar:
		if r.FieldsToScalar == nil {
			return nil, fmt.Errorf("requires fieldsToScalar params")
		}
		return engine.FieldsToScalarParams{
			HubFields:        parsePathMap(r.FieldsToScalar.HubFields),
			Pattern:          r.FieldsToScalar.Pattern,
			SpokePath:        engine.ParsePath(r.FieldsToScalar.SpokePath),
			JoinTemplate:     r.FieldsToScalar.JoinTemplate,
			LosslessOverride: r.FieldsToScalar.LosslessOverride,
		}, nil

	case StrategyArrayToMapByKey:
		if r.ArrayToMapByKey == nil {
			return nil, fmt.Errorf("requires arrayToMapByKey params")
		}
		return engine.ArrayToMapByKeyParams{
			HubPath: engine.ParsePath(r.ArrayToMapByKey.HubPath), SpokePath: engine.ParsePath(r.ArrayToMapByKey.SpokePath),
			KeyField: r.ArrayToMapByKey.KeyField,
		}, nil

	case StrategyMapToArrayByKey:
		if r.MapToArrayByKey == nil {
			return nil, fmt.Errorf("requires mapToArrayByKey params")
		}
		return engine.MapToArrayByKeyParams{
			HubPath: engine.ParsePath(r.MapToArrayByKey.HubPath), SpokePath: engine.ParsePath(r.MapToArrayByKey.SpokePath),
			KeyField: r.MapToArrayByKey.KeyField,
		}, nil

	case StrategyNumericScale:
		if r.NumericScale == nil {
			return nil, fmt.Errorf("requires numericScale params")
		}
		return engine.NumericScaleParams{
			HubPath: engine.ParsePath(r.NumericScale.HubPath), SpokePath: engine.ParsePath(r.NumericScale.SpokePath),
			Factor: r.NumericScale.Factor,
		}, nil

	case StrategyListJoin:
		if r.ListJoin == nil {
			return nil, fmt.Errorf("requires listJoin params")
		}
		return engine.ListJoinParams{
			HubPath: engine.ParsePath(r.ListJoin.HubPath), SpokePath: engine.ParsePath(r.ListJoin.SpokePath),
			Separator: r.ListJoin.Separator,
		}, nil

	case StrategyListSplit:
		if r.ListSplit == nil {
			return nil, fmt.Errorf("requires listSplit params")
		}
		return engine.ListSplitParams{
			HubPath: engine.ParsePath(r.ListSplit.HubPath), SpokePath: engine.ParsePath(r.ListSplit.SpokePath),
			Separator: r.ListSplit.Separator,
		}, nil

	default:
		return nil, fmt.Errorf("unknown strategy %q", r.Strategy)
	}
}

// parsePathMap converts a map of name -> dotted path string (as declared
// in the CRD-facing API) into name -> engine.FieldPath.
func parsePathMap(m map[string]string) map[string]engine.FieldPath {
	if m == nil {
		return nil
	}
	out := make(map[string]engine.FieldPath, len(m))
	for k, v := range m {
		out[k] = engine.ParsePath(v)
	}
	return out
}

func parsePaths(paths []string) []engine.FieldPath {
	out := make([]engine.FieldPath, 0, len(paths))
	for _, p := range paths {
		out = append(out, engine.ParsePath(p))
	}
	return out
}

// rekeyByPath is a no-op passthrough today (api KeyNames are already keyed
// by the dotted path string, matching what engine.FieldPath.String()
// produces), kept as a named seam in case path normalization ever diverges.
func rekeyByPath(_ []string, keyNames map[string]string) map[string]string {
	if keyNames == nil {
		return map[string]string{}
	}
	return keyNames
}

func convertPatchOps(ops []JSONPatchOp) ([]engine.JSONPatchOp, error) {
	out := make([]engine.JSONPatchOp, 0, len(ops))
	for _, o := range ops {
		var val any
		if o.Value != nil {
			v, err := jsonToAny(*o.Value)
			if err != nil {
				return nil, err
			}
			val = v
		}
		out = append(out, engine.JSONPatchOp{Op: o.Op, Path: o.Path, From: o.From, Value: val})
	}
	return out, nil
}

func jsonToAny(j extv1.JSON) (any, error) {
	if len(j.Raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(j.Raw, &v); err != nil {
		return nil, fmt.Errorf("unmarshal JSON value: %w", err)
	}
	return v, nil
}

func jsonMapToAny(m map[string]extv1.JSON) (map[string]any, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		val, err := jsonToAny(v)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		out[k] = val
	}
	return out, nil
}
