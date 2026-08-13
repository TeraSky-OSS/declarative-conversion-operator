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
	"sort"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	sigsyaml "sigs.k8s.io/yaml"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// SuggestOutput is a set of proposed rules, shaped exactly like the
// `spec.spokes` stanza of a conversion config so it can be pasted straight
// in after review.
type SuggestOutput struct {
	Spokes []teraskyv1alpha1.SpokeVersionRules `json:"spokes"`
}

// suggestSimilarityThreshold is how alike two field names must be, after
// normalization, before a rename is worth proposing. Deliberately
// permissive: a wrong suggestion costs a reviewer one deleted line, while a
// missed one costs them a manual schema comparison.
const suggestSimilarityThreshold = 0.6

// RunSuggest analyzes the config and proposes FieldRename and TypeCoerce
// rules for the hub and spoke fields it left uncovered. Suggestions are
// heuristic by construction — the engine can prove a mapping is lossless,
// but nothing can prove two differently-named fields were meant to be the
// same one — so the output is a starting point for review, never something
// to apply unread.
func RunSuggest(xrdPath, crdPath, configPath string) (*SuggestOutput, error) {
	kind, err := PeekConfigKind(configPath)
	if err != nil {
		return nil, err
	}
	if kind == "CRDConversionConfig" {
		if crdPath == "" {
			return nil, fmt.Errorf("%s is a CRDConversionConfig; pass its target schema with --crd, not --xrd", configPath)
		}
		crd, err := LoadCRD(crdPath)
		if err != nil {
			return nil, err
		}
		cfg, err := LoadCRDConfig(configPath)
		if err != nil {
			return nil, err
		}
		report, versions, err := runAnalyzeCRD(crd, cfg)
		if err != nil {
			return nil, err
		}
		return buildSuggestions(cfg.Spec.HubVersion, versions, report), nil
	}

	if xrdPath == "" {
		return nil, fmt.Errorf("%s is an XRDConversionConfig; pass its target schema with --xrd, not --crd", configPath)
	}
	xrd, err := LoadXRD(xrdPath)
	if err != nil {
		return nil, err
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	report, versions, err := runAnalyze(xrd, cfg)
	if err != nil {
		return nil, err
	}
	return buildSuggestions(cfg.Spec.HubVersion, versions, report), nil
}

func buildSuggestions(hubVersion string, versions []engine.VersionSchema, report engine.AnalyzeReport) *SuggestOutput {
	schemas := map[string]*extv1.JSONSchemaProps{}
	for _, v := range versions {
		schemas[v.Name] = v.Schema
	}
	hubLeaves := leafIndex(schemas[hubVersion])

	out := &SuggestOutput{}
	for _, sr := range report.SpokeReports {
		rules := suggestSpokeRules(sr, hubLeaves, leafIndex(schemas[sr.Version]))
		if len(rules) == 0 {
			continue
		}
		out.Spokes = append(out.Spokes, teraskyv1alpha1.SpokeVersionRules{Version: sr.Version, Rules: rules})
	}
	return out
}

func leafIndex(schema *extv1.JSONSchemaProps) map[string]engine.LeafField {
	out := map[string]engine.LeafField{}
	for _, l := range engine.FlattenSchema(schema) {
		out[l.Path.String()] = l
	}
	return out
}

// suggestSpokeRules pairs one spoke's uncovered hub fields with its
// uncovered spoke fields. Type coercions are matched first, since a field
// that kept its path and only changed type is unambiguous; whatever is left
// then competes for rename pairings by name similarity.
func suggestSpokeRules(sr engine.SpokeReport, hubLeaves, spokeLeaves map[string]engine.LeafField) []teraskyv1alpha1.ConversionRule {
	hubPaths := dedupeSorted(sr.Uncovered.UncoveredHub)
	spokePaths := dedupeSorted(sr.Uncovered.UncoveredSpoke)
	usedHub := map[string]bool{}
	usedSpoke := map[string]bool{}

	var rules []teraskyv1alpha1.ConversionRule
	for _, p := range hubPaths {
		hl, okHub := hubLeaves[p]
		sl, okSpoke := spokeLeaves[p]
		if !okHub || !okSpoke || hl.Kind == sl.Kind {
			continue
		}
		if !isScalarKind(hl.Kind) || !isScalarKind(sl.Kind) {
			continue
		}
		usedHub[p] = true
		usedSpoke[p] = true
		rules = append(rules, teraskyv1alpha1.ConversionRule{
			Strategy:   teraskyv1alpha1.StrategyTypeCoerce,
			TypeCoerce: &teraskyv1alpha1.TypeCoerceParams{Path: p},
		})
	}

	for _, c := range renameCandidates(hubPaths, spokePaths, hubLeaves, spokeLeaves) {
		if usedHub[c.hubPath] || usedSpoke[c.spokePath] {
			continue
		}
		usedHub[c.hubPath] = true
		usedSpoke[c.spokePath] = true
		rules = append(rules, teraskyv1alpha1.ConversionRule{
			Strategy:    teraskyv1alpha1.StrategyFieldRename,
			FieldRename: &teraskyv1alpha1.FieldRenameParams{HubPath: c.hubPath, SpokePath: c.spokePath},
		})
	}
	return rules
}

type renameCandidate struct {
	hubPath   string
	spokePath string
	score     float64
}

// renameCandidates scores every plausible hub/spoke pairing and returns
// them best-first, so a greedy walk assigns the strongest matches before
// weaker ones can steal a field. Pairs are only plausible when they share a
// parent path and a FieldKind: a rename moves a field's name, not its
// position in the tree or its type (that's what TypeCoerce is for).
func renameCandidates(hubPaths, spokePaths []string, hubLeaves, spokeLeaves map[string]engine.LeafField) []renameCandidate {
	var out []renameCandidate
	for _, hp := range hubPaths {
		hl, ok := hubLeaves[hp]
		if !ok {
			continue
		}
		for _, sp := range spokePaths {
			if hp == sp {
				continue
			}
			sl, ok := spokeLeaves[sp]
			if !ok || sl.Kind != hl.Kind || sl.Opaque != hl.Opaque {
				continue
			}
			if parentPath(hp) != parentPath(sp) {
				continue
			}
			score := nameSimilarity(lastPathSegment(hp), lastPathSegment(sp))
			if score < suggestSimilarityThreshold {
				continue
			}
			out = append(out, renameCandidate{hubPath: hp, spokePath: sp, score: score})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].hubPath != out[j].hubPath {
			return out[i].hubPath < out[j].hubPath
		}
		return out[i].spokePath < out[j].spokePath
	})
	return out
}

