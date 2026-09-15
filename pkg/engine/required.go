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
	"fmt"
	"sort"
	"strings"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// Diagnostic codes for required-field satisfaction. Stable identifiers,
// because callers branch on these rather than matching message text.
const (
	// CodeRequiredFieldUnsatisfiable: nothing writes a destination field
	// the schema requires. Every conversion in that direction produces an
	// object the apiserver rejects.
	CodeRequiredFieldUnsatisfiable = "RequiredFieldUnsatisfiable"
	// CodeRequiredFieldConditional: the only rule writing a required
	// destination field may not fire — it carries a `when` clause, or its
	// source is itself optional. The conversion succeeds for some inputs
	// and fails for others, which is the worst kind to diagnose in
	// production.
	CodeRequiredFieldConditional = "RequiredFieldConditional"
	// CodeRequiredFieldUnprovable: the writing rule is one whose output
	// the engine cannot reason about (CEL, JSONPatch, ScalarToFields).
	// Reported as a warning with a pointer at the empirical checks.
	CodeRequiredFieldUnprovable = "RequiredFieldUnprovable"
)

// requiredSatisfaction is the verdict for one required destination field.
type requiredSatisfaction int

// Ordered by how strong a guarantee each is, because bestVerdict takes the
// minimum across the rules writing a path. "Cannot predict the output of a
// rule that definitely fires" is a stronger position than "the rule may not
// fire at all", and the severities these map onto already say so: unprovable
// is a warning, conditional is an error.
const (
	satisfiedAlways requiredSatisfaction = iota
	satisfiedUnprovably
	satisfiedConditionally
	satisfiedNever
)

// unprovableStrategies are the strategies whose output the engine cannot
// predict. They are already always-lossy, and the honest verdict for a
// required field they write is "cannot prove", not "fails".
var unprovableStrategies = map[Strategy]bool{
	StrategyCEL:            true,
	StrategyJSONPatch:      true,
	StrategyScalarToFields: true,
}

