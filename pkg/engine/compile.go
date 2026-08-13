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

package engine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	jsonpatch "github.com/evanphx/json-patch/v5"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
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

// MaxForEachDepth is the maximum ForEach nesting allowed: a ForEach may
// contain another ForEach (arrays-of-arrays), but a third level is
// rejected at compile/admission time. Deeper nesting is not supported
// because coverage analysis and the CRD's schemaless nested-rule list
// would otherwise recurse without a bound, and real XR schemas rarely
// need more than two array levels.
const MaxForEachDepth = 2

// resolveAndBuildOps is the shared core used both at the top level and
// recursively for ForEach's nested rule list. depth tracks ForEach nesting
// to enforce MaxForEachDepth.
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
			// resolveSingletonArrayToObject already returns ops and a
			// verdict in true (HubToSpoke, SpokeToHub) order regardless of
			// which side owns the array, so no remapping is needed here.
			h2sOp, s2hOp, lossless, ruleDiags = resolveSingletonArrayToObject(idx, p.SpokePath, p.HubPath, spoke, hub, claimedSpoke, claimedHub, false)
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

		case FromMetadataParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveFromMetadata(idx, rule.Strategy, p, spoke, claimedSpoke)
			rr.SpokePaths = []string{p.SpokePath.String()}

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
			h2sOp, s2hOp, lossless, ruleDiags = resolveJSONPatch(idx, p, claimedHub, claimedSpoke)

		case ForEachParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveForEach(idx, p, hub, spoke, claimedHub, claimedSpoke, policy, depth)
			rr.HubPaths = []string{p.HubItemsPath.String()}
			rr.SpokePaths = []string{p.SpokeItemsPath.String()}

		case TypeCoerceParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveTypeCoerce(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.Path.String()}
			rr.SpokePaths = []string{p.Path.String()}

		case ScalarToFieldsParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveScalarToFields(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.HubPath.String()}
			for _, sp := range p.SpokeFields {
				rr.SpokePaths = append(rr.SpokePaths, sp.String())
			}

		case FieldsToScalarParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveFieldsToScalar(idx, p, hub, spoke, claimedHub, claimedSpoke)
			for _, hp := range p.HubFields {
				rr.HubPaths = append(rr.HubPaths, hp.String())
			}
			rr.SpokePaths = []string{p.SpokePath.String()}

		case ArrayToMapByKeyParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveArrayToMapByKey(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.HubPath.String()}
			rr.SpokePaths = []string{p.SpokePath.String()}

		case MapToArrayByKeyParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveMapToArrayByKey(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.HubPath.String()}
			rr.SpokePaths = []string{p.SpokePath.String()}

		case NumericScaleParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveNumericScale(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.HubPath.String()}
			rr.SpokePaths = []string{p.SpokePath.String()}

		case ListJoinParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveListJoin(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.HubPath.String()}
			rr.SpokePaths = []string{p.SpokePath.String()}

		case ListSplitParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveListSplit(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.HubPath.String()}
			rr.SpokePaths = []string{p.SpokePath.String()}

		case QuantityParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveQuantity(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.HubPath.String()}
			rr.SpokePaths = []string{p.SpokePath.String()}

		case DurationParams:
			h2sOp, s2hOp, lossless, ruleDiags = resolveDuration(idx, p, hub, spoke, claimedHub, claimedSpoke)
			rr.HubPaths = []string{p.HubPath.String()}
			rr.SpokePaths = []string{p.SpokePath.String()}

		default:
			ruleDiags = append(ruleDiags, errorf(idx, "rule %d: unknown or unset strategy params", idx))
		}

		if !lossless.HubToSpoke && !rule.AcknowledgeLossy {
			ruleDiags = append(ruleDiags, errorf(idx, "rule %d (%s): hub->spoke conversion is lossy but acknowledgeLossy is not set", idx, rule.Strategy))
		}
		if !lossless.SpokeToHub && !rule.AcknowledgeLossy {
			ruleDiags = append(ruleDiags, errorf(idx, "rule %d (%s): spoke->hub conversion is lossy but acknowledgeLossy is not set", idx, rule.Strategy))
		}

		if rule.When != nil {
			if len(rule.When.Path) == 0 {
				ruleDiags = append(ruleDiags, errorf(idx, "rule %d (%s): when.path is required", idx, rule.Strategy))
			} else {
				ruleDiags = append(ruleDiags, warnf(idx, "rule %d (%s): applies only when %q equals %#v; coverage of its target paths is partial", idx, rule.Strategy, rule.When.Path, rule.When.Equals))
				if h2sOp != nil {
					h2sOp = whenOp{path: rule.When.Path, equals: rule.When.Equals, inner: h2sOp}
				}
				if s2hOp != nil {
					s2hOp = whenOp{path: rule.When.Path, equals: rule.When.Equals, inner: s2hOp}
				}
			}
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
		if sl, ok := spokeByPath[key]; ok && !claimedSpoke[key] && sl.Kind == hl.Kind && sl.Opaque == hl.Opaque && sl.Construct == hl.Construct {
			// Identical shape on both sides: auto-covered, no rule needed.
			p := hl.Path
			h2sOps = append(h2sOps, identityOp{path: p})
			s2hOps = append(s2hOps, identityOp{path: p})
			claimedHub[key] = true
			claimedSpoke[key] = true
			continue
		}
		sev := SeverityError
		if policy == UnmappedFieldPolicyWarn {
			sev = SeverityWarning
		} else {
			verdict.HubToSpoke = false
		}
		diags = append(diags, uncoveredFieldDiag(sev, UncoveredSideHub, key, hl.Construct))
	}
	for _, sl := range spokeLeaves {
		key := sl.Path.String()
		if claimedSpoke[key] {
			continue
		}
		sev := SeverityError
		if policy == UnmappedFieldPolicyWarn {
			sev = SeverityWarning
		} else {
			verdict.SpokeToHub = false
		}
		diags = append(diags, uncoveredFieldDiag(sev, UncoveredSideSpoke, key, sl.Construct))
	}

	// A field the schema never declares at all (on either side) never
	// appears in hubLeaves/spokeLeaves above, so nothing claims it and no
	// diagnostic ever flags it -- it's invisible to everything else in
	// this function. Append a catch-all passthrough, last in each
	// direction's op list, so such a field is copied through unchanged
	// instead of silently dropped by Convert's empty starting output. Built
	// from the union of both schemas, not just the direction's source --
	// see mergeKnownTrees' doc comment for why using the source schema
	// alone would let this Op clobber a rule's freshly-written destination
	// field with stale same-named data from the source side.
	passthroughTree := mergeKnownTrees(buildKnownTree(hub), buildKnownTree(spoke))
	h2sOps = append(h2sOps, passthroughUnknownOp{tree: passthroughTree})
	s2hOps = append(s2hOps, passthroughUnknownOp{tree: passthroughTree})

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
	arrayToObjectLossless := arrayNode != nil && arrayNode.MaxItems != nil && *arrayNode.MaxItems <= 1
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
	if metadataField == "labels" {
		if msgs := k8svalidation.IsQualifiedName(p.Key); len(msgs) > 0 {
			diags = append(diags, errorf(idx, "rule %d (%s): invalid label key %q: %s", idx, strategy, p.Key, strings.Join(msgs, "; ")))
		}
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

// resolveFromMetadata is the geometric inverse of resolveToMetadata: the
// schema field lives on the spoke, and the stash key lives on hub metadata.
// Runtime ops are reused with the spoke field path substituted for hubPath.
func resolveFromMetadata(idx int, strategy Strategy, p FromMetadataParams, spoke *extv1.JSONSchemaProps, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	spokeNode, err := lookupPath(spoke, p.SpokePath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (%s): %v", idx, strategy, err))
	}
	diags = append(diags, claimSubtree(claimedSpoke, p.SpokePath, spokeNode, idx, "spoke")...)
	metadataField := "annotations"
	if strategy == StrategyFromLabel {
		metadataField = "labels"
	}
	serialization := p.Serialization
	if strategy == StrategyFromLabel {
		if serialization == "JSON" {
			diags = append(diags, errorf(idx, "rule %d (FromLabel): serialization=JSON is not supported for labels; use String", idx))
		}
		if serialization == "" {
			serialization = "String"
		}
	} else if serialization == "" {
		serialization = "JSON"
	}
	if metadataField == "labels" {
		if msgs := k8svalidation.IsQualifiedName(p.Key); len(msgs) > 0 {
			diags = append(diags, errorf(idx, "rule %d (%s): invalid label key %q: %s", idx, strategy, p.Key, strings.Join(msgs, "; ")))
		}
	}
	// Hub→spoke: restore the spoke field from hub metadata.
	h2s := restoreAnnotationOp{hubPath: p.SpokePath, metadataField: metadataField, key: p.Key, serialization: serialization}
	var s2h Op
	spokeToHubLossless := p.StashOnReverse
	if p.StashOnReverse {
		// Spoke→hub: stash the spoke field onto hub metadata.
		s2h = stashAnnotationOp{hubPath: p.SpokePath, metadataField: metadataField, key: p.Key, serialization: serialization}
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

	hubToSpoke := map[string]any{}
	spokeToHub := map[string]any{}
	spokeSeen := map[string]int{}
	for _, m := range p.Mapping {
		hubToSpoke[enumKey(m.Hub)] = m.Spoke
		spokeToHub[enumKey(m.Spoke)] = m.Hub
		spokeSeen[enumKey(m.Spoke)]++
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

// resolveJSONPatch handles the escape-hatch strategy. The engine can't
// statically verify what an arbitrary patch does, so lossiness always
// comes from the author's own LosslessOverride flag — but coverage is
// still tracked, best-effort: every "path"/"from" pointer mentioned by
// either direction's patch is claimed on BOTH the hub and spoke side. This
// intentionally over-claims (a path only ever mentioned in the
// spoke-to-hub patch still gets claimed on the hub side too) rather than
// leaving JSON-Patch-touched fields permanently "uncovered" — appropriate
// for a strategy whose entire nature is "trust the admin," while a
// genuine conflict with another rule targeting the same path is still
// caught, since claiming still goes through the same claim() bookkeeping
// every other strategy uses.
func resolveJSONPatch(idx int, p JSONPatchParams, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	h2sPatch, h2sDest, h2sAll, err := parsePatch(p.HubToSpoke)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (JSONPatch): hubToSpoke: %v", idx, err))
	}
	s2hPatch, s2hDest, s2hAll, err := parsePatch(p.SpokeToHub)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (JSONPatch): spokeToHub: %v", idx, err))
	}
	diags = append(diags, warnf(idx, "rule %d (JSONPatch): the engine cannot verify what this patch actually does; correctness and coverage are the author's responsibility", idx))

	// Claim every path either direction's patch mentions ("path" or
	// "from") on BOTH sides — see the doc comment above for why this
	// deliberately over-claims rather than under-claims.
	seen := map[string]bool{}
	for _, path := range append(append([]FieldPath{}, h2sAll...), s2hAll...) {
		key := path.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		if d := claim(claimedHub, path, idx, "hub"); d != nil {
			diags = append(diags, *d)
		}
		if d := claim(claimedSpoke, path, idx, "spoke"); d != nil {
			diags = append(diags, *d)
		}
	}

	lossless := LosslessVerdict{HubToSpoke: p.LosslessOverride, SpokeToHub: p.LosslessOverride}
	return jsonPatchOp{patch: h2sPatch, touchedPaths: h2sDest}, jsonPatchOp{patch: s2hPatch, touchedPaths: s2hDest}, lossless, diags
}

// parsePatch decodes ops into an executable jsonpatch.Patch, plus two path
// sets: destPaths (just "path" — the add/replace/move/copy/remove
// destination) is what the execution-time Op copies out of the patched
// document into the shared output tree; allPaths (both "path" and "from",
// deduped) is the broader set resolveJSONPatch uses for best-effort
// coverage claiming, since a "from" (e.g. a move's source) is a real field
// reference too even though it's never itself a destination.
func parsePatch(ops []JSONPatchOp) (patch jsonpatch.Patch, destPaths, allPaths []FieldPath, err error) {
	if len(ops) == 0 {
		return jsonpatch.Patch{}, nil, nil, nil
	}
	raw := make([]map[string]any, 0, len(ops))
	seenDest, seenAll := map[string]bool{}, map[string]bool{}
	addAll := func(ptr string) {
		if ptr == "" || seenAll[ptr] {
			return
		}
		seenAll[ptr] = true
		allPaths = append(allPaths, FieldPathFromJSONPointer(ptr))
	}
	for _, o := range ops {
		m := map[string]any{"op": o.Op, "path": o.Path}
		if o.From != "" {
			m["from"] = o.From
		}
		if o.Value != nil {
			m["value"] = o.Value
		}
		raw = append(raw, m)
		if !seenDest[o.Path] {
			seenDest[o.Path] = true
			destPaths = append(destPaths, FieldPathFromJSONPointer(o.Path))
		}
		addAll(o.Path)
		addAll(o.From)
	}
	b, marshalErr := json.Marshal(raw)
	if marshalErr != nil {
		return nil, nil, nil, marshalErr
	}
	patch, err = jsonpatch.DecodePatch(b)
	return patch, destPaths, allPaths, err
}

func resolveForEach(idx int, p ForEachParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool, policy UnmappedFieldPolicy, depth int) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	if depth >= MaxForEachDepth {
		diags = append(diags, errorf(idx, "rule %d (ForEach): nesting depth exceeds the supported maximum of %d", idx, MaxForEachDepth))
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
		// Nested analysis uses item-relative FieldPaths; qualify uncovered
		// paths with the ForEach items path so status FieldsUncovered*
		// reports the full hub/spoke location (e.g. "readReplicas.name").
		switch d.UncoveredSide {
		case UncoveredSideHub:
			d.FieldPath = qualifyUnderItemsPath(p.HubItemsPath, d.FieldPath)
		case UncoveredSideSpoke:
			d.FieldPath = qualifyUnderItemsPath(p.SpokeItemsPath, d.FieldPath)
		}
		diags = append(diags, d)
	}
	h2s := forEachOp{srcItemsPath: p.HubItemsPath, dstItemsPath: p.SpokeItemsPath, nested: nestedH2S}
	s2h := forEachOp{srcItemsPath: p.SpokeItemsPath, dstItemsPath: p.HubItemsPath, nested: nestedS2H}
	return h2s, s2h, nestedVerdict, diags
}

// qualifyUnderItemsPath prefixes an item-relative uncovered FieldPath with
// the ForEach items path. Nested ForEach calls qualify at each level so
// deeply nested paths accumulate the full hub/spoke location.
func qualifyUnderItemsPath(itemsPath FieldPath, relative string) string {
	if relative == "" {
		return itemsPath.String()
	}
	if len(itemsPath) == 0 {
		return relative
	}
	return itemsPath.String() + "." + relative
}

func resolveTypeCoerce(idx int, p TypeCoerceParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	hubKind := FieldKindUnknown
	if hubNode, err := lookupPath(hub, p.Path); err != nil {
		diags = append(diags, errorf(idx, "rule %d (TypeCoerce): hub: %v", idx, err))
	} else {
		hubKind, _ = classify(hubNode)
		if !isCoercibleScalar(hubKind) {
			diags = append(diags, errorf(idx, "rule %d (TypeCoerce): hub field %q is %s, not a coercible scalar type", idx, p.Path, hubKind))
		}
	}
	spokeKind := FieldKindUnknown
	if spokeNode, err := lookupPath(spoke, p.Path); err != nil {
		diags = append(diags, errorf(idx, "rule %d (TypeCoerce): spoke: %v", idx, err))
	} else {
		spokeKind, _ = classify(spokeNode)
		if !isCoercibleScalar(spokeKind) {
			diags = append(diags, errorf(idx, "rule %d (TypeCoerce): spoke field %q is %s, not a coercible scalar type", idx, p.Path, spokeKind))
		}
	}
	if d := claim(claimedHub, p.Path, idx, "hub"); d != nil {
		diags = append(diags, *d)
	}
	if d := claim(claimedSpoke, p.Path, idx, "spoke"); d != nil {
		diags = append(diags, *d)
	}
	h2s := coerceOp{path: p.Path, toKind: spokeKind}
	s2h := coerceOp{path: p.Path, toKind: hubKind}
	return h2s, s2h, LosslessVerdict{true, true}, diags
}

// resolveScalarToFields and resolveFieldsToScalar share the same
// validation shape (compile the pattern, parse the template, resolve
// every named field's schema kind), so compileSplitJoin does the common
// work once and each caller wires the ops for its own direction.
func compileSplitJoin(idx int, strategy Strategy, pattern, joinTemplate string, fields map[string]FieldPath, fieldsRoot *extv1.JSONSchemaProps, claimed map[string]bool, side string) (*regexp.Regexp, *template.Template, map[string]FieldKind, []Diagnostic) {
	var diags []Diagnostic
	re, err := regexp.Compile(pattern)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (%s): invalid pattern: %v", idx, strategy, err))
	}
	tmpl, err := template.New("join").Option("missingkey=error").Parse(joinTemplate)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (%s): invalid joinTemplate: %v", idx, strategy, err))
	}
	if re != nil {
		declared := map[string]bool{}
		for _, name := range re.SubexpNames() {
			if name != "" {
				declared[name] = true
			}
		}
		for name := range fields {
			if !declared[name] {
				diags = append(diags, errorf(idx, "rule %d (%s): field %q has no matching named capture group in pattern", idx, strategy, name))
			}
		}
	}
	kinds := map[string]FieldKind{}
	for name, path := range fields {
		node, err := lookupPath(fieldsRoot, path)
		if err != nil {
			diags = append(diags, errorf(idx, "rule %d (%s): %s: %v", idx, strategy, side, err))
			continue
		}
		kind, _ := classify(node)
		if !isCoercibleScalar(kind) {
			diags = append(diags, errorf(idx, "rule %d (%s): %s field %q is %s, not a coercible scalar type", idx, strategy, side, path, kind))
		}
		kinds[name] = kind
		if d := claim(claimed, path, idx, side); d != nil {
			diags = append(diags, *d)
		}
	}
	return re, tmpl, kinds, diags
}

