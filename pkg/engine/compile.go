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

import (
	"fmt"

	"encoding/json"
	jsonpatch "github.com/evanphx/json-patch/v5"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// Compile resolves rules against hub and spoke schemas and produces an
// executable Plan plus any diagnostics found. It is the lower-level
// building block Analyze uses per spoke; it's also directly usable by a CLI
// that wants compile-only behavior without the full report shape.
//
// The returned Plan is non-nil even when diagnostics contains errors, so
// callers that want the fail-closed default behavior ("never execute a
// plan derived from an invalid analysis") must check for errors themselves
// (see Analyze, which enforces this). Compile returns a non-nil error only
// for structural Go-level problems (nil schema), never for validation
// findings — those are reported as Diagnostics.
func Compile(rules RuleSet, hub, spoke *extv1.JSONSchemaProps) (*Plan, []Diagnostic, error) {
	if hub == nil || spoke == nil {
		return nil, nil, fmt.Errorf("compile: hub and spoke schemas must both be non-nil")
	}
	h2s, s2h, _, diags, _ := resolveAndBuildOps(rules.Rules, hub, spoke, effectivePolicy(rules.UnmappedFieldPolicy), 0)
	return &Plan{HubVersion: rules.HubVersion, SpokeVersion: rules.SpokeVersion, HubToSpoke: h2s, SpokeToHub: s2h}, diags, nil
}

func effectivePolicy(p UnmappedFieldPolicy) UnmappedFieldPolicy {
	if p == "" {
		return UnmappedFieldPolicyError
	}
	return p
}

const maxForEachDepth = 1

// resolveAndBuildOps is the shared core used both at the top level and
// recursively for ForEach's nested rule list. depth tracks ForEach nesting
// to enforce the depth-1 cap.
func resolveAndBuildOps(rules []Rule, hub, spoke *extv1.JSONSchemaProps, policy UnmappedFieldPolicy, depth int) (h2sOps, s2hOps []Op, results []RuleResult, diags []Diagnostic, verdict LosslessVerdict) {
	claimedHub := map[string]bool{}
	claimedSpoke := map[string]bool{}
	verdict = LosslessVerdict{HubToSpoke: true, SpokeToHub: true}

	for idx, rule := range rules {
		rr := RuleResult{Index: rule.SourceIndex, Strategy: rule.Strategy}
		if rr.Index == 0 && idx != 0 {
			rr.Index = idx
		}

		var h2sOp, s2hOp Op
		var lossless LosslessVerdict
		var ruleDiags []Diagnostic

		switch p := rule.Params.(type) {
		case FieldRenameParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveFieldRename(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.HubPath.String()}
			rr.SpokePaths = []string{p.SpokePath.String()}

		case ScalarToObjectParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveScalarToObject(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.HubPath.String()}
			rr.SpokePaths = []string{p.SpokePath.String()}

		case ObjectToScalarParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveObjectToScalar(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.HubPath.String()}
			rr.SpokePaths = []string{p.SpokePath.String()}

		case SingletonArrayToObjectParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveSingletonArrayToObject(idx, p.HubPath, p.SpokePath, hub, spoke, claimedHub, claimedSpoke, true)
			rr.HubPaths = []string{p.HubPath.String()}
			rr.SpokePaths = []string{p.SpokePath.String()}

		case ObjectToSingletonArrayParams:
			// Structural mirror: hub is the object, spoke is the array.
			var lossless2 LosslessVerdict
			s2hOp, h2sOp, lossless2, ruleDiags = resolveSingletonArrayToObject(idx, p.SpokePath, p.HubPath, spoke, hub, claimedSpoke, claimedHub, false)
			lossless = LosslessVerdict{HubToSpoke: lossless2.SpokeToHub, SpokeToHub: lossless2.HubToSpoke}
			rr.HubPaths = []string{p.HubPath.String()}
			rr.SpokePaths = []string{p.SpokePath.String()}

		case FieldsToMapParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveFieldsToMap(idx, p, hub, spoke, claimedHub, claimedSpoke)
			for _, hp := range p.HubPaths {
				rr.HubPaths = append(rr.HubPaths, hp.String())
			}
			rr.SpokePaths = []string{p.SpokeMapPath.String()}

		case MapToFieldsParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveMapToFields(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.HubMapPath.String()}
			for _, sp := range p.SpokePaths {
				rr.SpokePaths = append(rr.SpokePaths, sp.String())
			}

		case ToMetadataParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveToMetadata(idx, rule.Strategy, p, hub, claimedHub)
			rr.HubPaths = []string{p.HubPath.String()}

		case EnumRemapParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveEnumRemap(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.Path.String()}
			rr.SpokePaths = []string{p.Path.String()}

		case DefaultValueParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveDefaultValue(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.Path.String()}
			rr.SpokePaths = []string{p.Path.String()}

		case ConstantParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveConstant(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.Path.String()}
			rr.SpokePaths = []string{p.Path.String()}

		case DeleteParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveDelete(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.Path.String()}
			rr.SpokePaths = []string{p.Path.String()}

		case JSONPatchParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveJSONPatch(idx, p)

		case ForEachParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveForEach(idx, p, hub, spoke, claimedHub, claimedSpoke, policy, depth)
			rr.HubPaths = []string{p.HubItemsPath.String()}
			rr.SpokePaths = []string{p.SpokeItemsPath.String()}

		default:
			ruleDiags = append(ruleDiags, errorf(idx, "rule %d: unknown or unset strategy params", idx))
		}

		if !lossless.HubToSpoke && !rule.AcknowledgeLossy {
			ruleDiags = append(ruleDiags, errorf(idx, "rule %d (%s): hub->spoke conversion is lossy but acknowledgeLossy is not set", idx, rule.Strategy))
		}
		if !lossless.SpokeToHub && !rule.AcknowledgeLossy {
			ruleDiags = append(ruleDiags, errorf(idx, "rule %d (%s): spoke->hub conversion is lossy but acknowledgeLossy is not set", idx, rule.Strategy))
		}

		for _, d := range ruleDiags {
			if d.Severity == SeverityError {
				rr.Errors = append(rr.Errors, d.Message)
			} else {
				rr.Warnings = append(rr.Warnings, d.Message)
			}
		}
		rr.Lossless = lossless
		verdict = verdict.and(lossless)
		diags = append(diags, ruleDiags...)
		results = append(results, rr)

		if h2sOp != nil {
			h2sOps = append(h2sOps, h2sOp)
		}
		if s2hOp != nil {
			s2hOps = append(s2hOps, s2hOp)
		}
	}

	// Leftover-field scan: anything not claimed by a rule and not
	// structurally identical on both sides is an uncovered field —
	// "unknown means assume lossy, never silently pass."
	hubLeaves := flattenSchema(hub)
	spokeLeaves := flattenSchema(spoke)
	spokeByPath := map[string]LeafField{}
	for _, l := range spokeLeaves {
		spokeByPath[l.Path.String()] = l
	}

	for _, hl := range hubLeaves {
		key := hl.Path.String()
		if claimedHub[key] {
			continue
		}
		if sl, ok := spokeByPath[key]; ok && !claimedSpoke[key] && sl.Kind == hl.Kind && sl.Opaque == hl.Opaque {
			// Identical shape on both sides: auto-covered, no rule needed.
			p := hl.Path
			h2sOps = append(h2sOps, identityOp{path: p})
			s2hOps = append(s2hOps, identityOp{path: p})
			claimedHub[key] = true
			claimedSpoke[key] = true
			continue
		}
		if policy == UnmappedFieldPolicyWarn {
			diags = append(diags, warnf(-1, "hub field %q is not covered by any rule and has no identical counterpart in the spoke schema", key))
		} else {
			diags = append(diags, errorf(-1, "hub field %q is not covered by any rule and has no identical counterpart in the spoke schema", key))
			verdict.HubToSpoke = false
		}
	}
	for _, sl := range spokeLeaves {
		key := sl.Path.String()
		if claimedSpoke[key] {
			continue
		}
		if policy == UnmappedFieldPolicyWarn {
			diags = append(diags, warnf(-1, "spoke field %q is not covered by any rule and has no identical counterpart in the hub schema", key))
		} else {
			diags = append(diags, errorf(-1, "spoke field %q is not covered by any rule and has no identical counterpart in the hub schema", key))
			verdict.SpokeToHub = false
		}
	}

	return h2sOps, s2hOps, results, diags, verdict
}

// claim records that a single leaf path has been handled by a rule,
// returning an error diagnostic if it was already claimed by an earlier
// rule (two rules targeting the same field is a misconfiguration).
func claim(set map[string]bool, path FieldPath, idx int, side string) *Diagnostic {
	key := path.String()
	if set[key] {
		d := errorf(idx, "rule %d: %s field %q is already claimed by another rule", idx, side, key)
		return &d
	}
	set[key] = true
	return nil
}

// claimSubtree marks path and every leaf beneath it (per the schema node
// supplied) as claimed, for strategies that consume a whole object/map
// wholesale (e.g. the object side of ScalarToObject).
func claimSubtree(set map[string]bool, prefix FieldPath, node *extv1.JSONSchemaProps, idx int, side string) []Diagnostic {
	var diags []Diagnostic
	if d := claim(set, prefix, idx, side); d != nil {
		diags = append(diags, *d)
	}
	if node == nil {
		return diags
	}
	for _, leaf := range flattenSchema(node) {
		if len(leaf.Path) == 0 {
			// flattenSchema(node) emits a zero-length-path leaf when node
			// itself is a leaf (scalar, or opaque with no properties) —
			// that's the same field as prefix, already claimed above.
			continue
		}
		full := append(prefix.Clone(), leaf.Path...)
		if d := claim(set, full, idx, side); d != nil {
			diags = append(diags, *d)
		}
	}
	return diags
}

func resolveFieldRename(idx int, p FieldRenameParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	hubNode, err := lookupPath(hub, p.HubPath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (FieldRename): %v", idx, err))
	}
	spokeNode, err := lookupPath(spoke, p.SpokePath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (FieldRename): %v", idx, err))
	}
	if hubNode != nil && spokeNode != nil {
		hk, _ := classify(hubNode)
		sk, _ := classify(spokeNode)
		if hk != sk {
			diags = append(diags, errorf(idx, "rule %d (FieldRename): type mismatch: hub %q is %s, spoke %q is %s", idx, p.HubPath, hk, p.SpokePath, sk))
		}
	}
	if d := claim(claimedHub, p.HubPath, idx, "hub"); d != nil {
		diags = append(diags, *d)
	}
	if d := claim(claimedSpoke, p.SpokePath, idx, "spoke"); d != nil {
		diags = append(diags, *d)
	}
	return renameOp{from: p.HubPath, to: p.SpokePath}, renameOp{from: p.SpokePath, to: p.HubPath}, LosslessVerdict{true, true}, diags
}

func resolveScalarToObject(idx int, p ScalarToObjectParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	_, err := lookupPath(hub, p.HubPath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (ScalarToObject): hub: %v", idx, err))
	}
	spokeNode, err := lookupPath(spoke, p.SpokePath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (ScalarToObject): spoke: %v", idx, err))
	}
	lossless := LosslessVerdict{HubToSpoke: true, SpokeToHub: true}
	if spokeNode != nil {
		extra := extraKeys(spokeNode, p.Key)
		if len(extra) > 0 {
			lossless.SpokeToHub = false
		}
	}
	if d := claim(claimedHub, p.HubPath, idx, "hub"); d != nil {
		diags = append(diags, *d)
	}
	diags = append(diags, claimSubtree(claimedSpoke, p.SpokePath, spokeNode, idx, "spoke")...)
	h2s := wrapScalarOp{scalarPath: p.HubPath, objectPath: p.SpokePath, key: p.Key, defaults: p.DefaultsForOtherKeys}
	s2h := unwrapScalarOp{objectPath: p.SpokePath, scalarPath: p.HubPath, key: p.Key}
	return h2s, s2h, lossless, diags
}

func resolveObjectToScalar(idx int, p ObjectToScalarParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	hubNode, err := lookupPath(hub, p.HubPath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (ObjectToScalar): hub: %v", idx, err))
	}
	_, err = lookupPath(spoke, p.SpokePath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (ObjectToScalar): spoke: %v", idx, err))
	}
	lossless := LosslessVerdict{HubToSpoke: true, SpokeToHub: true}
	if hubNode != nil {
		extra := extraKeys(hubNode, p.Key)
		if len(extra) > 0 {
			lossless.HubToSpoke = false
		}
	}
	diags = append(diags, claimSubtree(claimedHub, p.HubPath, hubNode, idx, "hub")...)
	if d := claim(claimedSpoke, p.SpokePath, idx, "spoke"); d != nil {
		diags = append(diags, *d)
	}
	h2s := unwrapScalarOp{objectPath: p.HubPath, scalarPath: p.SpokePath, key: p.Key}
	s2h := wrapScalarOp{scalarPath: p.SpokePath, objectPath: p.HubPath, key: p.Key, defaults: p.DefaultsForOtherKeys}
	return h2s, s2h, lossless, diags
}

func extraKeys(objSchema *extv1.JSONSchemaProps, exclude string) []string {
	var out []string
	for k := range objSchema.Properties {
		if k != exclude {
			out = append(out, k)
		}
	}
	return out
}

// resolveSingletonArrayToObject handles both SingletonArrayToObject
// (arrayIsHub=true: arrayPath belongs to hub, objectPath to spoke) and, via
// the caller swapping arguments/claim-sets, the array side of
// ObjectToSingletonArray. It returns (arrayToObjectOp-direction,
// objectToArrayOp-direction, verdict-from-array-owner's-perspective).
func resolveSingletonArrayToObject(idx int, arrayPath, objectPath FieldPath, arraySchemaRoot, objectSchemaRoot *extv1.JSONSchemaProps, claimedArraySide, claimedObjectSide map[string]bool, arrayIsHub bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	arrayNode, err := lookupPath(arraySchemaRoot, arrayPath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d: array side: %v", idx, err))
	}
	objectNode, err := lookupPath(objectSchemaRoot, objectPath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d: object side: %v", idx, err))
	}
	arrayToObjectLossless := false
	if arrayNode != nil && arrayNode.MaxItems != nil && *arrayNode.MaxItems <= 1 {
		arrayToObjectLossless = true
	}
	if d := claim(claimedArraySide, arrayPath, idx, "array-side"); d != nil {
		diags = append(diags, *d)
	}
	diags = append(diags, claimSubtree(claimedObjectSide, objectPath, objectNode, idx, "object-side")...)

	arrayToObjectOp := arrayFirstToObjectOp{arrayPath: arrayPath, objectPath: objectPath}
	objectToArrayOp := objectToSingletonArrayOp{objectPath: objectPath, arrayPath: arrayPath}

	if arrayIsHub {
		// h2s = array(hub)->object(spoke); s2h = object(spoke)->array(hub)
		return arrayToObjectOp, objectToArrayOp, LosslessVerdict{HubToSpoke: arrayToObjectLossless, SpokeToHub: true}, diags
	}
	// arrayPath/objectPath belong to spoke/hub respectively in caller's
	// frame; caller remaps HubToSpoke/SpokeToHub afterward.
	return objectToArrayOp, arrayToObjectOp, LosslessVerdict{HubToSpoke: true, SpokeToHub: arrayToObjectLossless}, diags
}

func resolveFieldsToMap(idx int, p FieldsToMapParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	for _, hp := range p.HubPaths {
		if _, err := lookupPath(hub, hp); err != nil {
			diags = append(diags, errorf(idx, "rule %d (FieldsToMap): %v", idx, err))
		}
		if d := claim(claimedHub, hp, idx, "hub"); d != nil {
			diags = append(diags, *d)
		}
	}
	mapNode, err := lookupPath(spoke, p.SpokeMapPath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (FieldsToMap): %v", idx, err))
	}
	diags = append(diags, claimSubtree(claimedSpoke, p.SpokeMapPath, mapNode, idx, "spoke")...)

	onUnknown := p.OnUnknownSpokeKey
	if onUnknown == "" {
		onUnknown = UnknownKeyError
	}
	keyNames := keyNamesByPath(p.KeyNames)
	h2s := collapseToMapOp{fieldPaths: p.HubPaths, mapPath: p.SpokeMapPath, keyNames: keyNames}
	s2h := expandFromMapOp{mapPath: p.SpokeMapPath, fieldPaths: p.HubPaths, keyNames: keyNames, onUnknownKey: onUnknown}
	lossless := LosslessVerdict{HubToSpoke: true, SpokeToHub: onUnknown == UnknownKeyError}
	return h2s, s2h, lossless, diags
}

func resolveMapToFields(idx int, p MapToFieldsParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	mapNode, err := lookupPath(hub, p.HubMapPath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (MapToFields): %v", idx, err))
	}
	diags = append(diags, claimSubtree(claimedHub, p.HubMapPath, mapNode, idx, "hub")...)
	for _, sp := range p.SpokePaths {
		if _, err := lookupPath(spoke, sp); err != nil {
			diags = append(diags, errorf(idx, "rule %d (MapToFields): %v", idx, err))
		}
		if d := claim(claimedSpoke, sp, idx, "spoke"); d != nil {
			diags = append(diags, *d)
		}
	}
	onUnknown := p.OnUnknownHubKey
	if onUnknown == "" {
		onUnknown = UnknownKeyError
	}
	keyNames := keyNamesByPath(p.KeyNames)
	h2s := expandFromMapOp{mapPath: p.HubMapPath, fieldPaths: p.SpokePaths, keyNames: keyNames, onUnknownKey: onUnknown}
	s2h := collapseToMapOp{fieldPaths: p.SpokePaths, mapPath: p.HubMapPath, keyNames: keyNames}
	lossless := LosslessVerdict{HubToSpoke: onUnknown == UnknownKeyError, SpokeToHub: true}
	return h2s, s2h, lossless, diags
}

func keyNamesByPath(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func resolveToMetadata(idx int, strategy Strategy, p ToMetadataParams, hub *extv1.JSONSchemaProps, claimedHub map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	hubNode, err := lookupPath(hub, p.HubPath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (%s): %v", idx, strategy, err))
	}
	// Claim the whole subtree wholesale: the annotation/label captures
	// the hub field's value verbatim, whatever shape it is.
	diags = append(diags, claimSubtree(claimedHub, p.HubPath, hubNode, idx, "hub")...)
	metadataField := "annotations"
	if strategy == StrategyToLabel {
		metadataField = "labels"
	}
	serialization := p.Serialization
	if serialization == "" {
		serialization = "JSON"
	}
	h2s := stashAnnotationOp{hubPath: p.HubPath, metadataField: metadataField, key: p.Key, serialization: serialization}
	var s2h Op
	spokeToHubLossless := p.RestoreOnReverse
	if p.RestoreOnReverse {
		s2h = restoreAnnotationOp{hubPath: p.HubPath, metadataField: metadataField, key: p.Key, serialization: serialization}
	} else {
		s2h = stripMetadataKeyOp{metadataField: metadataField, key: p.Key}
	}
	return h2s, s2h, LosslessVerdict{HubToSpoke: true, SpokeToHub: spokeToHubLossless}, diags
}

func resolveEnumRemap(idx int, p EnumRemapParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	if _, err := lookupPath(hub, p.Path); err != nil {
		diags = append(diags, errorf(idx, "rule %d (EnumRemap): hub: %v", idx, err))
	}
	if _, err := lookupPath(spoke, p.Path); err != nil {
		diags = append(diags, errorf(idx, "rule %d (EnumRemap): spoke: %v", idx, err))
	}
	if d := claim(claimedHub, p.Path, idx, "hub"); d != nil {
		diags = append(diags, *d)
	}
	if d := claim(claimedSpoke, p.Path, idx, "spoke"); d != nil {
		diags = append(diags, *d)
	}

	hubToSpoke := map[string]string{}
	spokeToHub := map[string]string{}
	spokeSeen := map[string]int{}
	for _, m := range p.Mapping {
		hubToSpoke[m.Hub] = m.Spoke
		spokeToHub[m.Spoke] = m.Hub
		spokeSeen[m.Spoke]++
	}
	injective := true
	for _, n := range spokeSeen {
		if n > 1 {
			injective = false
		}
	}

	onUnmappedHub := p.OnUnmappedHubValue
	if onUnmappedHub == "" {
		onUnmappedHub = UnknownKeyError
	}
	onUnmappedSpoke := p.OnUnmappedSpokeValue
	if onUnmappedSpoke == "" {
		onUnmappedSpoke = UnknownKeyError
	}

	h2s := remapEnumOp{path: p.Path, table: hubToSpoke, onUnmapped: onUnmappedHub}
	s2h := remapEnumOp{path: p.Path, table: spokeToHub, onUnmapped: onUnmappedSpoke}

	lossless := LosslessVerdict{
		HubToSpoke: onUnmappedHub == UnknownKeyError,
		SpokeToHub: onUnmappedSpoke == UnknownKeyError && injective,
	}
	if !injective {
		diags = append(diags, warnf(idx, "rule %d (EnumRemap): mapping is not injective (multiple hub values map to the same spoke value); spoke->hub is ambiguous", idx))
	}
	return h2s, s2h, lossless, diags
}

func resolveDefaultValue(idx int, p DefaultValueParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	root := hub
	claimed := claimedHub
	side := "hub"
	if p.ExistsOn == SideSpoke {
		root = spoke
		claimed = claimedSpoke
		side = "spoke"
	}
	if _, err := lookupPath(root, p.Path); err != nil {
		diags = append(diags, errorf(idx, "rule %d (DefaultValue): %v", idx, err))
	}
	if d := claim(claimed, p.Path, idx, side); d != nil {
		diags = append(diags, *d)
	}
	inject := injectDefaultOp{path: p.Path, value: p.Default}
	drop := dropFieldOp{}
	if p.ExistsOn == SideSpoke {
		// Hub lacks the field: hub->spoke injects the default; spoke->hub drops the real value.
		return inject, drop, LosslessVerdict{HubToSpoke: true, SpokeToHub: false}, diags
	}
	// Spoke lacks the field: spoke->hub injects the default; hub->spoke drops the real value.
	return drop, inject, LosslessVerdict{HubToSpoke: false, SpokeToHub: true}, diags
}

func resolveConstant(idx int, p ConstantParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	root := hub
	claimed := claimedHub
	side := "hub"
	if p.ExistsOn == SideSpoke {
		root = spoke
		claimed = claimedSpoke
		side = "spoke"
	}
	if _, err := lookupPath(root, p.Path); err != nil {
		diags = append(diags, errorf(idx, "rule %d (Constant): %v", idx, err))
	}
	if d := claim(claimed, p.Path, idx, side); d != nil {
		diags = append(diags, *d)
	}
	inject := injectConstantOp{path: p.Path, value: p.Value}
	drop := dropFieldOp{}
	if p.ExistsOn == SideSpoke {
		return inject, drop, LosslessVerdict{HubToSpoke: true, SpokeToHub: false}, diags
	}
	return drop, inject, LosslessVerdict{HubToSpoke: false, SpokeToHub: true}, diags
}

func resolveDelete(idx int, p DeleteParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	root := hub
	claimed := claimedHub
	side := "hub"
	if p.ExistsOn == SideSpoke {
		root = spoke
		claimed = claimedSpoke
		side = "spoke"
	}
	node, err := lookupPath(root, p.Path)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (Delete): %v", idx, err))
	}
	if node != nil {
		parent := p.Path[:len(p.Path)-1]
		parentNode, perr := lookupPath(root, parent)
		if perr == nil && parentNode != nil {
			for _, r := range parentNode.Required {
				if r == lastSegment(p.Path) {
					diags = append(diags, warnf(idx, "rule %d (Delete): %q is required on %s; converted objects on that side will fail apiserver validation unless the schema also declares a default", idx, p.Path, side))
				}
			}
		}
	}
	if d := claim(claimed, p.Path, idx, side); d != nil {
		diags = append(diags, *d)
	}
	drop := dropFieldOp{}
	noop := dropFieldOp{}
	if p.ExistsOn == SideHub {
		// hub->spoke discards real hub data (lossy); spoke->hub is a no-op (nothing to lose).
		return drop, noop, LosslessVerdict{HubToSpoke: false, SpokeToHub: true}, diags
	}
	// spoke->hub discards real spoke data (lossy); hub->spoke is a no-op.
	return noop, drop, LosslessVerdict{HubToSpoke: true, SpokeToHub: false}, diags
}

func resolveJSONPatch(idx int, p JSONPatchParams) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	h2sPatch, err := parsePatch(p.HubToSpoke)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (JSONPatch): hubToSpoke: %v", idx, err))
	}
	s2hPatch, err := parsePatch(p.SpokeToHub)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (JSONPatch): spokeToHub: %v", idx, err))
	}
	diags = append(diags, warnf(idx, "rule %d (JSONPatch): the engine cannot statically verify field coverage for JSON Patch rules; ensure fields it touches aren't independently flagged as uncovered", idx))
	lossless := LosslessVerdict{HubToSpoke: p.LosslessOverride, SpokeToHub: p.LosslessOverride}
	return jsonPatchOp{patch: h2sPatch}, jsonPatchOp{patch: s2hPatch}, lossless, diags
}

