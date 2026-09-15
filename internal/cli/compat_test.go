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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// compatRepo builds a throwaway git repository with a base and a head
// revision, so the command is exercised through real `git show` rather than
// through a stub. That matters: resolving revisions without a checkout is
// half of what this command has to get right.
type compatRepo struct {
	dir string
	t   *testing.T
}

func newCompatRepo(t *testing.T) *compatRepo {
	t.Helper()
	r := &compatRepo{dir: t.TempDir(), t: t}
	r.git("init", "-q")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "test")
	return r
}

func (r *compatRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, errb.String())
	}
	return out.String()
}

func (r *compatRepo) write(name, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *compatRepo) commit(msg string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "-m", msg)
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

// run executes compat from inside the repo, because git show resolves
// relative to the working directory.
func (r *compatRepo) run(opts CompatOptions) *CompatReport {
	r.t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.Chdir(r.dir); err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()

	rep, err := RunCompat(opts)
	if err != nil {
		r.t.Fatalf("RunCompat: %v", err)
	}
	return rep
}

const compatXRD = `apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata:
  name: xthings.compat.example.org
spec:
  scope: Namespaced
  group: compat.example.org
  names:
    kind: XThing
    plural: xthings
  versions:
    - name: v2
      served: true
      referenceable: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                size: {type: string}
                tier: {type: string}
    - name: v1
      served: %s
      referenceable: false
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                storageSize: {type: string}
                tier: {type: string}
`

func compatConfig(rules string) string {
	return `apiVersion: terasky.com/v1alpha1
kind: XRDConversionConfig
metadata:
  name: things-conversion
spec:
  targetXRD:
    name: xthings.compat.example.org
  hubVersion: v2
  spokes:
    - version: v1
      rules:
` + rules
}

const renameBoth = `        - strategy: FieldRename
          fieldRename:
            hubPath: spec.size
            spokePath: spec.storageSize
        - strategy: FieldRename
          fieldRename:
            hubPath: spec.tier
            spokePath: spec.tier
`

func hasClass(rep *CompatReport, class string) *CompatChange {
	for i := range rep.Changes {
		if rep.Changes[i].Class == class {
			return &rep.Changes[i]
		}
	}
	return nil
}

func TestCompat_NoChangeIsClean(t *testing.T) {
	r := newCompatRepo(t)
	r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "true", 1))
	r.write("config.yaml", compatConfig(renameBoth))
	base := r.commit("base")
	r.write("README.md", "unrelated")
	head := r.commit("head")

	rep := r.run(CompatOptions{Base: base, Head: head, ConfigPath: "config.yaml", XRDPath: "xrd.yaml"})
	if len(rep.Changes) != 0 {
		t.Errorf("an unrelated change produced %d difference(s): %+v", len(rep.Changes), rep.Changes)
	}
	if rep.Breaking() {
		t.Error("clean comparison reported breaking")
	}
}

