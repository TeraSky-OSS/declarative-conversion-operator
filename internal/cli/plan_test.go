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
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const stagesDir = "../../examples/crossplane-xr-multiversion"

// stagePlan plans against one of the example stages, supplying the config
// only when that stage has one (stage 01 predates the conversion config).
func stagePlan(t *testing.T, stage, to string) *PlanReport {
	t.Helper()
	opts := PlanOptions{XRDPath: filepath.Join(stagesDir, stage, "xrd.yaml"), To: to}
	cfg := filepath.Join(stagesDir, stage, "xrdconversionconfig.yaml")
	if _, err := os.Stat(cfg); err == nil {
		opts.ConfigPath = cfg
	}
	rep, err := RunPlan(opts)
	if err != nil {
		t.Fatalf("plan %s --to %s: %v", stage, to, err)
	}
	return rep
}

// stagePlanVerified plans a stage with the cluster-only gates asserted, for
// the tests about what comes after them.
func stagePlanVerified(t *testing.T, stage, to string) *PlanReport {
	t.Helper()
	opts := PlanOptions{XRDPath: filepath.Join(stagesDir, stage, "xrd.yaml"), To: to, AssumeVerified: true}
	cfg := filepath.Join(stagesDir, stage, "xrdconversionconfig.yaml")
	if _, err := os.Stat(cfg); err == nil {
		opts.ConfigPath = cfg
	}
	rep, err := RunPlan(opts)
	if err != nil {
		t.Fatalf("plan %s --to %s: %v", stage, to, err)
	}
	return rep
}

func stepByTitle(rep *PlanReport, substr string) *PlanStep {
	for i := range rep.Steps {
		if strings.Contains(rep.Steps[i].Title, substr) {
			return &rep.Steps[i]
		}
	}
	return nil
}

// The six example stages are the only end-to-end record of what a correct
// migration looks like, so the plan has to agree with them. Each stage's
// "next step" is the one the README tells the operator to do there.
func TestRunPlan_TracksTheExampleStages(t *testing.T) {
	for _, tc := range []struct {
		stage, to string
		// wantNext is the title fragment of the step plan should offer, or
		// "" when nothing is outstanding that files can decide.
		wantNext string
	}{
		{"02-add-v2", "v2", "Promote v2 to the hub"},
		{"03-promote-v2", "v2", ""},
		{"04-add-v3", "v3", "Promote v3 to the hub"},
		{"05-promote-v3", "v3", ""},
		// v1 is unserved here, so the only thing left for it is dropping
		// the block — but that is a retirement step, withheld until the
		// operator confirms the cluster-only gates. See the dedicated test.
		{"06-deprecate-v1", "v3", ""},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			rep := stagePlan(t, tc.stage, tc.to)
			next := rep.NextStep()
			switch {
			case tc.wantNext == "" && next != nil:
				t.Fatalf("next = step %d %q, want nothing outstanding", next.Number, next.Title)
			case tc.wantNext == "":
				return
			case next == nil:
				t.Fatalf("next = nothing, want a step titled %q", tc.wantNext)
			case !strings.Contains(next.Title, tc.wantNext):
				t.Fatalf("next = %q, want a step titled %q", next.Title, tc.wantNext)
			}
		})
	}
}

// A stage that has already done a step must show it done, not re-offer it:
// re-offering "promote v2" to someone who has promoted v2 is how a plan
// stops being trusted.
func TestRunPlan_MarksCompletedStepsDone(t *testing.T) {
	rep := stagePlan(t, "03-promote-v2", "v2")
	for _, title := range []string{"Serve v2", "Wire v2 into the conversion config", "Promote v2 to the hub"} {
		s := stepByTitle(rep, title)
		if s == nil {
			t.Fatalf("no step titled %q in plan", title)
		}
		if s.Status != StepDone {
			t.Errorf("step %q status = %s, want %s", title, s.Status, StepDone)
		}
	}
	if rep.From != "v2" {
		t.Errorf("From = %q, want v2 (referenceable version)", rep.From)
	}
}

