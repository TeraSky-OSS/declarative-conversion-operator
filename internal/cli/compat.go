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
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// Compatibility change classes, ordered by how much attention each deserves.
//
// The point of naming them is that a branch-protection rule can require the
// gate without requiring a human to classify every diff by hand: breaking
// changes fail, and a deliberate one is acknowledged by class rather than by
// switching the whole check off.
const (
	ClassLosslessToLossy     = "LosslessToLossy"
	ClassCoverageLost        = "CoverageLost"
	ClassServedVersionRemove = "ServedVersionRemoved"
	ClassRuleRemoved         = "RuleRemoved"
	ClassHubChanged          = "HubChanged"
	ClassStrategyChanged     = "StrategyChanged"
	ClassMappingChanged      = "MappingChanged"
	ClassCoverageGained      = "CoverageGained"
	ClassNewVersion          = "NewVersion"
)

// severityOf is the single place a class's severity is decided, so the
// table in the command's help, the classifier and the gate cannot drift
// apart.
func severityOf(class string) string {
	switch class {
	case ClassLosslessToLossy, ClassCoverageLost, ClassServedVersionRemove,
		ClassRuleRemoved, ClassHubChanged:
		return "breaking"
	case ClassStrategyChanged, ClassMappingChanged:
		return "review"
	}
	return "safe"
}

// CompatChange is one classified difference between two revisions.
type CompatChange struct {
	Class    string `json:"class"`
	Severity string `json:"severity"` // breaking|review|safe
	Spoke    string `json:"spoke,omitempty"`
	Detail   string `json:"detail"`
	// Acknowledged records that --allow named this class, so the output
	// says which acknowledgements were used rather than silently passing.
	Acknowledged bool `json:"acknowledged,omitempty"`
}

// CompatReport is the outcome of comparing two revisions.
type CompatReport struct {
	Base    string         `json:"base"`
	Head    string         `json:"head"`
	Config  string         `json:"config"`
	Changes []CompatChange `json:"changes"`
	Allowed []string       `json:"allowed,omitempty"`
	// Notes records facts about the comparison itself rather than about the
	// delta -- above all, that one of the revisions does not analyze
	// cleanly. "No differences" between two configs that both fail to
	// compile is true and useless, and a reviewer reading only the verdict
	// would take it for a clean bill of health.
	Notes []string `json:"notes,omitempty"`
}

// Breaking reports whether any unacknowledged breaking change was found.
func (r *CompatReport) Breaking() bool {
	for _, c := range r.Changes {
		if c.Severity == "breaking" && !c.Acknowledged {
			return true
		}
	}
	return false
}

// CompatOptions configures a comparison.
type CompatOptions struct {
	Base       string
	Head       string
	ConfigPath string
	XRDPath    string
	CRDPath    string
	Allow      []string
}

// gitShow reads a path at a revision without requiring a checkout, so the
// gate works in a shallow CI clone (fetch-depth: 2) rather than needing the
// full history.
func gitShow(ref, path string) ([]byte, error) {
	spec, err := gitPathspec(path)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("git", "show", ref+":"+spec) // #nosec G204 -- ref and path are the caller's own arguments, same trust as --config
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git show %s:%s: %w: %s", ref, path, err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

// gitPathspec rewrites a filesystem path into one `git show <rev>:` accepts.
//
// A bare path after the colon is resolved from the repository root, not the
// working directory -- so `--config config.yaml`, which every other command
// in this CLI reads relative to where you are standing, would fail with
// "does not exist in HEAD" from any subdirectory. A leading ./ or ../ makes
// git resolve it the way the shell just did.
func gitPathspec(path string) (string, error) {
	if filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolving %s against the working directory: %w", path, err)
		}
		rel, err := filepath.Rel(wd, path)
		if err != nil {
			return "", fmt.Errorf("%s is not reachable from the working directory, which is what git resolves a revision path against: %w", path, err)
		}
		path = rel
	}
	if strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../") {
		return path, nil
	}
	return "./" + path, nil
}

// analysisAt is everything the comparison needs from one revision.
type analysisAt struct {
	hub      string
	report   engine.AnalyzeReport
	versions []engine.VersionSchema
}