func TestCompat_DetectsEachBreakingClass(t *testing.T) {
	t.Run("CoverageLost and RuleRemoved", func(t *testing.T) {
		r := newCompatRepo(t)
		r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "true", 1))
		r.write("config.yaml", compatConfig(renameBoth))
		base := r.commit("base")
		// Drop the tier rule entirely.
		r.write("config.yaml", compatConfig(`        - strategy: FieldRename
          fieldRename:
            hubPath: spec.size
            spokePath: spec.storageSize
`))
		head := r.commit("drop a rule")

		rep := r.run(CompatOptions{Base: base, Head: head, ConfigPath: "config.yaml", XRDPath: "xrd.yaml"})
		if c := hasClass(rep, ClassCoverageLost); c == nil {
			t.Errorf("a field that lost its rule was not reported: %+v", rep.Changes)
		} else if !strings.Contains(c.Detail, "spec.tier") {
			t.Errorf("CoverageLost does not name the field: %s", c.Detail)
		}
		if hasClass(rep, ClassRuleRemoved) == nil {
			t.Errorf("a removed rule was not reported: %+v", rep.Changes)
		}
		if !rep.Breaking() {
			t.Error("dropping a rule should be breaking")
		}
	})

	t.Run("ServedVersionRemoved", func(t *testing.T) {
		r := newCompatRepo(t)
		r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "true", 1))
		r.write("config.yaml", compatConfig(renameBoth))
		base := r.commit("base")
		r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "false", 1))
		head := r.commit("stop serving v1")

		rep := r.run(CompatOptions{Base: base, Head: head, ConfigPath: "config.yaml", XRDPath: "xrd.yaml"})
		c := hasClass(rep, ClassServedVersionRemove)
		if c == nil {
			t.Fatalf("un-serving a version was not reported: %+v", rep.Changes)
		}
		if !strings.Contains(c.Detail, "check-unserve") {
			t.Errorf("the message should point at the safety check: %s", c.Detail)
		}
		if !rep.Breaking() {
			t.Error("un-serving a version should be breaking")
		}
	})

	t.Run("HubChanged", func(t *testing.T) {
		r := newCompatRepo(t)
		xrd := strings.Replace(compatXRD, "%s", "true", 1)
		r.write("xrd.yaml", xrd)
		r.write("config.yaml", compatConfig(renameBoth))
		base := r.commit("base")
		// Move the hub to v1: referenceable moves with it.
		moved := strings.Replace(xrd, "    - name: v2\n      served: true\n      referenceable: true", "    - name: v2\n      served: true\n      referenceable: false", 1)
		moved = strings.Replace(moved, "    - name: v1\n      served: true\n      referenceable: false", "    - name: v1\n      served: true\n      referenceable: true", 1)
		r.write("xrd.yaml", moved)
		r.write("config.yaml", strings.Replace(compatConfig(`        - strategy: FieldRename
          fieldRename:
            hubPath: spec.storageSize
            spokePath: spec.size
        - strategy: FieldRename
          fieldRename:
            hubPath: spec.tier
            spokePath: spec.tier
`), "hubVersion: v2\n  spokes:\n    - version: v1", "hubVersion: v1\n  spokes:\n    - version: v2", 1))
		head := r.commit("promote the hub")

		rep := r.run(CompatOptions{Base: base, Head: head, ConfigPath: "config.yaml", XRDPath: "xrd.yaml"})
		if hasClass(rep, ClassHubChanged) == nil {
			t.Fatalf("a hub promotion was not reported: %+v", rep.Changes)
		}
		if !rep.Breaking() {
			t.Error("a hub change should be breaking until acknowledged")
		}

		// ...and a deliberate one passes with an explicit acknowledgement,
		// rather than by turning the gate off.
		ack := r.run(CompatOptions{Base: base, Head: head, ConfigPath: "config.yaml", XRDPath: "xrd.yaml", Allow: []string{ClassHubChanged}})
		if c := hasClass(ack, ClassHubChanged); c == nil || !c.Acknowledged {
			t.Error("--allow did not mark the class acknowledged")
		}
	})

	t.Run("StrategyChanged is review, not breaking", func(t *testing.T) {
		r := newCompatRepo(t)
		r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "true", 1))
		r.write("config.yaml", compatConfig(renameBoth))
		base := r.commit("base")
		r.write("config.yaml", compatConfig(`        - strategy: FieldRename
          fieldRename:
            hubPath: spec.size
            spokePath: spec.storageSize
        - strategy: Constant
          constant:
            path: spec.tier
            existsOn: Spoke
            value: gold
          acknowledgeLossy: true
          reason: "test fixture"
`))
		head := r.commit("change a strategy")

		rep := r.run(CompatOptions{Base: base, Head: head, ConfigPath: "config.yaml", XRDPath: "xrd.yaml"})
		if hasClass(rep, ClassStrategyChanged) == nil {
			t.Errorf("a strategy change was not reported: %+v", rep.Changes)
		}
	})
}

func TestCompat_CoverageGainedIsSafe(t *testing.T) {
	r := newCompatRepo(t)
	r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "true", 1))
	r.write("config.yaml", compatConfig(`        - strategy: FieldRename
          fieldRename:
            hubPath: spec.size
            spokePath: spec.storageSize
`))
	base := r.commit("base")
	r.write("config.yaml", compatConfig(renameBoth))
	head := r.commit("cover another field")

	rep := r.run(CompatOptions{Base: base, Head: head, ConfigPath: "config.yaml", XRDPath: "xrd.yaml"})
	if hasClass(rep, ClassCoverageGained) == nil {
		t.Errorf("newly covered field was not reported: %+v", rep.Changes)
	}
	if rep.Breaking() {
		t.Error("gaining coverage must not be breaking")
	}
}