func isScalarKind(k engine.FieldKind) bool {
	switch k {
	case engine.FieldKindString, engine.FieldKindInteger, engine.FieldKindNumber, engine.FieldKindBoolean:
		return true
	}
	return false
}

func parentPath(p string) string {
	if i := strings.LastIndex(p, "."); i >= 0 {
		return p[:i]
	}
	return ""
}

func lastPathSegment(p string) string {
	if i := strings.LastIndex(p, "."); i >= 0 {
		return p[i+1:]
	}
	return p
}

func dedupeSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// nameSimilarity scores two field names between 0 and 1 by edit distance
// over their normalized forms, so storageGB/storage_size/StorageSize all
// compare on the letters that carry meaning rather than on casing or
// punctuation an API author changed in passing.
func nameSimilarity(a, b string) float64 {
	na, nb := normalizeFieldName(a), normalizeFieldName(b)
	if na == "" || nb == "" {
		return 0
	}
	if na == nb {
		return 1
	}
	longest := len(na)
	if len(nb) > longest {
		longest = len(nb)
	}
	return 1 - float64(levenshtein(na, nb))/float64(longest)
}

func normalizeFieldName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func newSuggestCmd() *cobra.Command {
	var xrdPath, crdPath, configPath, output string
	cmd := &cobra.Command{
		Use:   "suggest",
		Short: "Propose rule stubs for fields no rule covers yet",
		Long: `Analyze a conversion config, then propose rules for whatever it leaves
uncovered — the tedious half of authoring a mapping between two versions.

Hub and spoke fields that sit under the same parent, carry the same type, and
have similar names are proposed as FieldRename rules. Fields that kept their
path but changed scalar type are proposed as TypeCoerce rules.

These are heuristics, not conclusions: nothing can prove two differently-named
fields were meant to be the same one. Read every suggestion, delete the wrong
ones, and let convctl validate and convctl test grade what's left. Strategies
beyond rename and coerce are intentionally never guessed at.

Output is shaped exactly like a config's spec.spokes stanza, so accepted
suggestions merge in by hand with no reshaping.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch output {
			case "yaml", "json":
			default:
				return fmt.Errorf("invalid --output value %q (want yaml or json)", output)
			}
			out, err := RunSuggest(xrdPath, crdPath, configPath)
			if err != nil {
				return err
			}
			if output == "json" {
				return writeJSON(cmd, out)
			}
			if len(out.Spokes) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "# no suggestions: every uncovered field is either already claimed or too dissimilar to guess at")
				return err
			}
			data, err := sigsyaml.Marshal(out)
			if err != nil {
				return fmt.Errorf("rendering YAML: %w", err)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "# suggested rules — review every one before merging into spec.spokes")
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	cmd.Flags().StringVarP(&xrdPath, "xrd", "x", "", "Path to an XRD YAML file (required for an XRDConversionConfig)")
	cmd.Flags().StringVar(&crdPath, "crd", "", "Path to a CRD YAML file (required for a CRDConversionConfig)")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to an XRDConversionConfig or CRDConversionConfig YAML file (required)")
	cmd.Flags().StringVarP(&output, "output", "o", "yaml", "Output format: yaml|json")
	_ = cmd.MarkFlagRequired("config")
	cmd.MarkFlagsOneRequired("xrd", "crd")
	cmd.MarkFlagsMutuallyExclusive("xrd", "crd")
	return cmd
}