// RunCompat compares the schema and config at two git revisions and
// classifies every difference.
//
// This is a presentation of Analyze's output at two points in time, not a
// second engine: the same analysis that decides losslessness and coverage
// for one revision decides it for both, so the two can never disagree about
// what a rule does.
func RunCompat(opts CompatOptions) (*CompatReport, error) {
	if opts.Base == "" || opts.Head == "" {
		return nil, errors.New("--base and --head are both required")
	}
	if opts.ConfigPath == "" {
		return nil, errors.New("--config is required")
	}

	base, err := analyzeAtRef(opts.Base, opts)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", opts.Base, err)
	}
	head, err := analyzeAtRef(opts.Head, opts)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", opts.Head, err)
	}

	rep := &CompatReport{Base: opts.Base, Head: opts.Head, Config: opts.ConfigPath, Allowed: opts.Allow}
	rep.Notes = analysisNotes(opts.Base, base.report, opts.Head, head.report)
	rep.Changes = classify(base, head)

	allowed := map[string]bool{}
	for _, a := range opts.Allow {
		allowed[a] = true
	}
	for i := range rep.Changes {
		if allowed[rep.Changes[i].Class] {
			rep.Changes[i].Acknowledged = true
		}
	}
	return rep, nil
}

func analyzeAtRef(ref string, opts CompatOptions) (analysisAt, error) {
	var out analysisAt

	cfgData, err := gitShow(ref, opts.ConfigPath)
	if err != nil {
		return out, err
	}

	switch {
	case opts.XRDPath != "":
		xrdData, xerr := gitShow(ref, opts.XRDPath)
		if xerr != nil {
			return out, xerr
		}
		xrd, perr := parseXRDBytes(xrdData)
		if perr != nil {
			return out, perr
		}
		cfg, perr := parseXRDConfigBytes(cfgData)
		if perr != nil {
			return out, perr
		}
		report, versions, aerr := runAnalyze(xrd, cfg)
		if aerr != nil {
			return out, aerr
		}
		return analysisAt{hub: cfg.Spec.HubVersion, report: report, versions: versions}, nil

	case opts.CRDPath != "":
		crdData, cerr := gitShow(ref, opts.CRDPath)
		if cerr != nil {
			return out, cerr
		}
		crd, perr := parseCRDBytes(crdData)
		if perr != nil {
			return out, perr
		}
		cfg, perr := parseCRDConfigBytes(cfgData)
		if perr != nil {
			return out, perr
		}
		report, versions, aerr := runAnalyzeCRD(crd, cfg)
		if aerr != nil {
			return out, aerr
		}
		return analysisAt{hub: cfg.Spec.HubVersion, report: report, versions: versions}, nil
	}
	return out, errors.New("one of --xrd or --crd is required")
}

// analysisNotes records that a revision does not analyze cleanly.
//
// These are not compatibility changes -- an uncovered field or an unserved
// spoke is ordinary currency here, and several of them are exactly what the
// change classes describe. But a report that says "No differences" about two
// revisions that both fail `convctl validate` reads as a pass, so the
// comparison says which side it could not fully analyze.
func analysisNotes(baseRef string, base engine.AnalyzeReport, headRef string, head engine.AnalyzeReport) []string {
	var notes []string
	for _, side := range []struct {
		ref    string
		report engine.AnalyzeReport
	}{{baseRef, base}, {headRef, head}} {
		if !side.report.HasErrors() {
			continue
		}
		n := 0
		first := ""
		for _, sr := range side.report.SpokeReports {
			for _, d := range sr.Errors {
				n++
				if first == "" {
					first = fmt.Sprintf("spoke %s: %s", sr.Version, d.Message)
				}
			}
		}
		notes = append(notes, fmt.Sprintf("the config at %s does not analyze cleanly (%d error(s), first: %s) — run `convctl validate` there; the comparison below is between what each revision declares, not between two working configs", side.ref, n, first))
	}
	return notes
}