// analyzeRequiredFields checks, for one direction, whether the rule set can
// always produce every field the destination schema requires.
//
// The engine already tracked required-ness and used it for exactly one
// thing: a warning when a Delete rule targeted a required field. The general
// question — can this rule set produce a valid object for *every* input? —
// went unasked, so a spoke requiring a field that no rule can always produce
// failed at admission for some objects and not others.
//
// Direction is named from the destination's point of view: destRequired are
// the destination schema's required leaves, and writes maps a destination
// path to the rules that write it.
func analyzeRequiredFields(destSchema *extv1.JSONSchemaProps, srcSchema *extv1.JSONSchemaProps, rules []Rule, results []RuleResult, destSide string) []Diagnostic {
	writes := map[string][]int{} // destination path -> rule indexes
	destPaths := make([]string, 0, len(results))
	for i, rr := range results {
		dest := rr.SpokePaths
		if destSide == "hub" {
			dest = rr.HubPaths
		}
		for _, p := range dest {
			writes[p] = append(writes[p], i)
			destPaths = append(destPaths, p)
		}
	}

	required := unconditionallyRequiredLeaves(destSchema, destPaths)
	if len(required) == 0 {
		return nil
	}

	srcRequired := map[string]bool{}
	for _, l := range flattenSchema(srcSchema) {
		if l.Required {
			srcRequired[l.Path.String()] = true
		}
	}

	var diags []Diagnostic
	for _, leaf := range required {
		path := leaf.Path.String()

		// The apiserver fills a missing field that has a default, so a
		// required leaf with one is satisfied no matter what the rules do.
		if leaf.Schema != nil && leaf.Schema.Default != nil {
			continue
		}

		idxs := writes[path]
		if len(idxs) == 0 {
			// An identical leaf on both sides is carried by passthrough,
			// and passthrough is unconditional — but only if the source
			// leaf is itself required, or nothing guarantees a value.
			if srcRequired[path] {
				continue
			}
			if leaf.ancestorWrittenWholesale {
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning, Code: CodeRequiredFieldUnprovable, RuleIndex: -1, FieldPath: path,
					Message: fmt.Sprintf("%s field %q is required, and the object containing it is produced whole by a rule rather than written into; whether that rule supplies this key cannot be determined from the plan. Verify empirically with `convctl test --validate-output` or `--fuzz`", destSide, path),
				})
				continue
			}
			diags = append(diags, Diagnostic{
				Severity: SeverityError, Code: CodeRequiredFieldUnsatisfiable, RuleIndex: -1, FieldPath: path,
				Message: fmt.Sprintf("%s field %q is required but no rule writes it and no source field of the same name is required; conversions in this direction produce objects the apiserver will reject", destSide, path),
			})
			continue
		}

		verdict, why, ruleIdx := bestVerdict(idxs, rules, results, srcRequired, destSide)
		switch verdict {
		case satisfiedAlways:
			// Nothing to report.
		case satisfiedUnprovably:
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning, Code: CodeRequiredFieldUnprovable, RuleIndex: ruleIdx, FieldPath: path,
				Message: fmt.Sprintf("%s field %q is required and is written by %s, whose output the engine cannot predict; verify empirically with `convctl test --validate-output` or `--fuzz`", destSide, path, why),
			})
		case satisfiedConditionally:
			diags = append(diags, Diagnostic{
				Severity: SeverityError, Code: CodeRequiredFieldConditional, RuleIndex: ruleIdx, FieldPath: path,
				Message: fmt.Sprintf("%s field %q is required but %s; the conversion succeeds for some inputs and produces an object the apiserver rejects for others. Make the rule unconditional, give the field a schema default, or acknowledge it on the rule", destSide, path, why),
			})
		case satisfiedNever:
			diags = append(diags, Diagnostic{
				Severity: SeverityError, Code: CodeRequiredFieldUnsatisfiable, RuleIndex: ruleIdx, FieldPath: path,
				Message: fmt.Sprintf("%s field %q is required but %s", destSide, path, why),
			})
		}
	}
	sortDiagnostics(diags)
	return diags
}

// bestVerdict takes the strongest guarantee among the rules writing a path:
// one unconditional rule is enough, however many conditional ones sit beside
// it.
func bestVerdict(idxs []int, rules []Rule, results []RuleResult, srcRequired map[string]bool, destSide string) (requiredSatisfaction, string, int) {
	best := satisfiedNever
	bestWhy := "no rule writing it guarantees a value"
	bestIdx := -1

	for _, i := range idxs {
		rr := results[i]
		var rule Rule
		for _, r := range rules {
			if r.SourceIndex == rr.Index {
				rule = r
				break
			}
		}

		verdict, why := verdictForRule(rule, rr, srcRequired, destSide)
		if verdict < best {
			best, bestWhy, bestIdx = verdict, why, rr.Index
		}
	}
	return best, bestWhy, bestIdx
}

func verdictForRule(rule Rule, rr RuleResult, srcRequired map[string]bool, destSide string) (requiredSatisfaction, string) {
	// `when` first. A rule whose condition is false does not write the
	// field at all, and that is knowable regardless of how unpredictable
	// its output would have been had it fired -- checking the strategy
	// first downgraded a conditional CEL or JSONPatch rule to a warning
	// about output nobody would ever see.
	if rule.When != nil {
		return satisfiedConditionally, fmt.Sprintf("the only rule writing it, rule %d (%s), carries a `when` clause and may not fire", rr.Index, rr.Strategy)
	}
	if unprovableStrategies[rr.Strategy] {
		return satisfiedUnprovably, fmt.Sprintf("rule %d (%s)", rr.Index, rr.Strategy)
	}

	// Strategies that produce a value out of nothing satisfy a required
	// field unconditionally, whatever the source looks like.
	switch rr.Strategy {
	case StrategyConstant, StrategyDefaultValue:
		return satisfiedAlways, ""
	}

	// Otherwise the guarantee is only as strong as the source: an optional
	// source field means an absent destination for the inputs that omit it.
	src := rr.HubPaths
	if destSide == "hub" {
		src = rr.SpokePaths
	}
	for _, s := range src {
		if !srcRequired[s] {
			return satisfiedConditionally, fmt.Sprintf("rule %d (%s) writes it from %q, which is itself optional, so an input omitting that field converts to an object missing a required one", rr.Index, rr.Strategy, s)
		}
	}
	return satisfiedAlways, ""
}

