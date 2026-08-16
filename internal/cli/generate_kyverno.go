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

package cli

import (
	"fmt"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

const (
	defaultXRDAPIVersionLabel = "xrd-api-version"
	kyvernoMutatingAPIVersion = "policies.kyverno.io/v1"
	kyvernoMutatingKind       = "MutatingPolicy"
)

// GenerateKyvernoOptions configures RunGenerateKyverno.
type GenerateKyvernoOptions struct {
	XRDPath               string
	To                    string
	From                  string
	LabelKey              string
	CompositionPolicyName string
	MigratePolicyName     string
}

type xrdIdentity struct {
	Group          string
	Kind           string
	Plural         string
	Versions       []string
	ServedVersions []string
}

// kyvernoMutatingPolicy is the subset of policies.kyverno.io/v1 MutatingPolicy
// this command emits. Struct field order keeps YAML/JSON goldens stable.
type kyvernoMutatingPolicy struct {
	APIVersion string              `json:"apiVersion"`
	Kind       string              `json:"kind"`
	Metadata   kyvernoObjectMeta   `json:"metadata"`
	Spec       kyvernoMutatingSpec `json:"spec"`
}

type kyvernoObjectMeta struct {
	Name string `json:"name"`
}

type kyvernoMutatingSpec struct {
	Evaluation       kyvernoEvaluation       `json:"evaluation"`
	MatchConstraints kyvernoMatchConstraints `json:"matchConstraints"`
	// MatchConditions must stay empty. Kyverno 1.18 can ignore a
	// MutatingPolicy whose matchConditions read object.spec
	// (kyverno/kyverno#15353). XRD / selector filters live in the mutation CEL.
	MatchConditions []kyvernoMatchCondition `json:"matchConditions,omitempty"`
	Mutations       []kyvernoMutation       `json:"mutations"`
}

type kyvernoEvaluation struct {
	Admission      kyvernoToggle `json:"admission"`
	MutateExisting kyvernoToggle `json:"mutateExisting"`
}

type kyvernoToggle struct {
	Enabled bool `json:"enabled"`
}

type kyvernoMatchConstraints struct {
	ResourceRules []kyvernoResourceRule `json:"resourceRules"`
}

type kyvernoResourceRule struct {
	APIGroups   []string `json:"apiGroups"`
	APIVersions []string `json:"apiVersions"`
	Resources   []string `json:"resources"`
	Operations  []string `json:"operations"`
}

type kyvernoMatchCondition struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
}

type kyvernoMutation struct {
	PatchType          string                     `json:"patchType"`
	ApplyConfiguration *kyvernoApplyConfiguration `json:"applyConfiguration,omitempty"`
	JSONPatch          *kyvernoJSONPatch          `json:"jsonPatch,omitempty"`
}

type kyvernoApplyConfiguration struct {
	Expression string `json:"expression"`
}

type kyvernoJSONPatch struct {
	Expression string `json:"expression"`
}

// RunGenerateKyverno drafts a per-XRD Composition labeler and a per-hub XR
// migrate MutatingPolicy. It never applies anything.
func RunGenerateKyverno(opts GenerateKyvernoOptions) ([]kyvernoMutatingPolicy, error) {
	if opts.XRDPath == "" {
		return nil, fmt.Errorf("--xrd is required")
	}
	if opts.To == "" {
		return nil, fmt.Errorf("--to is required: name the XRD version the migrate policy writes")
	}
	labelKey := opts.LabelKey
	if labelKey == "" {
		labelKey = defaultXRDAPIVersionLabel
	}

	xrd, err := LoadXRD(opts.XRDPath)
	if err != nil {
		return nil, err
	}
	id, err := xrdIdentityFrom(xrd)
	if err != nil {
		return nil, err
	}
	if !containsString(id.Versions, opts.To) {
		return nil, fmt.Errorf("--to %q is not a version on the XRD (have %s)", opts.To, strings.Join(id.Versions, ", "))
	}
	if opts.From != "" && !containsString(id.Versions, opts.From) {
		return nil, fmt.Errorf("--from %q is not a version on the XRD (have %s)", opts.From, strings.Join(id.Versions, ", "))
	}

	compName := opts.CompositionPolicyName
	if compName == "" {
		compName = "label-compositions-" + dns1123Label(id.Plural)
	}
	migName := opts.MigratePolicyName
	if migName == "" {
		// Stable per-XRD name. A hub flip updates --from/--to on the
		// same MutatingPolicy; do not mint migrate-*-to-vN objects.
		migName = "set-composition-version-selector-" + dns1123Label(id.Plural)
	}

	return []kyvernoMutatingPolicy{
		compositionLabelerPolicy(compName, id, labelKey),
		xrMigratePolicy(migName, id, labelKey, opts.From, opts.To),
	}, nil
}