// classify turns two analyses into a list of classified changes.
func classify(base, head analysisAt) []CompatChange {
	var out []CompatChange

	if base.hub != head.hub {
		out = append(out, CompatChange{
			Class: ClassHubChanged, Severity: severityOf(ClassHubChanged),
			Detail: fmt.Sprintf("hub version moved from %s to %s; every stored object converts through a different pivot, so this must be deliberate", base.hub, head.hub),
		})
	}

	baseServed := servedSet(base.versions)
	headServed := servedSet(head.versions)
	for _, v := range sortedKeys(baseServed) {
		if !headServed[v] {
			out = append(out, CompatChange{
				Class: ClassServedVersionRemove, Severity: severityOf(ClassServedVersionRemove),
				Detail: fmt.Sprintf("version %s is no longer served; objects still stored at it become unreadable. Check with `convctl versions --check-unserve %s` first", v, v),
			})
		}
	}
	for _, v := range sortedKeys(headServed) {
		if !baseServed[v] {
			out = append(out, CompatChange{
				Class: ClassNewVersion, Severity: severityOf(ClassNewVersion),
				Detail: fmt.Sprintf("version %s is newly served", v),
			})
		}
	}

	baseSpokes := spokeIndex(base.report)
	headSpokes := spokeIndex(head.report)

	// The union, not just the base: a version that gains its first rule set
	// in the head revision has no base entry at all, and iterating only the
	// base would report nothing for it -- a revision that added coverage
	// would show up as "no differences".
	allSpokes := map[string]bool{}
	for n := range baseSpokes {
		allSpokes[n] = true
	}
	for n := range headSpokes {
		allSpokes[n] = true
	}

	for _, name := range sortedKeys(allSpokes) {
		b := baseSpokes[name]
		h, stillThere := headSpokes[name]
		if !stillThere {
			// A dropped spoke is reported through the served-version check
			// when the version went too; otherwise it is lost coverage.
			if headServed[name] {
				out = append(out, CompatChange{
					Class: ClassCoverageLost, Severity: severityOf(ClassCoverageLost), Spoke: name,
					Detail: fmt.Sprintf("spoke %s no longer has a rule set, but the version is still served", name),
				})
			}
			continue
		}

		// Per direction, not per spoke. Requiring both directions to have
		// been lossless at the base would miss the case this class exists
		// for: a spoke that was already lossy one way, where the OTHER
		// direction then stops round-tripping. Nothing would be reported
		// and the gate would pass a regression.
		for _, d := range []struct {
			name       string
			base, head bool
		}{
			{"hub→spoke", b.Lossless.HubToSpoke, h.Lossless.HubToSpoke},
			{"spoke→hub", b.Lossless.SpokeToHub, h.Lossless.SpokeToHub},
		} {
			if d.base && !d.head {
				out = append(out, CompatChange{
					Class: ClassLosslessToLossy, Severity: severityOf(ClassLosslessToLossy), Spoke: name,
					Detail: fmt.Sprintf("spoke %s: %s used to round-trip losslessly and no longer does", name, d.name),
				})
			}
		}

		out = append(out, comparePaths(name, b, h)...)
		out = append(out, compareStrategies(name, b, h)...)
		out = append(out, compareAssociations(name, b, h)...)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return severityRank(out[i].Severity) < severityRank(out[j].Severity)
		}
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		return out[i].Detail < out[j].Detail
	})
	return out
}

// comparePaths reports fields that used to have a rule and no longer do, and
// vice versa.
func comparePaths(spoke string, b, h engine.SpokeReport) []CompatChange {
	var out []CompatChange
	baseClaimed := claimedPaths(b)
	headClaimed := claimedPaths(h)

	for _, p := range sortedKeys(baseClaimed) {
		if !headClaimed[p] {
			out = append(out, CompatChange{
				Class: ClassCoverageLost, Severity: severityOf(ClassCoverageLost), Spoke: spoke,
				Detail: fmt.Sprintf("spoke %s: %q had a rule and no longer does", spoke, p),
			})
		}
	}
	for _, p := range sortedKeys(headClaimed) {
		if !baseClaimed[p] {
			out = append(out, CompatChange{
				Class: ClassCoverageGained, Severity: severityOf(ClassCoverageGained), Spoke: spoke,
				Detail: fmt.Sprintf("spoke %s: %q is newly covered by a rule", spoke, p),
			})
		}
	}
	return out
}

