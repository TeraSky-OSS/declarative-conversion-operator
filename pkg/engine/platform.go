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

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// PlatformInjectedPaths describes field paths that the platform owning a
// resource merges into the schema the apiserver actually enforces, *over*
// whatever the author declared at the same path.
//
// The engine analyses the authored schema, so without this it will happily
// compile rules against a subtree that will never exist at runtime. It
// stays agnostic of which platform this is by taking a path set rather
// than a scope enum: the adapter owns the mapping (see
// pkg/xrdadapter, which derives it from the XRD's scope), and an adapter
// with nothing to declare — pkg/crdadapter, since nothing rewrites a plain
// native CRD's schema — simply supplies the zero value.
type PlatformInjectedPaths struct {
	// Platform names the owner for diagnostic messages, e.g. "Crossplane".
	// Defaults to "the platform" when empty.
	Platform string
	// Paths are the injected roots. A path is a whole subtree: anything at
	// or below it belongs to the platform.
	Paths []FieldPath
	// Uncertain downgrades both diagnostics from errors to warnings,
	// because the caller could not determine which set actually applies
	// and Paths is a superset of every candidate. A false error here would
	// block a config that is in fact correct, which is worse than a
	// warning on one that is not.
	Uncertain bool
	// UncertainReason is appended to the diagnostic message so the reader
	// knows why the check was softened. Required when Uncertain is set.
	UncertainReason string
}

// PlatformAwareSource is an optional SchemaSource extension. Analyze type
// asserts for it, so an adapter opts in simply by implementing the method
// and every existing caller picks the check up without changing a line.
type PlatformAwareSource interface {
	PlatformInjectedPaths() PlatformInjectedPaths
}

// Diagnostic codes for the two platform-collision checks.
const (
	// CodeAuthoredFieldShadowedByPlatform: the authored schema declares a
	// name from the injected set, so the author's declaration is silently
	// replaced in the schema the apiserver enforces.
	//
	// (Named "…ByPlatform" rather than "…ByCrossplane": pkg/engine has no
	// knowledge of Crossplane, and the message names the platform from
	// PlatformInjectedPaths.Platform.)
	CodeAuthoredFieldShadowedByPlatform = "AuthoredFieldShadowedByPlatform"
	// CodeRuleTargetsInjectedPath: a rule's hub or spoke path resolves at
	// or inside the injected set.
	CodeRuleTargetsInjectedPath = "RuleTargetsInjectedPath"
)

func (p PlatformInjectedPaths) platformName() string {
	if p.Platform == "" {
		return "the platform"
	}
	return p.Platform
}

func (p PlatformInjectedPaths) severity() Severity {
	if p.Uncertain {
		return SeverityWarning
	}
	return SeverityError
}

// uncertaintyNote is appended to every diagnostic this check emits when the
// caller could not pin the injected set down.
func (p PlatformInjectedPaths) uncertaintyNote() string {
	if !p.Uncertain {
		return ""
	}
	if p.UncertainReason == "" {
		return " (reported as a warning rather than an error: the applicable injected-field set could not be determined, so this is the union of every candidate and may not apply)"
	}
	return fmt.Sprintf(" (reported as a warning rather than an error: %s, so this is the union of every candidate set and may not apply)", p.UncertainReason)
}

// checkAuthoredShadowing reports every injected path the authored schema
// declares a node at. Such a declaration is replaced wholesale in the
// served schema, so any rule written against it — and any coverage
// reasoning about it — is about a subtree that does not exist at runtime.
func (p PlatformInjectedPaths) checkAuthoredShadowing(schema *extv1.JSONSchemaProps, version, side string) []Diagnostic {
	if len(p.Paths) == 0 || schema == nil {
		return nil
	}
	var diags []Diagnostic
	for _, path := range p.Paths {
		if !schemaDeclaresPath(schema, path) {
			continue
		}
		diags = append(diags, Diagnostic{
			Severity:  p.severity(),
			Code:      CodeAuthoredFieldShadowedByPlatform,
			RuleIndex: -1,
			FieldPath: path.String(),
			Message: fmt.Sprintf("the %s version %q schema declares %q, which %s injects into the served schema over the top of it — the authored declaration never exists at runtime, so rules and coverage analysis against it are meaningless. Rename the field%s",
				side, version, path.String(), p.platformName(), p.uncertaintyNote()),
		})
	}
	return diags
}

// checkRuleTargets reports every rule whose hub or spoke path lands at or
// inside the injected set. These fields are platform machinery: converting
// them is never right, and passthrough already carries them through
// untouched in both directions.
func (p PlatformInjectedPaths) checkRuleTargets(results []RuleResult) []Diagnostic {
	if len(p.Paths) == 0 {
		return nil
	}
	var diags []Diagnostic
	for _, rr := range results {
		for _, side := range []struct {
			name  string
			paths []string
		}{{"hub", rr.HubPaths}, {"spoke", rr.SpokePaths}} {
			for _, raw := range side.paths {
				injected, ok := p.owns(ParsePath(raw))
				if !ok {
					continue
				}
				diags = append(diags, Diagnostic{
					Severity:  p.severity(),
					Code:      CodeRuleTargetsInjectedPath,
					RuleIndex: rr.Index,
					FieldPath: raw,
					Message: fmt.Sprintf("rule %d (%s) targets %s path %q, which is inside %q — %s owns that subtree and the engine already passes it through untouched in both directions, so converting it is never correct%s",
						rr.Index, rr.Strategy, side.name, raw, injected.String(), p.platformName(), p.uncertaintyNote()),
				})
			}
		}
	}
	return diags
}

// owns reports whether path lands at or below one of the injected roots,
// returning that root.
func (p PlatformInjectedPaths) owns(path FieldPath) (FieldPath, bool) {
	for _, root := range p.Paths {
		if len(root) > 0 && path.HasPrefix(root) {
			return root, true
		}
	}
	return nil, false
}

// schemaDeclaresPath reports whether schema declares a named node at path.
// It walks Properties only: a path that disappears into an opaque map or a
// preserve-unknown-fields subtree was never *declared* by the author, so
// there is nothing for the platform to shadow.
func schemaDeclaresPath(schema *extv1.JSONSchemaProps, path FieldPath) bool {
	node := schema
	for _, seg := range path {
		if node == nil {
			return false
		}
		next, ok := node.Properties[seg]
		if !ok {
			return false
		}
		node = &next
	}
	return node != nil
}

// sortDiagnostics gives the two platform checks a stable order, so a
// status block or a CLI report does not churn between reconciles purely
// because Go randomised a map iteration upstream.
func sortDiagnostics(diags []Diagnostic) {
	sort.SliceStable(diags, func(i, j int) bool {
		if diags[i].RuleIndex != diags[j].RuleIndex {
			return diags[i].RuleIndex < diags[j].RuleIndex
		}
		return diags[i].FieldPath < diags[j].FieldPath
	})
}