// Markdown has to be stable between runs on the same input, or a sticky PR
// comment produces a fresh diff every time CI runs.
func TestCompat_MarkdownIsStable(t *testing.T) {
	r := newCompatRepo(t)
	r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "true", 1))
	r.write("config.yaml", compatConfig(renameBoth))
	base := r.commit("base")
	r.write("config.yaml", compatConfig(`        - strategy: FieldRename
          fieldRename:
            hubPath: spec.size
            spokePath: spec.storageSize
`))
	head := r.commit("drop a rule")

	var a, b bytes.Buffer
	r.run(CompatOptions{Base: base, Head: head, ConfigPath: "config.yaml", XRDPath: "xrd.yaml"}).WriteMarkdown(&a)
	r.run(CompatOptions{Base: base, Head: head, ConfigPath: "config.yaml", XRDPath: "xrd.yaml"}).WriteMarkdown(&b)
	if a.String() != b.String() {
		t.Errorf("markdown differs between two runs on the same input:\n--- a ---\n%s\n--- b ---\n%s", a.String(), b.String())
	}
	if !strings.Contains(a.String(), "| Severity | Class | Detail |") {
		t.Errorf("markdown is not a table: %s", a.String())
	}
}

// A shallow clone is the normal CI case; git show must not need history.
func TestCompat_WorksAgainstShortRefs(t *testing.T) {
	r := newCompatRepo(t)
	r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "true", 1))
	r.write("config.yaml", compatConfig(renameBoth))
	r.commit("base")
	r.write("config.yaml", compatConfig(`        - strategy: FieldRename
          fieldRename:
            hubPath: spec.size
            spokePath: spec.storageSize
`))
	r.commit("head")

	rep := r.run(CompatOptions{Base: "HEAD~1", Head: "HEAD", ConfigPath: "config.yaml", XRDPath: "xrd.yaml"})
	if hasClass(rep, ClassCoverageLost) == nil {
		t.Errorf("HEAD~1..HEAD did not resolve: %+v", rep.Changes)
	}
}

func TestCompat_UnresolvableRefIsAClearError(t *testing.T) {
	r := newCompatRepo(t)
	r.write("config.yaml", compatConfig(renameBoth))
	r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "true", 1))
	r.commit("base")

	wd, _ := os.Getwd()
	_ = os.Chdir(r.dir)
	defer func() { _ = os.Chdir(wd) }()

	_, err := RunCompat(CompatOptions{Base: "nope", Head: "HEAD", ConfigPath: "config.yaml", XRDPath: "xrd.yaml"})
	if err == nil {
		t.Fatal("expected an error for an unresolvable revision")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("the error should name the revision: %v", err)
	}
}

func TestCompat_LosslessToLossyIsBreaking(t *testing.T) {
	r := newCompatRepo(t)
	r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "true", 1))
	r.write("config.yaml", compatConfig(renameBoth))
	base := r.commit("base")
	// Same paths, but tier now comes from a constant: the value a user set
	// on the hub no longer survives the round trip.
	r.write("config.yaml", compatConfig(`        - strategy: FieldRename
          fieldRename:
            hubPath: spec.size
            spokePath: spec.storageSize
        - strategy: Constant
          constant:
            path: spec.tier
            existsOn: Spoke
            value: gold
          acknowledgeLossy: true
          reason: "test fixture"
`))
	head := r.commit("make a direction lossy")

	rep := r.run(CompatOptions{Base: base, Head: head, ConfigPath: "config.yaml", XRDPath: "xrd.yaml"})
	c := hasClass(rep, ClassLosslessToLossy)
	if c == nil {
		t.Fatalf("a direction that stopped round-tripping was not reported: %+v", rep.Changes)
	}
	if c.Severity != "breaking" {
		t.Errorf("severity = %q, want breaking", c.Severity)
	}
	if !strings.Contains(c.Detail, "v1") {
		t.Errorf("detail should name the spoke: %s", c.Detail)
	}
}

func TestCompat_NewVersionIsSafe(t *testing.T) {
	r := newCompatRepo(t)
	// Start with v1 unserved, then serve it: that is a newly served version.
	r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "false", 1))
	r.write("config.yaml", compatConfig(renameBoth))
	base := r.commit("base")
	r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "true", 1))
	head := r.commit("serve v1")

	rep := r.run(CompatOptions{Base: base, Head: head, ConfigPath: "config.yaml", XRDPath: "xrd.yaml"})
	c := hasClass(rep, ClassNewVersion)
	if c == nil {
		t.Fatalf("a newly served version was not reported: %+v", rep.Changes)
	}
	if c.Severity != "safe" {
		t.Errorf("severity = %q, want safe", c.Severity)
	}
	if rep.Breaking() {
		t.Error("serving a new version must not be breaking")
	}
}