// compareStrategies reports a path whose rule changed strategy, and a rule
// that disappeared without another claiming its paths.
// compareAssociations catches a rule set that kept every path it claims and
// every strategy it uses, but wired them to each other differently.
//
// Swapping two renames -- A→X, B→Y becoming A→Y, B→X -- changes what the
// conversion does to every object, while leaving the claimed paths, the
// per-path strategies, the rule indices and the aggregate losslessness
// verdict all identical. Every other comparison here works from those
// aggregates, so without this the gate reports no differences at all.
//
// Only reported when the claimed paths are otherwise unchanged: when they
// are not, comparePaths and compareStrategies already describe the edit, and
// repeating it here would be noise.
func compareAssociations(spoke string, b, h engine.SpokeReport) []CompatChange {
	if !sameKeys(claimedPaths(b), claimedPaths(h)) {
		return nil
	}
	base, head := ruleSignatures(b), ruleSignatures(h)
	if strings.Join(base, "\n") == strings.Join(head, "\n") {
		return nil
	}
	var gone []string
	inHead := map[string]int{}
	for _, sig := range head {
		inHead[sig]++
	}
	for _, sig := range base {
		if inHead[sig] > 0 {
			inHead[sig]--
			continue
		}
		gone = append(gone, sig)
	}
	if len(gone) == 0 {
		return nil
	}
	return []CompatChange{{
		Class: ClassMappingChanged, Severity: severityOf(ClassMappingChanged), Spoke: spoke,
		Detail: fmt.Sprintf("spoke %s: the same paths are claimed by the same strategies, but wired differently — %s no longer describes any rule", spoke, strings.Join(gone, "; ")),
	}}
}

// ruleSignatures renders each rule as its strategy plus the exact hub and
// spoke paths it associates, sorted so the comparison is order-independent.
func ruleSignatures(sr engine.SpokeReport) []string {
	out := make([]string, 0, len(sr.RuleResults))
	for _, rr := range sr.RuleResults {
		hub := append([]string{}, rr.HubPaths...)
		spoke := append([]string{}, rr.SpokePaths...)
		sort.Strings(hub)
		sort.Strings(spoke)
		out = append(out, fmt.Sprintf("%s(hub:%s -> spoke:%s)", rr.Strategy, strings.Join(hub, ","), strings.Join(spoke, ",")))
	}
	sort.Strings(out)
	return out
}