// resolveScalarToFields handles the escape-hatch strategy pair described
// on ScalarToFieldsParams. Like JSONPatch, the engine cannot verify that
// Pattern and JoinTemplate are true inverses of each other, so lossiness
// always comes from the author's own LosslessOverride flag.
func resolveScalarToFields(idx int, p ScalarToFieldsParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	if _, err := lookupPath(hub, p.HubPath); err != nil {
		diags = append(diags, errorf(idx, "rule %d (ScalarToFields): hub: %v", idx, err))
	}
	if d := claim(claimedHub, p.HubPath, idx, "hub"); d != nil {
		diags = append(diags, *d)
	}
	re, tmpl, kinds, splitDiags := compileSplitJoin(idx, StrategyScalarToFields, p.Pattern, p.JoinTemplate, p.SpokeFields, spoke, claimedSpoke, "spoke")
	diags = append(diags, splitDiags...)
	diags = append(diags, warnf(idx, "rule %d (ScalarToFields): the engine cannot verify pattern/joinTemplate are true inverses; correctness and coverage are the author's responsibility", idx))

	var h2s, s2h Op
	if re != nil {
		h2s = splitFieldOp{sourcePath: p.HubPath, pattern: re, destFields: p.SpokeFields, destKinds: kinds}
	}
	if tmpl != nil {
		s2h = joinFieldsOp{srcFields: p.SpokeFields, destPath: p.HubPath, tmpl: tmpl}
	}
	lossless := LosslessVerdict{HubToSpoke: p.LosslessOverride, SpokeToHub: p.LosslessOverride}
	return h2s, s2h, lossless, diags
}