// --allow must acknowledge only the class named, not disable the gate.
func TestCompat_AllowIsPerClass(t *testing.T) {
	r := newCompatRepo(t)
	r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "true", 1))
	r.write("config.yaml", compatConfig(renameBoth))
	base := r.commit("base")
	r.write("xrd.yaml", strings.Replace(compatXRD, "%s", "false", 1))
	r.write("config.yaml", compatConfig(`        - strategy: FieldRename
          fieldRename:
            hubPath: spec.size
            spokePath: spec.storageSize
`))
	head := r.commit("un-serve and drop a rule")

	// Acknowledging only the un-serve leaves the dropped rule breaking.
	rep := r.run(CompatOptions{Base: base, Head: head, ConfigPath: "config.yaml", XRDPath: "xrd.yaml", Allow: []string{ClassServedVersionRemove}})
	if c := hasClass(rep, ClassServedVersionRemove); c == nil || !c.Acknowledged {
		t.Error("the acknowledged class was not marked")
	}
	if !rep.Breaking() {
		t.Error("--allow of one class must not acknowledge the others")
	}
}

// `git show <rev>:<path>` resolves a bare path from the repository root, not
// from the working directory — so a path that every other command in this
// CLI reads relative to where you are standing would fail here with "does
// not exist in HEAD" from any subdirectory. The ./ prefix is what makes the
// two agree.
func TestGitPathspec_ResolvesRelativeToTheWorkingDirectory(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"config.yaml", "./config.yaml"},
		{"examples/native-crd/crd.yaml", "./examples/native-crd/crd.yaml"},
		{"./config.yaml", "./config.yaml"},
		{"../sibling/config.yaml", "../sibling/config.yaml"},
	} {
		got, err := gitPathspec(tc.in)
		if err != nil {
			t.Fatalf("gitPathspec(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("gitPathspec(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// An absolute path is not something git accepts after the colon at all, so
// it has to become a path relative to where git will resolve it from.
func TestGitPathspec_RewritesAbsolutePaths(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	got, err := gitPathspec(filepath.Join(wd, "testdata", "config.yaml"))
	if err != nil {
		t.Fatalf("gitPathspec: %v", err)
	}
	if got != "./testdata/config.yaml" {
		t.Errorf("gitPathspec(abs) = %q, want ./testdata/config.yaml", got)
	}
}

// The regression this class exists for is per direction, so a spoke that was
// already lossy one way has to be watched in the other. Requiring the base
// to have been lossless in BOTH directions lets exactly that case through,
// and the gate passes a conversion that stopped round-tripping.
func TestClassify_ReportsADirectionThatRegressedWhileTheOtherWasAlreadyLossy(t *testing.T) {
	at := func(hubToSpoke, spokeToHub bool) analysisAt {
		return analysisAt{
			hub: "v2",
			report: engine.AnalyzeReport{SpokeReports: []engine.SpokeReport{{
				Version:  "v1",
				Lossless: engine.LosslessVerdict{HubToSpoke: hubToSpoke, SpokeToHub: spokeToHub},
			}}},
			versions: []engine.VersionSchema{{Name: "v1", Served: true}, {Name: "v2", Served: true}},
		}
	}

	// Already lossy spoke→hub at the base; hub→spoke then regresses.
	got := classify(at(true, false), at(false, false))
	found := false
	for _, c := range got {
		if c.Class == ClassLosslessToLossy {
			found = true
			if !strings.Contains(c.Detail, "hub→spoke") {
				t.Errorf("detail should name the direction that regressed: %s", c.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("a hub→spoke regression went unreported because spoke→hub was already lossy: %+v", got)
	}

	// And a direction that was already lossy at the base is not re-reported.
	for _, c := range classify(at(true, false), at(true, false)) {
		if c.Class == ClassLosslessToLossy {
			t.Errorf("no direction changed, but a regression was reported: %+v", c)
		}
	}
}

// Swapping two renames leaves every claimed path, every strategy, every rule
// index and the aggregate losslessness verdict identical — and changes what
// the conversion does to every object. Every other comparison here works
// from those aggregates, so without a per-rule signature the gate sees
// nothing at all.
func TestClassify_DetectsReWiredMappingsWithUnchangedPaths(t *testing.T) {
	at := func(a, b string) analysisAt {
		return analysisAt{
			hub: "v2",
			report: engine.AnalyzeReport{SpokeReports: []engine.SpokeReport{{
				Version: "v1",
				RuleResults: []engine.RuleResult{
					{Index: 0, Strategy: engine.StrategyFieldRename, HubPaths: []string{"spec.a"}, SpokePaths: []string{a}},
					{Index: 1, Strategy: engine.StrategyFieldRename, HubPaths: []string{"spec.b"}, SpokePaths: []string{b}},
				},
			}}},
			versions: []engine.VersionSchema{{Name: "v1", Served: true}, {Name: "v2", Served: true}},
		}
	}
	got := classify(at("spec.x", "spec.y"), at("spec.y", "spec.x"))
	found := false
	for _, c := range got {
		if c.Class == ClassMappingChanged {
			found = true
			if c.Severity != "review" {
				t.Errorf("severity = %q, want review", c.Severity)
			}
		}
	}
	if !found {
		t.Fatalf("two renames swapped destinations and nothing was reported: %+v", got)
	}

	// An unchanged rule set reports nothing.
	for _, c := range classify(at("spec.x", "spec.y"), at("spec.x", "spec.y")) {
		if c.Class == ClassMappingChanged {
			t.Errorf("nothing changed, but a mapping change was reported: %+v", c)
		}
	}
}

// A version that gains its first rule set in the head revision has no base
// entry at all, so iterating only the base spokes reported nothing for it.
func TestClassify_ReportsCoverageGainedOnAHeadOnlySpoke(t *testing.T) {
	versions := []engine.VersionSchema{{Name: "v1", Served: true}, {Name: "v2", Served: true}}
	base := analysisAt{hub: "v2", report: engine.AnalyzeReport{}, versions: versions}
	head := analysisAt{
		hub: "v2",
		report: engine.AnalyzeReport{SpokeReports: []engine.SpokeReport{{
			Version: "v1",
			RuleResults: []engine.RuleResult{
				{Index: 0, Strategy: engine.StrategyFieldRename, HubPaths: []string{"spec.a"}, SpokePaths: []string{"spec.x"}},
			},
		}}},
		versions: versions,
	}
	got := classify(base, head)
	if len(got) == 0 {
		t.Fatal("a spoke that gained its first rule set was reported as no differences")
	}
	found := false
	for _, c := range got {
		if c.Class == ClassCoverageGained {
			found = true
		}
	}
	if !found {
		t.Errorf("want CoverageGained for the new spoke, got %+v", got)
	}
}

// "No differences" between two revisions that both fail to compile reads as
// a pass. The report says which side it could not fully analyze.
func TestAnalysisNotes_SayWhichRevisionDoesNotCompile(t *testing.T) {
	broken := engine.AnalyzeReport{SpokeReports: []engine.SpokeReport{{
		Version: "v1",
		Errors:  []engine.Diagnostic{{Severity: engine.SeverityError, Message: "spec.tier is not covered by any rule"}},
	}}}
	notes := analysisNotes("base-sha", broken, "head-sha", engine.AnalyzeReport{})
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want exactly one (the base)", notes)
	}
	if !strings.Contains(notes[0], "base-sha") || !strings.Contains(notes[0], "spec.tier") {
		t.Errorf("note should name the revision and the first error: %s", notes[0])
	}
	if len(analysisNotes("base-sha", engine.AnalyzeReport{}, "head-sha", engine.AnalyzeReport{})) != 0 {
		t.Error("two clean revisions should produce no notes")
	}
}

// Every class the classifier can emit has to be acknowledgeable, or a
// deliberate change has no way past the gate except switching it off.
func TestKnownCompatClass_CoversEveryClassTheClassifierEmits(t *testing.T) {
	for _, c := range []string{
		ClassLosslessToLossy, ClassCoverageLost, ClassServedVersionRemove,
		ClassRuleRemoved, ClassHubChanged, ClassStrategyChanged,
		ClassMappingChanged, ClassCoverageGained, ClassNewVersion,
	} {
		if !knownCompatClass(c) {
			t.Errorf("--allow %s is rejected, but classify can emit it", c)
		}
		if severityOf(c) == "" {
			t.Errorf("%s has no severity", c)
		}
	}
}