func sameKeys(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func compareStrategies(spoke string, b, h engine.SpokeReport) []CompatChange {
	var out []CompatChange
	baseByPath := strategyByPath(b)
	headByPath := strategyByPath(h)

	for _, p := range sortedKeys(baseByPath) {
		hs, ok := headByPath[p]
		if !ok {
			continue // already reported as CoverageLost
		}
		bs := baseByPath[p]
		if bs != hs {
			out = append(out, CompatChange{
				Class: ClassStrategyChanged, Severity: severityOf(ClassStrategyChanged), Spoke: spoke,
				Detail: fmt.Sprintf("spoke %s: %q changed strategy from %s to %s", spoke, p, bs, hs),
			})
		}
	}

	// A rule index present before and gone now, whose paths nobody else
	// claims, is a removal rather than an edit.
	baseRules := ruleIndexSet(b)
	headRules := ruleIndexSet(h)
	for _, idx := range sortedInts(baseRules) {
		if _, stillPresent := headRules[idx]; stillPresent {
			continue
		}
		rr := baseRules[idx]
		orphaned := false
		for _, p := range append(append([]string{}, rr.HubPaths...), rr.SpokePaths...) {
			if _, stillClaimed := headByPath[p]; !stillClaimed {
				orphaned = true
			}
		}
		if orphaned {
			out = append(out, CompatChange{
				Class: ClassRuleRemoved, Severity: severityOf(ClassRuleRemoved), Spoke: spoke,
				Detail: fmt.Sprintf("spoke %s: rule %d (%s) was removed and no replacement claims its paths", spoke, rr.Index, rr.Strategy),
			})
		}
	}
	return out
}

func claimedPaths(sr engine.SpokeReport) map[string]bool {
	out := map[string]bool{}
	for _, rr := range sr.RuleResults {
		for _, p := range append(append([]string{}, rr.HubPaths...), rr.SpokePaths...) {
			out[p] = true
		}
	}
	return out
}

func strategyByPath(sr engine.SpokeReport) map[string]engine.Strategy {
	out := map[string]engine.Strategy{}
	for _, rr := range sr.RuleResults {
		for _, p := range append(append([]string{}, rr.HubPaths...), rr.SpokePaths...) {
			out[p] = rr.Strategy
		}
	}
	return out
}

func ruleIndexSet(sr engine.SpokeReport) map[int]engine.RuleResult {
	out := map[int]engine.RuleResult{}
	for _, rr := range sr.RuleResults {
		out[rr.Index] = rr
	}
	return out
}

func spokeIndex(r engine.AnalyzeReport) map[string]engine.SpokeReport {
	out := map[string]engine.SpokeReport{}
	for _, sr := range r.SpokeReports {
		out[sr.Version] = sr
	}
	return out
}

func servedSet(versions []engine.VersionSchema) map[string]bool {
	out := map[string]bool{}
	for _, v := range versions {
		if v.Served {
			out[v.Name] = true
		}
	}
	return out
}

func severityRank(s string) int {
	switch s {
	case "breaking":
		return 0
	case "review":
		return 1
	}
	return 2
}

func sortedInts[V any](m map[int]V) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// WriteTable renders the report for a terminal.
func (r *CompatReport) WriteTable(w io.Writer) {
	_, _ = fmt.Fprintf(w, "Compatibility: %s → %s (%s)\n", r.Base, r.Head, r.Config)
	if len(r.Allowed) > 0 {
		_, _ = fmt.Fprintf(w, "Acknowledged classes: %s\n", strings.Join(r.Allowed, ", "))
	}
	for _, n := range r.Notes {
		_, _ = fmt.Fprintf(w, "NOTE: %s\n", n)
	}
	if len(r.Changes) == 0 {
		_, _ = fmt.Fprintln(w, "\nNo differences.")
		return
	}
	_, _ = fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SEVERITY\tCLASS\tDETAIL")
	for _, c := range r.Changes {
		sev := c.Severity
		if c.Acknowledged {
			sev += " (allowed)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", sev, c.Class, c.Detail)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w)
	if r.Breaking() {
		_, _ = fmt.Fprintln(w, "RESULT: breaking changes found. Acknowledge a deliberate one with --allow <class>.")
		return
	}
	_, _ = fmt.Fprintln(w, "RESULT: no unacknowledged breaking changes.")
}

// WriteMarkdown renders the report for a PR comment.
//
// Stable between runs with the same input — no timestamps, fixed ordering —
// so a sticky comment updates in place instead of producing a fresh diff
// every time CI runs.
func (r *CompatReport) WriteMarkdown(w io.Writer) {
	_, _ = fmt.Fprintf(w, "### Conversion compatibility: `%s` → `%s`\n\n", r.Base, r.Head)
	for _, n := range r.Notes {
		_, _ = fmt.Fprintf(w, "> **Note:** %s\n\n", n)
	}
	if len(r.Changes) == 0 {
		_, _ = fmt.Fprintln(w, "No differences.")
		return
	}
	if r.Breaking() {
		_, _ = fmt.Fprint(w, "**Breaking changes found.** Acknowledge a deliberate one with `--allow <class>`.\n\n")
	} else {
		_, _ = fmt.Fprint(w, "No unacknowledged breaking changes.\n\n")
	}
	_, _ = fmt.Fprintln(w, "| Severity | Class | Detail |")
	_, _ = fmt.Fprintln(w, "|---|---|---|")
	for _, c := range r.Changes {
		sev := c.Severity
		if c.Acknowledged {
			sev += " (allowed)"
		}
		_, _ = fmt.Fprintf(w, "| %s | `%s` | %s |\n", sev, c.Class, strings.ReplaceAll(c.Detail, "|", "\\|"))
	}
}