// Asking for a version the XRD does not declare is unreachable, and saying
// so beats printing a plan whose first step is impossible.
func TestRunPlan_RejectsUndeclaredTarget(t *testing.T) {
	_, err := RunPlan(PlanOptions{XRDPath: filepath.Join(stagesDir, "01-v1-only", "xrd.yaml"), To: "v2"})
	if err == nil {
		t.Fatal("want an error for a version that is not declared")
	}
	if !strings.Contains(err.Error(), "not declared") {
		t.Errorf("error = %v, want it to say the version is not declared", err)
	}
}

// --to is what makes a plan a plan; without it there is nothing to plan to.
func TestRunPlan_RequiresTo(t *testing.T) {
	_, err := RunPlan(PlanOptions{XRDPath: filepath.Join(stagesDir, "02-add-v2", "xrd.yaml")})
	if err == nil || !strings.Contains(err.Error(), "--to is required") {
		t.Fatalf("err = %v, want --to to be required", err)
	}
}

// Only one step is ever offered. Presenting every satisfiable step at once
// is how they get done out of order, which for this sequence means serving a
// version with no conversion behind it.
func TestBuildSteps_OffersExactlyOneStep(t *testing.T) {
	rep := stagePlan(t, "02-add-v2", "v2")
	ready := 0
	for _, s := range rep.Steps {
		if s.Status == StepReady {
			ready++
		}
	}
	if ready != 1 {
		t.Errorf("%d ready steps, want exactly 1", ready)
	}
	for _, s := range rep.Steps {
		if s.Status == StepBlocked && s.BlockedBy == "" {
			t.Errorf("step %d %q is blocked with no reason", s.Number, s.Title)
		}
	}
}

// A step whose completion cannot be read out of a manifest must say so
// rather than claim either state — and, crucially, must not block the steps
// after it, or a late-stage plan would hide its entire remaining tail.
func TestBuildSteps_ClusterOnlyStepsNeitherBlockNorAdvance(t *testing.T) {
	rep := stagePlanVerified(t, "06-deprecate-v1", "v3")
	migrate := stepByTitle(rep, "Migrate stored objects")
	if migrate == nil {
		t.Fatal("no storage-migration step")
	}
	if migrate.Status != StepUnknown {
		t.Errorf("storage migration status = %s, want %s", migrate.Status, StepUnknown)
	}
	if migrate.BlockedBy != clusterOnly {
		t.Errorf("storage migration blockedBy = %q, want the cluster-only note", migrate.BlockedBy)
	}
	next := rep.NextStep()
	if next == nil || next.Number <= migrate.Number {
		t.Fatalf("next = %v, want a step after the cluster-only one to still be reachable", next)
	}
}

// Every step has to carry a gate, and every actionable one a way to prove
// it. A step without a gate is just the prose that already exists.
func TestBuildSteps_EveryStepIsGatedAndVerifiable(t *testing.T) {
	rep := stagePlan(t, "04-add-v3", "v3")
	for _, s := range rep.Steps {
		if strings.TrimSpace(s.Gate) == "" {
			t.Errorf("step %d %q has no gate", s.Number, s.Title)
		}
		if strings.TrimSpace(s.Verify) == "" {
			t.Errorf("step %d %q has no verify command", s.Number, s.Title)
		}
		if strings.TrimSpace(s.Run) == "" {
			t.Errorf("step %d %q has no command to run", s.Number, s.Title)
		}
	}
}