// resolveFieldsToScalar is the structural mirror of resolveScalarToFields.
func resolveFieldsToScalar(idx int, p FieldsToScalarParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	if _, err := lookupPath(spoke, p.SpokePath); err != nil {
		diags = append(diags, errorf(idx, "rule %d (FieldsToScalar): spoke: %v", idx, err))
	}
	if d := claim(claimedSpoke, p.SpokePath, idx, "spoke"); d != nil {
		diags = append(diags, *d)
	}
	re, tmpl, kinds, splitDiags := compileSplitJoin(idx, StrategyFieldsToScalar, p.Pattern, p.JoinTemplate, p.HubFields, hub, claimedHub, "hub")
	diags = append(diags, splitDiags...)
	diags = append(diags, warnf(idx, "rule %d (FieldsToScalar): the engine cannot verify pattern/joinTemplate are true inverses; correctness and coverage are the author's responsibility", idx))

	var h2s, s2h Op
	if tmpl != nil {
		h2s = joinFieldsOp{srcFields: p.HubFields, destPath: p.SpokePath, tmpl: tmpl}
	}
	if re != nil {
		s2h = splitFieldOp{sourcePath: p.SpokePath, pattern: re, destFields: p.HubFields, destKinds: kinds}
	}
	lossless := LosslessVerdict{HubToSpoke: p.LosslessOverride, SpokeToHub: p.LosslessOverride}
	return h2s, s2h, lossless, diags
}