// unconditionallyRequiredLeaves returns the leaves a converted object must
// carry for the apiserver to accept it.
//
// "Required" in an OpenAPI schema is relative to the enclosing object: if
// the parent is absent, the child is not expected. So a leaf required inside
// an object that may legitimately be missing is not unconditionally
// required, and reporting it would be a false positive on a very common
// schema shape.
//
// An ancestor counts as present when it is either declared required, or the
// conversion writes something into it — because then the object exists in
// the output and its own required fields apply. That second half is not an
// optimization: `spec` is essentially never listed in a CRD root's
// `required`, so without it every field under `spec` would be skipped, which
// is every field that matters.
// requiredLeaf is a required destination leaf plus how its ancestors come to
// exist, which decides how strong a claim the analysis can make about it.
type requiredLeaf struct {
	LeafField
	// ancestorWrittenWholesale is set when the object containing this leaf
	// is itself the destination of a rule, rather than being reached
	// through a rule that writes into it. A rule that produces the whole
	// object may or may not put this key in it, and which of those it does
	// is not knowable from the plan.
	ancestorWrittenWholesale bool
}

func unconditionallyRequiredLeaves(schema *extv1.JSONSchemaProps, writtenDestPaths []string) []requiredLeaf {
	if schema == nil {
		return nil
	}
	var out []requiredLeaf
	for _, leaf := range flattenSchema(schema) {
		if !leaf.Required {
			continue
		}
		present, wholesale := ancestorsPresent(schema, leaf.Path, writtenDestPaths)
		if !present {
			continue
		}
		out = append(out, requiredLeaf{LeafField: leaf, ancestorWrittenWholesale: wholesale})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path.String() < out[j].Path.String() })
	return out
}

// ancestorsPresent reports whether every object between the root and path
// will exist in a converted object, and whether any of them exists only
// because a rule writes that object wholesale.
//
// The distinction matters: a rule writing into `spec.network.mtu` proves
// `spec.network` exists AND says nothing about `spec.network.cidr`, which is
// then a genuine unsatisfied requirement. A rule whose destination IS
// `spec.network` also proves the object exists, but it may well be producing
// `cidr` as one of the keys it puts there -- so reporting that as
// unsatisfiable would be a false alarm, and skipping the leaf entirely (what
// this did before) hides a real risk. It is reported as unprovable instead.
func ancestorsPresent(root *extv1.JSONSchemaProps, path FieldPath, writtenDestPaths []string) (present, wholesale bool) {
	node := root
	for i := 0; i < len(path)-1; i++ {
		seg := path[i]
		ancestor := path[:i+1].String()
		switch {
		case containsRequired(node.Required, seg):
		case anyHasPrefix(writtenDestPaths, ancestor+"."):
		case contains(writtenDestPaths, ancestor):
			wholesale = true
		default:
			return false, false
		}
		next, ok := node.Properties[seg]
		if !ok {
			return false, false
		}
		node = &next
	}
	return true, wholesale
}

func contains(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

func anyHasPrefix(paths []string, prefix string) bool {
	for _, p := range paths {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func containsRequired(required []string, name string) bool {
	for _, r := range required {
		if r == name {
			return true
		}
	}
	return false
}