// The promote gate is the one that matters most, and the reason is subtle
// enough that omitting it makes the whole plan unsafe: applying a config is
// not the same as the webhook converting, and the failure mode in between is
// silent — HTTP 200, stored object relabelled and unconverted.
func TestBuildSteps_PromoteGateNamesTheSilentFailure(t *testing.T) {
	rep := stagePlan(t, "02-add-v2", "v2")
	promote := stepByTitle(rep, "Promote v2 to the hub")
	if promote == nil {
		t.Fatal("no promote step")
	}
	for _, want := range []string{"UNCONVERTED", "generated CRD"} {
		if !strings.Contains(promote.Gate, want) {
			t.Errorf("promote gate does not mention %q:\n%s", want, promote.Gate)
		}
	}
	if !strings.Contains(promote.Verify, "--verify-propagation") {
		t.Errorf("promote verify = %q, want it to check propagation", promote.Verify)
	}
}

// A package-managed XRD has to have its conversion config applied BEFORE the
// package upgrade lands. That is the reverse of the unpackaged order, and
// getting it backwards serves a version with no conversion at all.
func TestBuildSteps_PackageManagedInvertsTheConfigOrdering(t *testing.T) {
	rep, err := RunPlan(PlanOptions{
		XRDPath:        filepath.Join(stagesDir, "02-add-v2", "xrd.yaml"),
		ConfigPath:     filepath.Join(stagesDir, "02-add-v2", "xrdconversionconfig.yaml"),
		To:             "v2",
		PackageManaged: true,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !rep.PackageManaged {
		t.Fatal("PackageManaged = false, want the forced flag to be honoured")
	}
	cfgStep := stepByTitle(rep, "Wire v2 into the conversion config")
	if cfgStep == nil {
		t.Fatal("no config step")
	}
	if !strings.Contains(cfgStep.Title, "BEFORE the package upgrade") {
		t.Errorf("config step title = %q, want it to flag the inverted ordering", cfgStep.Title)
	}

	var out bytes.Buffer
	rep.WriteTable(&out)
	if !strings.Contains(out.String(), "PACKAGE-MANAGED") {
		t.Errorf("table does not warn about package ordering:\n%s", out.String())
	}
}

// Detecting package management from ownerReferences is what makes the
// warning appear without the operator having to know to ask for it.
func TestOwnedByPackage_DetectsConfigurationRevisionOwner(t *testing.T) {
	xrd, err := LoadXRD(filepath.Join(stagesDir, "02-add-v2", "xrd.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if ownedByPackage(xrd) {
		t.Error("a hand-applied XRD was reported as package-managed")
	}
	xrd.SetOwnerReferences(append(xrd.GetOwnerReferences(), metaOwner("ConfigurationRevision")))
	if !ownedByPackage(xrd) {
		t.Error("an XRD owned by a ConfigurationRevision was not detected")
	}
}

// A served, undeprecated spoke is not retiring. Keeping old versions
// readable is the point of a conversion webhook, and a plan that told the
// operator to unserve v1 the moment v2 became the hub would be telling them
// to break every client still on v1.
func TestRetiringVersions_LeavesServedSpokesAlone(t *testing.T) {
	st := planState{
		hub: "v2",
		versions: []planVersion{
			{name: "v1", served: true},
			{name: "v2", served: true, referenceable: true},
		},
	}
	if got := retiringVersions(st, "v2"); len(got) != 0 {
		t.Errorf("retiring = %v, want nothing while v1 is still served and undeprecated", got)
	}
}

func TestRetiringVersions_RetiresTheReplacedHubAndDeprecatedVersions(t *testing.T) {
	st := planState{
		hub: "v1",
		versions: []planVersion{
			{name: "v0", served: false},
			{name: "v1", served: true, referenceable: true},
			{name: "v2", served: true, deprecated: true},
			{name: "v3", served: true},
		},
	}
	got := retiringVersions(st, "v3")
	want := []string{"v1", "v0", "v2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("retiring = %v, want %v (replaced hub, then unserved, then deprecated)", got, want)
	}
}

// The same sequence has to work for a native CRD, where the hub is the
// storage version and there are no Compositions to retarget. Emitting a
// retarget step for a plain CRD would send the operator after something that
// does not exist.
func TestRunPlan_NativeCRDOmitsCompositionRetargeting(t *testing.T) {
	rep, err := RunPlan(PlanOptions{
		CRDPath:    "../../examples/native-crd/crd.yaml",
		ConfigPath: "../../examples/native-crd/crdconversionconfig.yaml",
		To:         "v2",
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if rep.Kind != "CRD" {
		t.Errorf("Kind = %q, want CRD", rep.Kind)
	}
	if s := stepByTitle(rep, "Retarget Compositions"); s != nil {
		t.Errorf("native CRD plan includes step %d %q", s.Number, s.Title)
	}
	promote := stepByTitle(rep, "Promote v2 to the hub")
	if promote == nil {
		t.Fatal("no promote step")
	}
	if !strings.Contains(promote.Run, "storage: true") {
		t.Errorf("promote run = %q, want it to name the CRD's storage field, not referenceable", promote.Run)
	}
	for _, s := range rep.Steps {
		if strings.Contains(s.Verify, "--xrd") || strings.Contains(s.Run, "--xrd") {
			t.Errorf("step %d %q points at --xrd for a native CRD target", s.Number, s.Title)
		}
	}
}

// Numbering has to stay dense and ordered, because the blocked-by messages
// refer to steps by number.
func TestBuildSteps_NumbersAreDenseAndOrdered(t *testing.T) {
	rep := stagePlanVerified(t, "06-deprecate-v1", "v3")
	for i, s := range rep.Steps {
		if s.Number != i+1 {
			t.Fatalf("step at index %d has number %d, want %d", i, s.Number, i+1)
		}
	}
}

func metaOwner(kind string) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "pkg.crossplane.io/v1", Kind: kind, Name: "cfg-abc123", UID: "u"}
}

// A half-edited manifest with no referenceable version is not a state the
// apiserver would accept, but it is a state a file is in mid-edit — and this
// command reads files. Every clause naming the outgoing hub has to disappear
// rather than render empty, because what this command emits is commands to
// run, and "--hub " is worse than no advice at all.
func TestBuildSteps_NoHubYetEmitsNoEmptyVersionNames(t *testing.T) {
	st := planState{
		name: "xwidgets.example.org", kind: "XRD",
		versions: []planVersion{{name: "v1", served: true}, {name: "v2", served: true}},
		spokes:   map[string]bool{},
	}
	for _, s := range buildSteps(st, "v2", true) {
		for label, text := range map[string]string{"run": s.Run, "gate": s.Gate, "verify": s.Verify, "blocked": s.BlockedBy} {
			// Double spaces and bare flag endings are what an empty
			// version renders as; "--hub v2" must keep passing.
			for _, dangling := range []string{"--hub  ", "--spoke  ", "--check-unserve  ", "--version-pair :", "from  to", " on  for now", "LIVE OBJECTS at \n"} {
				if strings.Contains(text+"\n", dangling) {
					t.Errorf("step %d %q %s renders an empty version name (%q): %s", s.Number, s.Title, label, dangling, text)
				}
			}
			for _, bare := range []string{"--hub", "--spoke", "--check-unserve", "--to"} {
				if strings.HasSuffix(strings.TrimSpace(text), bare) {
					t.Errorf("step %d %q %s ends on a flag with no value: %s", s.Number, s.Title, label, text)
				}
			}
		}
	}
}

// The package-managed warning has to change the sequence, not just the
// prose. The plan offers the first outstanding step, so leaving "serve the
// version" ahead of "apply the config" would recommend exactly the order
// that opens the window the warning is about.
func TestBuildSteps_PackageManagedPutsTheConfigStepFirst(t *testing.T) {
	st := planState{
		name: "xwidgets.example.org", kind: "XRD", hub: "v1", packaged: true,
		versions: []planVersion{{name: "v1", served: true, referenceable: true}, {name: "v2"}},
		spokes:   map[string]bool{},
	}
	steps := buildSteps(st, "v2", true)
	if !strings.Contains(steps[0].Title, "conversion config") {
		t.Fatalf("step 1 is %q, want the conversion config first on a package-managed XRD", steps[0].Title)
	}
	if !strings.Contains(steps[1].Title, "Serve v2") {
		t.Fatalf("step 2 is %q, want serving v2 second", steps[1].Title)
	}

	// And the un-packaged order is still the other way around.
	st.packaged = false
	plain := buildSteps(st, "v2", true)
	if !strings.Contains(plain[0].Title, "Serve v2") {
		t.Fatalf("step 1 is %q, want serving v2 first when the XRD is hand-applied", plain[0].Title)
	}
}

// Promotion is two edits: the XRD's referenceable version and the config's
// hubVersion. A target half-way through is converting toward a version
// nothing is stored at, and must not read as done — the cluster-only steps
// after it do not block, so "done" here would let retirement come next.
func TestBuildSteps_PromotionIsNotDoneUntilTheConfigAgrees(t *testing.T) {
	st := planState{
		name: "xwidgets.example.org", kind: "XRD", hub: "v2",
		versions:  []planVersion{{name: "v1", served: true}, {name: "v2", served: true, referenceable: true}},
		hasConfig: true, configName: "c", configHub: "v1",
		spokes: map[string]bool{"v2": true},
	}
	promote := stepByTitle(&PlanReport{Steps: buildSteps(st, "v2", true)}, "Promote v2 to the hub")
	if promote == nil {
		t.Fatal("no promote step")
	}
	if promote.Status != StepReady {
		t.Errorf("promote status = %s, want %s while the config still names v1", promote.Status, StepReady)
	}
	if !strings.Contains(promote.BlockedBy, "spec.hubVersion: v1") {
		t.Errorf("blockedBy = %q, want it to name the config's stale hubVersion", promote.BlockedBy)
	}

	st.configHub = "v2"
	promote = stepByTitle(&PlanReport{Steps: buildSteps(st, "v2", true)}, "Promote v2 to the hub")
	if promote.Status != StepDone {
		t.Errorf("promote status = %s, want %s once both halves agree", promote.Status, StepDone)
	}
}

// Un-serving a version is the one step here that cannot be walked back, and
// whether it is safe rests entirely on facts this command cannot read from a
// file. Offering it on the strength of a manifest would be the worst
// recommendation the tool could make, so it is withheld until the operator
// says they ran the verify commands.
func TestBuildSteps_RetirementIsWithheldUntilTheClusterGatesAreAsserted(t *testing.T) {
	rep := stagePlan(t, "06-deprecate-v1", "v3")
	drop := stepByTitle(rep, "Drop the v1 version block")
	if drop == nil {
		t.Fatal("the retirement step is missing entirely; it should be shown, just not offered")
	}
	if drop.Status != StepBlocked {
		t.Errorf("drop step status = %s, want %s while storage is unconfirmed", drop.Status, StepBlocked)
	}
	if !strings.Contains(drop.BlockedBy, "--assume-verified") {
		t.Errorf("blockedBy = %q, want it to say how to proceed", drop.BlockedBy)
	}
	if next := rep.NextStep(); next != nil {
		t.Errorf("next = %q, want nothing offered while retirement is the only thing left", next.Title)
	}

	var out bytes.Buffer
	rep.WriteTable(&out)
	if !strings.Contains(out.String(), "--assume-verified") {
		t.Errorf("the summary does not tell the operator how to proceed:\n%s", out.String())
	}

	// Asserted, the same plan offers it.
	verified := stagePlanVerified(t, "06-deprecate-v1", "v3")
	next := verified.NextStep()
	if next == nil || !strings.Contains(next.Title, "Drop the v1 version block") {
		t.Fatalf("next = %v, want the drop step once the cluster gates are asserted", next)
	}
}