// resolveArrayToMapByKey: array->map is lossless (modulo the runtime
// duplicate/missing-key errors arrayToMapByKeyOp itself reports);
// map->array is always treated as lossy since the reconstructed array is
// sorted by key rather than reproducing whatever order the original had.
func resolveArrayToMapByKey(idx int, p ArrayToMapByKeyParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	if _, err := lookupPath(hub, p.HubPath); err != nil {
		diags = append(diags, errorf(idx, "rule %d (ArrayToMapByKey): hub: %v", idx, err))
	}
	if _, err := lookupPath(spoke, p.SpokePath); err != nil {
		diags = append(diags, errorf(idx, "rule %d (ArrayToMapByKey): spoke: %v", idx, err))
	}
	if d := claim(claimedHub, p.HubPath, idx, "hub"); d != nil {
		diags = append(diags, *d)
	}
	if d := claim(claimedSpoke, p.SpokePath, idx, "spoke"); d != nil {
		diags = append(diags, *d)
	}
	h2s := arrayToMapByKeyOp{arrayPath: p.HubPath, mapPath: p.SpokePath, keyField: p.KeyField}
	s2h := mapToArrayByKeyOp{mapPath: p.SpokePath, arrayPath: p.HubPath, keyField: p.KeyField}
	return h2s, s2h, LosslessVerdict{HubToSpoke: true, SpokeToHub: false}, diags
}