func xrdIdentityFrom(xrd *unstructured.Unstructured) (xrdIdentity, error) {
	var id xrdIdentity
	var err error
	id.Group, _, err = unstructured.NestedString(xrd.Object, "spec", "group")
	if err != nil {
		return id, fmt.Errorf("reading spec.group: %w", err)
	}
	id.Kind, _, err = unstructured.NestedString(xrd.Object, "spec", "names", "kind")
	if err != nil {
		return id, fmt.Errorf("reading spec.names.kind: %w", err)
	}
	id.Plural, _, err = unstructured.NestedString(xrd.Object, "spec", "names", "plural")
	if err != nil {
		return id, fmt.Errorf("reading spec.names.plural: %w", err)
	}
	if id.Group == "" || id.Kind == "" || id.Plural == "" {
		return id, fmt.Errorf("XRD %q must set spec.group, spec.names.kind, and spec.names.plural", xrd.GetName())
	}

	raw, found, err := unstructured.NestedSlice(xrd.Object, "spec", "versions")
	if err != nil {
		return id, fmt.Errorf("reading spec.versions: %w", err)
	}
	if !found || len(raw) == 0 {
		return id, fmt.Errorf("XRD %q has no spec.versions", xrd.GetName())
	}
	for i, item := range raw {
		vm, ok := item.(map[string]any)
		if !ok {
			return id, fmt.Errorf("spec.versions[%d] is not an object", i)
		}
		name, _, err := unstructured.NestedString(vm, "name")
		if err != nil || name == "" {
			return id, fmt.Errorf("spec.versions[%d] has no name", i)
		}
		id.Versions = append(id.Versions, name)
		served, found, err := unstructured.NestedBool(vm, "served")
		if err != nil {
			return id, fmt.Errorf("spec.versions[%d].served: %w", i, err)
		}
		if !found || served {
			id.ServedVersions = append(id.ServedVersions, name)
		}
	}
	if len(id.ServedVersions) == 0 {
		return id, fmt.Errorf("XRD %q has no served versions", xrd.GetName())
	}
	return id, nil
}

func compositionLabelerPolicy(name string, id xrdIdentity, labelKey string) kyvernoMutatingPolicy {
	return kyvernoMutatingPolicy{
		APIVersion: kyvernoMutatingAPIVersion,
		Kind:       kyvernoMutatingKind,
		Metadata:   kyvernoObjectMeta{Name: name},
		Spec: kyvernoMutatingSpec{
			Evaluation: kyvernoEvaluation{
				Admission:      kyvernoToggle{Enabled: true},
				MutateExisting: kyvernoToggle{Enabled: true},
			},
			MatchConstraints: kyvernoMatchConstraints{
				ResourceRules: []kyvernoResourceRule{{
					APIGroups:   []string{"apiextensions.crossplane.io"},
					APIVersions: []string{"v1"},
					Resources:   []string{"compositions"},
					Operations:  []string{"CREATE", "UPDATE"},
				}},
			},
			Mutations: []kyvernoMutation{{
				PatchType: "ApplyConfiguration",
				ApplyConfiguration: &kyvernoApplyConfiguration{
					Expression: compositionLabelCEL(id, labelKey),
				},
			}},
		},
	}
}

func xrMigratePolicy(name string, id xrdIdentity, labelKey, from, to string) kyvernoMutatingPolicy {
	return kyvernoMutatingPolicy{
		APIVersion: kyvernoMutatingAPIVersion,
		Kind:       kyvernoMutatingKind,
		Metadata:   kyvernoObjectMeta{Name: name},
		Spec: kyvernoMutatingSpec{
			Evaluation: kyvernoEvaluation{
				// Admission must be on. Kyverno 1.18.1 never creates
				// UpdateRequests for MutatingPolicy mutateExisting
				// (kyverno/kyverno#16255), so admission:false is a no-op.
				Admission:      kyvernoToggle{Enabled: true},
				MutateExisting: kyvernoToggle{Enabled: true},
			},
			MatchConstraints: kyvernoMatchConstraints{
				ResourceRules: []kyvernoResourceRule{{
					APIGroups:   []string{id.Group},
					APIVersions: append([]string(nil), id.ServedVersions...),
					Resources:   []string{id.Plural},
					Operations:  []string{"CREATE", "UPDATE"},
				}},
			},
			Mutations: []kyvernoMutation{{
				PatchType: "JSONPatch",
				JSONPatch: &kyvernoJSONPatch{
					Expression: migrateJSONPatchCEL(labelKey, from, to),
				},
			}},
		},
	}
}