func parsePatch(ops []JSONPatchOp) (jsonpatch.Patch, error) {
	if len(ops) == 0 {
		return jsonpatch.Patch{}, nil
	}
	raw := make([]map[string]any, 0, len(ops))
	for _, o := range ops {
		m := map[string]any{"op": o.Op, "path": o.Path}
		if o.From != "" {
			m["from"] = o.From
		}
		if o.Value != nil {
			m["value"] = o.Value
		}
		raw = append(raw, m)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return jsonpatch.DecodePatch(b)
}

func resolveForEach(idx int, p ForEachParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool, policy UnmappedFieldPolicy, depth int) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	if depth >= maxForEachDepth {
		diags = append(diags, errorf(idx, "rule %d (ForEach): nesting depth exceeds the supported maximum of %d", idx, maxForEachDepth))
		return nil, nil, LosslessVerdict{true, true}, diags
	}
	hubArray, err := lookupPath(hub, p.HubItemsPath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (ForEach): hub: %v", idx, err))
	}
	spokeArray, err := lookupPath(spoke, p.SpokeItemsPath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (ForEach): spoke: %v", idx, err))
	}
	if d := claim(claimedHub, p.HubItemsPath, idx, "hub"); d != nil {
		diags = append(diags, *d)
	}
	if d := claim(claimedSpoke, p.SpokeItemsPath, idx, "spoke"); d != nil {
		diags = append(diags, *d)
	}
	if hubArray == nil || spokeArray == nil || hubArray.Items == nil || hubArray.Items.Schema == nil || spokeArray.Items == nil || spokeArray.Items.Schema == nil {
		diags = append(diags, errorf(idx, "rule %d (ForEach): both items paths must resolve to arrays with a known item schema", idx))
		return nil, nil, LosslessVerdict{true, true}, diags
	}
	nestedH2S, nestedS2H, _, nestedDiags, nestedVerdict := resolveAndBuildOps(p.Rules, hubArray.Items.Schema, spokeArray.Items.Schema, policy, depth+1)
	for _, d := range nestedDiags {
		d.Message = fmt.Sprintf("rule %d (ForEach) item: %s", idx, d.Message)
		diags = append(diags, d)
	}
	h2s := forEachOp{srcItemsPath: p.HubItemsPath, dstItemsPath: p.SpokeItemsPath, nested: nestedH2S}
	s2h := forEachOp{srcItemsPath: p.SpokeItemsPath, dstItemsPath: p.HubItemsPath, nested: nestedS2H}
	return h2s, s2h, nestedVerdict, diags
}