// resolveMapToArrayByKey is the structural mirror of
// resolveArrayToMapByKey: the hub is the map, the spoke is the array.
func resolveMapToArrayByKey(idx int, p MapToArrayByKeyParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	if _, err := lookupPath(hub, p.HubPath); err != nil {
		diags = append(diags, errorf(idx, "rule %d (MapToArrayByKey): hub: %v", idx, err))
	}
	if _, err := lookupPath(spoke, p.SpokePath); err != nil {
		diags = append(diags, errorf(idx, "rule %d (MapToArrayByKey): spoke: %v", idx, err))
	}
	if d := claim(claimedHub, p.HubPath, idx, "hub"); d != nil {
		diags = append(diags, *d)
	}
	if d := claim(claimedSpoke, p.SpokePath, idx, "spoke"); d != nil {
		diags = append(diags, *d)
	}
	h2s := mapToArrayByKeyOp{mapPath: p.HubPath, arrayPath: p.SpokePath, keyField: p.KeyField}
	s2h := arrayToMapByKeyOp{arrayPath: p.SpokePath, mapPath: p.HubPath, keyField: p.KeyField}
	return h2s, s2h, LosslessVerdict{HubToSpoke: false, SpokeToHub: true}, diags
}

// resolveNumericScale: the direction landing on an integer-typed field is
// treated as lossy, since dividing/multiplying by Factor may not produce
// a whole number for every possible input even though any one sample
// value might happen to.
func resolveNumericScale(idx int, p NumericScaleParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	if p.Factor == 0 {
		diags = append(diags, errorf(idx, "rule %d (NumericScale): factor must not be zero", idx))
	}
	hubKind := FieldKindUnknown
	if hubNode, err := lookupPath(hub, p.HubPath); err != nil {
		diags = append(diags, errorf(idx, "rule %d (NumericScale): hub: %v", idx, err))
	} else {
		hubKind, _ = classify(hubNode)
		if hubKind != FieldKindInteger && hubKind != FieldKindNumber {
			diags = append(diags, errorf(idx, "rule %d (NumericScale): hub field %q is %s, not numeric", idx, p.HubPath, hubKind))
		}
	}
	spokeKind := FieldKindUnknown
	if spokeNode, err := lookupPath(spoke, p.SpokePath); err != nil {
		diags = append(diags, errorf(idx, "rule %d (NumericScale): spoke: %v", idx, err))
	} else {
		spokeKind, _ = classify(spokeNode)
		if spokeKind != FieldKindInteger && spokeKind != FieldKindNumber {
			diags = append(diags, errorf(idx, "rule %d (NumericScale): spoke field %q is %s, not numeric", idx, p.SpokePath, spokeKind))
		}
	}
	if d := claim(claimedHub, p.HubPath, idx, "hub"); d != nil {
		diags = append(diags, *d)
	}
	if d := claim(claimedSpoke, p.SpokePath, idx, "spoke"); d != nil {
		diags = append(diags, *d)
	}
	h2s := numericScaleOp{fromPath: p.HubPath, toPath: p.SpokePath, factor: p.Factor, multiply: false, round: spokeKind == FieldKindInteger}
	s2h := numericScaleOp{fromPath: p.SpokePath, toPath: p.HubPath, factor: p.Factor, multiply: true, round: hubKind == FieldKindInteger}
	lossless := LosslessVerdict{HubToSpoke: spokeKind == FieldKindNumber, SpokeToHub: hubKind == FieldKindNumber}
	return h2s, s2h, lossless, diags
}