func compositionMatchCEL(id xrdIdentity) string {
	return fmt.Sprintf(
		`has(object.spec.compositeTypeRef) && has(object.spec.compositeTypeRef.kind) && object.spec.compositeTypeRef.kind == %s && has(object.spec.compositeTypeRef.apiVersion) && object.spec.compositeTypeRef.apiVersion.startsWith(%s)`,
		celString(id.Kind), celString(id.Group+"/"))
}

func compositionLabelCEL(id xrdIdentity, labelKey string) string {
	// ApplyConfiguration Object.metadata.labels{ "hyphen-key": ... } is invalid
	// CEL (quoted keys are not field identifiers). A map literal is.
	return strings.TrimSpace(fmt.Sprintf(`
(
  %s
  ?
    Object{
      metadata: Object.metadata{
        labels: {
          %s: object.spec.compositeTypeRef.apiVersion.split("/")[1]
        }
      }
    }
  :
    Object{}
)
`, compositionMatchCEL(id), celString(labelKey)))
}

func migrateMatchCEL(labelKey, from, to string) string {
	// UPDATE: decide from oldObject (the live XR). After the selector is
	// --to, Crossplane writing compositionRef back must not rematch.
	// CREATE: oldObject is null; decide from the incoming object.
	live := migrateNeedsCEL("oldObject", labelKey, from, to)
	incoming := migrateNeedsCEL("object", labelKey, from, to)
	return fmt.Sprintf(
		`(oldObject != null && has(oldObject.spec) ? (%s) : (%s))`,
		live, incoming)
}

func migrateNeedsCEL(obj, labelKey, from, to string) string {
	key := celString(labelKey)
	sel := obj + ".spec.crossplane.compositionSelector"
	labels := sel + ".matchLabels"
	cmp := fmt.Sprintf("%s[%s] != %s", labels, key, celString(to))
	if from != "" {
		cmp = fmt.Sprintf("%s[%s] == %s", labels, key, celString(from))
	}
	return fmt.Sprintf(
		`has(%s.spec.crossplane) && (!has(%s) || !has(%s) || !(%s in %s) || %s)`,
		obj, sel, labels, key, labels, cmp)
}

func migrateJSONPatchCEL(labelKey, from, to string) string {
	key := celString(labelKey)
	val := celString(to)
	ptr := jsonPointerEscape(labelKey)
	return strings.TrimSpace(fmt.Sprintf(`
(
  %s
  ?
    (
      (
        has(object.spec.crossplane.compositionRef)
        ?
          [
            JSONPatch{
              op: "remove",
              path: "/spec/crossplane/compositionRef"
            }
          ]
        :
          []
      )
      +
      (
        has(object.spec.crossplane.compositionRevisionRef)
        ?
          [
            JSONPatch{
              op: "remove",
              path: "/spec/crossplane/compositionRevisionRef"
            }
          ]
        :
          []
      )
      +
      (
        has(object.spec.crossplane.compositionSelector) &&
        has(object.spec.crossplane.compositionSelector.matchLabels) &&
        (%s in object.spec.crossplane.compositionSelector.matchLabels)
        ?
          [
            JSONPatch{
              op: "replace",
              path: "/spec/crossplane/compositionSelector/matchLabels/%s",
              value: %s
            }
          ]
        :
        has(object.spec.crossplane.compositionSelector) &&
        has(object.spec.crossplane.compositionSelector.matchLabels)
        ?
          [
            JSONPatch{
              op: "add",
              path: "/spec/crossplane/compositionSelector/matchLabels/%s",
              value: %s
            }
          ]
        :
        has(object.spec.crossplane.compositionSelector)
        ?
          [
            JSONPatch{
              op: "add",
              path: "/spec/crossplane/compositionSelector/matchLabels",
              value: {
                %s: %s
              }
            }
          ]
        :
          [
            JSONPatch{
              op: "add",
              path: "/spec/crossplane/compositionSelector",
              value: {
                "matchLabels": {
                  %s: %s
                }
              }
            }
          ]
      )
    )
  :
    []
)
`, migrateMatchCEL(labelKey, from, to), key, ptr, val, ptr, val, key, val, key, val))
}

func celString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func jsonPointerEscape(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	s = strings.ReplaceAll(s, "/", "~1")
	return s
}

func dns1123Label(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func encodeKyvernoYAML(docs []kyvernoMutatingPolicy) ([]byte, error) {
	var b strings.Builder
	b.WriteString("# Generated by convctl generate kyverno. Review before apply. convctl never applies this.\n")
	for i, doc := range docs {
		if i > 0 {
			b.WriteString("---\n")
		}
		data, err := sigsyaml.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("marshaling policy: %w", err)
		}
		b.Write(data)
	}
	return []byte(b.String()), nil
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