// resolveListJoin: always lossless. An element that happens to contain
// Separator as a substring will fail to round-trip, which convctl test
// correctly surfaces as an unacknowledged loss — that's a genuine data
// problem, not a documented risk of this strategy the way pattern-based
// escape hatches are.
func resolveListJoin(idx int, p ListJoinParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	itemKind := FieldKindUnknown
	hubNode, err := lookupPath(hub, p.HubPath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (ListJoin): hub: %v", idx, err))
	} else {
		kind, _ := classify(hubNode)
		if kind != FieldKindArray || hubNode.Items == nil || hubNode.Items.Schema == nil {
			diags = append(diags, errorf(idx, "rule %d (ListJoin): hub field %q must be an array with a known item schema", idx, p.HubPath))
		} else {
			itemKind, _ = classify(hubNode.Items.Schema)
			if !isCoercibleScalar(itemKind) {
				diags = append(diags, errorf(idx, "rule %d (ListJoin): hub array %q items are %s, not a coercible scalar type", idx, p.HubPath, itemKind))
			}
		}
	}
	if spokeNode, err := lookupPath(spoke, p.SpokePath); err != nil {
		diags = append(diags, errorf(idx, "rule %d (ListJoin): spoke: %v", idx, err))
	} else if kind, _ := classify(spokeNode); kind != FieldKindString {
		diags = append(diags, errorf(idx, "rule %d (ListJoin): spoke field %q is %s, not a string", idx, p.SpokePath, kind))
	}
	if d := claim(claimedHub, p.HubPath, idx, "hub"); d != nil {
		diags = append(diags, *d)
	}
	if d := claim(claimedSpoke, p.SpokePath, idx, "spoke"); d != nil {
		diags = append(diags, *d)
	}
	h2s := joinListOp{arrayPath: p.HubPath, stringPath: p.SpokePath, separator: p.Separator}
	s2h := splitListOp{stringPath: p.SpokePath, arrayPath: p.HubPath, separator: p.Separator, itemKind: itemKind}
	return h2s, s2h, LosslessVerdict{true, true}, diags
}

// resolveListSplit is the structural mirror of resolveListJoin: the hub
// field is the delimited string, the spoke field is the array.
func resolveListSplit(idx int, p ListSplitParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	if hubNode, err := lookupPath(hub, p.HubPath); err != nil {
		diags = append(diags, errorf(idx, "rule %d (ListSplit): hub: %v", idx, err))
	} else if kind, _ := classify(hubNode); kind != FieldKindString {
		diags = append(diags, errorf(idx, "rule %d (ListSplit): hub field %q is %s, not a string", idx, p.HubPath, kind))
	}
	itemKind := FieldKindUnknown
	spokeNode, err := lookupPath(spoke, p.SpokePath)
	if err != nil {
		diags = append(diags, errorf(idx, "rule %d (ListSplit): spoke: %v", idx, err))
	} else {
		kind, _ := classify(spokeNode)
		if kind != FieldKindArray || spokeNode.Items == nil || spokeNode.Items.Schema == nil {
			diags = append(diags, errorf(idx, "rule %d (ListSplit): spoke field %q must be an array with a known item schema", idx, p.SpokePath))
		} else {
			itemKind, _ = classify(spokeNode.Items.Schema)
			if !isCoercibleScalar(itemKind) {
				diags = append(diags, errorf(idx, "rule %d (ListSplit): spoke array %q items are %s, not a coercible scalar type", idx, p.SpokePath, itemKind))
			}
		}
	}
	if d := claim(claimedHub, p.HubPath, idx, "hub"); d != nil {
		diags = append(diags, *d)
	}
	if d := claim(claimedSpoke, p.SpokePath, idx, "spoke"); d != nil {
		diags = append(diags, *d)
	}
	h2s := splitListOp{stringPath: p.HubPath, arrayPath: p.SpokePath, separator: p.Separator, itemKind: itemKind}
	s2h := joinListOp{arrayPath: p.SpokePath, stringPath: p.HubPath, separator: p.Separator}
	return h2s, s2h, LosslessVerdict{true, true}, diags
}

// resolveQuantity infers which side is the Quantity string from the
// schemas. String→millivalue is lossless; millivalue→canonical Quantity
// string is lossy ("0.5" and "500m" are the same Quantity).
func resolveQuantity(idx int, p QuantityParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	return resolveStringIntegerPair(idx, "Quantity", p.HubPath, p.SpokePath, hub, spoke, claimedHub, claimedSpoke, func(src, dst FieldPath, toInteger bool) Op {
		return quantityOp{src: src, dst: dst, toInteger: toInteger}
	})
}

// resolveDuration infers which side is the duration string. String→seconds
// is lossless; seconds→canonical duration string is lossy ("5m" vs "5m0s").
func resolveDuration(idx int, p DurationParams, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool) (Op, Op, LosslessVerdict, []Diagnostic) {
	return resolveStringIntegerPair(idx, "Duration", p.HubPath, p.SpokePath, hub, spoke, claimedHub, claimedSpoke, func(src, dst FieldPath, toInteger bool) Op {
		return durationOp{src: src, dst: dst, toInteger: toInteger}
	})
}

// resolveStringIntegerPair is the shared compile path for Quantity and
// Duration: exactly one side must be a string and the other an integer.
// The string→integer direction is lossless; integer→canonical string is
// not, because the formatter picks one of several equivalent spellings.
func resolveStringIntegerPair(idx int, name string, hubPath, spokePath FieldPath, hub, spoke *extv1.JSONSchemaProps, claimedHub, claimedSpoke map[string]bool, makeOp func(src, dst FieldPath, toInteger bool) Op) (Op, Op, LosslessVerdict, []Diagnostic) {
	var diags []Diagnostic
	hubKind := FieldKindUnknown
	if hubNode, err := lookupPath(hub, hubPath); err != nil {
		diags = append(diags, errorf(idx, "rule %d (%s): hub: %v", idx, name, err))
	} else {
		hubKind, _ = classify(hubNode)
	}
	spokeKind := FieldKindUnknown
	if spokeNode, err := lookupPath(spoke, spokePath); err != nil {
		diags = append(diags, errorf(idx, "rule %d (%s): spoke: %v", idx, name, err))
	} else {
		spokeKind, _ = classify(spokeNode)
	}
	hubStr, hubInt := hubKind == FieldKindString, hubKind == FieldKindInteger
	spokeStr, spokeInt := spokeKind == FieldKindString, spokeKind == FieldKindInteger
	if !((hubStr && spokeInt) || (hubInt && spokeStr)) {
		diags = append(diags, errorf(idx, "rule %d (%s): one side must be a string and the other an integer (hub %q is %s, spoke %q is %s)", idx, name, hubPath, hubKind, spokePath, spokeKind))
	}
	if d := claim(claimedHub, hubPath, idx, "hub"); d != nil {
		diags = append(diags, *d)
	}
	if d := claim(claimedSpoke, spokePath, idx, "spoke"); d != nil {
		diags = append(diags, *d)
	}
	if hubStr && spokeInt {
		return makeOp(hubPath, spokePath, true), makeOp(spokePath, hubPath, false), LosslessVerdict{HubToSpoke: true, SpokeToHub: false}, diags
	}
	if hubInt && spokeStr {
		return makeOp(hubPath, spokePath, false), makeOp(spokePath, hubPath, true), LosslessVerdict{HubToSpoke: false, SpokeToHub: true}, diags
	}
	return nil, nil, LosslessVerdict{}, diags
}
