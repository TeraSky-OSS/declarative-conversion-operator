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
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Step statuses.
const (
	StepDone  = "done"
	StepReady = "ready"
	// StepUnknown is a step whose completion cannot be read out of a
	// manifest — retargeting Compositions, migrating storage, pruning
	// storedVersions. Distinct from ready on purpose: claiming "ready" for
	// something the tool has not checked would be a lie, and marking it
	// blocked would stall every later step that *is* decidable.
	StepUnknown = "unknown"
	StepBlocked = "blocked"
)

// PlanStep is one gated step on the path to the requested state.
//
// Every step carries the command to run, the condition that has to hold
// before the next one is safe, and the command that proves it. A plan that
// only listed actions would be the prose that already exists spread across
// three documents; the gates are the part an operator currently has to hold
// in their head.
type PlanStep struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	// Run is the exact command, with flags filled in from the live state.
	Run string `json:"run,omitempty"`
	// Gate states, as a checkable condition, when it is safe to proceed.
	Gate string `json:"gate"`
	// Verify is the command that proves the gate holds.
	Verify string `json:"verify,omitempty"`
	Status string `json:"status"`
	// BlockedBy says what is blocking, never just that something is.
	BlockedBy string `json:"blockedBy,omitempty"`
}

// PlanReport is the ordered path from where the target is to where the user
// asked it to be.
type PlanReport struct {
	Resource string `json:"resource"`
	Kind     string `json:"kind"`
	Config   string `json:"config,omitempty"`
	From     string `json:"from"`
	To       string `json:"to"`
	// PackageManaged reorders the sequence: for an XRD shipped in a
	// Configuration, the conversion config must be applied before the
	// package upgrade lands, which is the reverse of the unpackaged order.
	PackageManaged bool       `json:"packageManaged"`
	Steps          []PlanStep `json:"steps"`
}

// PlanOptions configures the planner.
type PlanOptions struct {
	XRDPath    string
	CRDPath    string
	ConfigPath string
	To         string
	// PackageManaged forces the package-managed ordering. When a live XRD
	// is read it is derived from ownership instead.
	PackageManaged bool
	// AssumeVerified is the operator asserting that the steps this command
	// cannot check from files -- retargeting Compositions, migrating
	// storage, pruning storedVersions -- have been confirmed with their
	// verify commands. Retiring a version is irreversible and depends on
	// exactly those facts, so without this the retirement steps are never
	// offered as the next thing to do.
	AssumeVerified bool
}

// planState is everything the planner learned about the target.
type planState struct {
	name       string
	kind       string
	hub        string
	versions   []planVersion
	configName string
	spokes     map[string]bool
	configHub  string
	hasConfig  bool
	packaged   bool
	// native distinguishes a plain CRD from an XRD: there are no
	// Compositions pointed at a CRD, and no package to order against.
	native bool
}

type planVersion struct {
	name          string
	served        bool
	referenceable bool
	deprecated    bool
}

// RunPlan determines where the target is and prints the ordered, gated path
// to where the user asked to be. Read-only: it constructs no write client
// and executes none of the steps.
func RunPlan(opts PlanOptions) (*PlanReport, error) {
	if opts.To == "" {
		return nil, errors.New("--to is required: name the version to make the hub")
	}
	st, err := loadPlanState(opts)
	if err != nil {
		return nil, err
	}

	target := ""
	for _, v := range st.versions {
		if v.name == opts.To {
			target = v.name
		}
	}
	if target == "" {
		return nil, fmt.Errorf("version %q is not declared on %s; add the version block to the %s first, then re-run", opts.To, st.name, st.kind)
	}

	rep := &PlanReport{
		Resource: st.name, Kind: st.kind, Config: st.configName,
		From: st.hub, To: opts.To, PackageManaged: st.packaged,
	}
	rep.Steps = buildSteps(st, opts.To, opts.AssumeVerified)
	return rep, nil
}

func loadPlanState(opts PlanOptions) (planState, error) {
	if opts.CRDPath != "" {
		return loadCRDPlanState(opts)
	}
	return loadXRDPlanState(opts)
}

// loadCRDPlanState reads the same state out of a native CRD. A CRD carries
// it more directly than an XRD does — storage instead of referenceable, and
// no Compositions to retarget — but the sequence and its gates are the same.
func loadCRDPlanState(opts PlanOptions) (planState, error) {
	var st planState
	crd, err := LoadCRD(opts.CRDPath)
	if err != nil {
		return st, err
	}
	st.name = crdName(crd)
	st.kind = "CRD"
	st.native = true
	for _, v := range crd.Spec.Versions {
		st.versions = append(st.versions, planVersion{
			name: v.Name, served: v.Served, referenceable: v.Storage, deprecated: v.Deprecated,
		})
		if v.Storage {
			st.hub = v.Name
		}
	}
	if len(st.versions) == 0 {
		return st, fmt.Errorf("%s has no spec.versions", st.name)
	}

	st.spokes = map[string]bool{}
	if opts.ConfigPath != "" {
		cfg, cerr := LoadCRDConfig(opts.ConfigPath)
		if cerr != nil {
			return st, cerr
		}
		st.hasConfig = true
		st.configName = cfg.Name
		st.configHub = cfg.Spec.HubVersion
		for _, sp := range cfg.Spec.Spokes {
			st.spokes[sp.Version] = true
		}
	}
	return st, nil
}

func loadXRDPlanState(opts PlanOptions) (planState, error) {
	var st planState
	if opts.XRDPath == "" {
		return st, errors.New("--xrd or --crd is required")
	}

	xrd, err := LoadXRD(opts.XRDPath)
	if err != nil {
		return st, err
	}
	st.name = xrdName(xrd)
	st.kind = "XRD"
	st.packaged = opts.PackageManaged || ownedByPackage(xrd)

	raw, found, err := unstructured.NestedSlice(xrd.Object, "spec", "versions")
	if err != nil || !found {
		return st, fmt.Errorf("%s has no spec.versions", st.name)
	}
	for _, r := range raw {
		vm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(vm, "name")
		served, _, _ := unstructured.NestedBool(vm, "served")
		ref, _, _ := unstructured.NestedBool(vm, "referenceable")
		dep, _, _ := unstructured.NestedBool(vm, "deprecated")
		st.versions = append(st.versions, planVersion{name: name, served: served, referenceable: ref, deprecated: dep})
		if ref {
			st.hub = name
		}
	}

	st.spokes = map[string]bool{}
	if opts.ConfigPath != "" {
		cfg, cerr := LoadConfig(opts.ConfigPath)
		if cerr != nil {
			return st, cerr
		}
		st.hasConfig = true
		st.configName = cfg.Name
		st.configHub = cfg.Spec.HubVersion
		for _, s := range cfg.Spec.Spokes {
			st.spokes[s.Version] = true
		}
	}
	return st, nil
}

// ownedByPackage reports whether Crossplane's package establisher owns this
// XRD, which changes the safe ordering — see buildSteps.
func ownedByPackage(xrd *unstructured.Unstructured) bool {
	for _, o := range xrd.GetOwnerReferences() {
		if o.Kind == "ConfigurationRevision" {
			return true
		}
	}
	return false
}

// buildSteps produces the ordered, gated path.
//
// The sequence is the one the docs describe in prose, made checkable: add the
// version, wire the spoke, verify conversion, verify propagation, promote the
// hub, retarget Compositions, migrate storage, prune, un-serve, drop.
func buildSteps(st planState, to string, assumeVerified bool) []PlanStep {
	var steps []PlanStep
	n := 0
	// The indexes of the steps that retire a version, which must not be
	// offered while the storage facts they depend on are unverified.
	var retirementIdx []int
	add := func(s PlanStep) {
		n++
		s.Number = n
		steps = append(steps, s)
	}

	target := versionByName(st.versions, to)
	oldHub := st.hub
	// A manifest with no hub at all is not a state Crossplane or the
	// apiserver will accept, but it is a state a half-edited file is in --
	// which is exactly what this command is pointed at. Every clause that
	// names the outgoing hub is dropped rather than rendered with an empty
	// version, because the output of this command is commands to run.
	hadHub := oldHub != ""

	// 1. The version has to exist and be served before anything can convert
	//    to it.
	served := target != nil && target.served
	serveStep := PlanStep{
		Title: fmt.Sprintf("Serve %s on the %s", to, st.kind),
		Run: ternary(hadHub,
			fmt.Sprintf("edit %s: set spec.versions[name=%s].served: true (leave %s on %s for now)", st.name, to, st.hubField(), oldHub),
			fmt.Sprintf("edit %s: set spec.versions[name=%s].served: true", st.name, to)),
		Gate:   to + " is served and the apiserver accepts reads at it",
		Verify: fmt.Sprintf("kubectl get %s %s -o jsonpath='{.spec.versions[?(@.name==\"%s\")].served}'", st.resourceType(), st.name, to),
		Status: doneIf(served),
		BlockedBy: blockedBecause(!served,
			fmt.Sprintf("spec.versions[name=%s].served is not true", to)),
	}

	// 2. The conversion config. For a package-managed XRD this has to come
	//    BEFORE the package upgrade lands, which is the reverse of the
	//    unpackaged order: otherwise the new served version exists for a
	//    while with no conversion at all, and reads at it return stored
	//    objects relabelled and unconverted.
	spokeWired := st.hasConfig && (st.spokes[to] || st.configHub == to)
	configStep := PlanStep{
		Title: fmt.Sprintf("Wire %s into the conversion config", to),
		Run: ternary(hadHub,
			fmt.Sprintf("convctl suggest %s --hub %s --spoke %s  # then apply the config", st.targetFlag(), oldHub, to),
			fmt.Sprintf("convctl suggest %s --hub %s  # then apply the config", st.targetFlag(), to)),
		Gate:   fmt.Sprintf("the config declares a rule set covering %s, and `convctl validate` is clean", to),
		Verify: fmt.Sprintf("convctl validate %s --config <config.yaml>", st.targetFlag()),
		Status: doneIf(spokeWired),
		BlockedBy: blockedBecause(!spokeWired,
			ternary(st.hasConfig,
				fmt.Sprintf("no spoke entry for %s in %s", to, st.configName),
				"no conversion config was supplied (--config)")),
	}
	// The package-managed ordering is the reverse of the hand-applied one,
	// and it has to be the reverse in the sequence rather than only in the
	// prose: the plan offers the first outstanding step, so leaving "serve
	// the version" first would recommend exactly the order that opens the
	// window this note warns about.
	if st.packaged {
		configStep.Title += " — BEFORE the package upgrade"
		configStep.Gate += ". This XRD is package-managed: the config must be applied before the Configuration revision that serves " + to + " lands, or the version is served with no conversion at all"
		serveStep.Run = "upgrade the Configuration to the revision whose XRD serves " + to
		serveStep.Gate = to + " is served and the apiserver accepts reads at it — with the conversion config already applied"
		add(configStep)
		add(serveStep)
	} else {
		add(serveStep)
		add(configStep)
	}

	// 3. Promote. The two verification gates live here rather than as steps
	//    of their own: whether a conversion has been exercised against real
	//    objects is not state that can be read back out of a manifest, so a
	//    "verify" step could never be marked done and every plan would stall
	//    on it forever. They are what makes this step safe, which is exactly
	//    what a gate is.
	// Promotion is two edits, not one. A target whose XRD names the new hub
	// while the config still names the old one is mid-promotion: the
	// webhook is converting toward a version nothing is stored at. Calling
	// that done would let retirement become the next step, because the
	// cluster-only steps in between do not block.
	promoted := st.hub == to && (!st.hasConfig || st.configHub == to)
	add(PlanStep{
		Title: fmt.Sprintf("Promote %s to the hub", to),
		Run: ternary(hadHub,
			fmt.Sprintf("edit %s: move %s: true from %s to %s, and set spec.hubVersion: %s in the config", st.name, st.hubField(), oldHub, to, to),
			fmt.Sprintf("edit %s: set %s: true on %s, and set spec.hubVersion: %s in the config", st.name, st.hubField(), to, to)),
		Gate: "conversion is verified against real objects AND the webhook has reached the generated CRD. " +
			"Applied is not the same as converting: until Crossplane re-renders the generated CRD, reads at a non-storage version " +
			"return stored objects relabelled but UNCONVERTED, with HTTP 200 and no error",
		Verify: ternary(hadHub,
			fmt.Sprintf("convctl test %s --config <config.yaml> --live --validate-output --verify-propagation --version-pair %s:%s", st.targetFlag(), oldHub, to),
			fmt.Sprintf("convctl test %s --config <config.yaml> --live --validate-output --verify-propagation", st.targetFlag())),
		Status:    doneIf(promoted),
		BlockedBy: blockedBecause(!promoted, promotionBlocker(st, to, oldHub, hadHub)),
	})

	// 4. Compositions follow the hub. Nothing points a compositeTypeRef at
	//    a native CRD, so this step exists only for XRDs.
	if !st.native {
		add(PlanStep{
			Title:     "Retarget Compositions at the new hub",
			Run:       fmt.Sprintf("convctl retarget --xrd %s --to %s", st.name, to),
			Gate:      fmt.Sprintf("every Composition's compositeTypeRef.apiVersion names %s, and every XR's compositionRef points at a retargeted Composition", to),
			Verify:    fmt.Sprintf("convctl retarget --xrd %s --to %s --dry-run", st.name, to),
			Status:    StepUnknown,
			BlockedBy: clusterOnly,
		})
	}

	// 5-6. Storage.
	add(PlanStep{
		Title: "Migrate stored objects to " + to,
		Run:   fmt.Sprintf("convctl migrate-storage %s %s", st.targetFlagName(), st.name),
		Gate:  "every object has been rewritten at the new storage version",
		Verify: ternary(hadHub,
			fmt.Sprintf("convctl versions %s --config <config.yaml>  # LIVE OBJECTS at %s", st.targetFlag(), oldHub),
			fmt.Sprintf("convctl versions %s --config <config.yaml>  # LIVE OBJECTS per version", st.targetFlag())),
		Status:    StepUnknown,
		BlockedBy: clusterOnly,
	})
	add(PlanStep{
		Title: "Prune storedVersions",
		Run:   fmt.Sprintf("convctl migrate-storage %s %s --prune-stored-versions", st.targetFlagName(), st.name),
		Gate:  "status.storedVersions lists only " + to,
		Verify: ternary(hadHub,
			fmt.Sprintf("convctl versions %s --check-unserve %s", st.targetFlag(), oldHub),
			"convctl versions "+st.targetFlag()),
		Status:    StepUnknown,
		BlockedBy: clusterOnly,
	})

	// 7-8. Retiring the old version, only once nothing depends on it.
	for _, old := range retiringVersions(st, to) {
		retirementIdx = append(retirementIdx, n, n+1)
		oldV := versionByName(st.versions, old)
		unserved := oldV != nil && !oldV.served
		add(PlanStep{
			Title: "Stop serving " + old,
			Run:   fmt.Sprintf("edit %s: set spec.versions[name=%s].served: false (mark it deprecated first, with a deprecationWarning)", st.name, old),
			// Not "no objects remain readable at it": every served version
			// reads back every object, because the apiserver converts on
			// read. The blockers the verify command actually applies are
			// storage, writers, and an incomplete walk.
			Gate:      old + " is not in status.storedVersions, no field manager is still writing it, and the object walk completed",
			Verify:    fmt.Sprintf("convctl versions %s --check-unserve %s", st.targetFlag(), old),
			Status:    doneIf(unserved),
			BlockedBy: blockedBecause(!unserved, old+" is still served"),
		})
		dropped := oldV == nil
		add(PlanStep{
			Title:     fmt.Sprintf("Drop the %s version block", old),
			Run:       fmt.Sprintf("edit %s: remove the %s entry from spec.versions, and its spoke from the config", st.name, old),
			Gate:      old + " has been unserved long enough that no client depends on it; this step is irreversible for objects still stored at it",
			Verify:    "convctl compat --base <before> --head <after> --config <config.yaml> " + st.targetFlag(),
			Status:    doneIf(dropped),
			BlockedBy: blockedBecause(!dropped, fmt.Sprintf("the %s version block is still present", old)),
		})
	}

	// The first step that is genuinely not done is the one to do now, and
	// everything after it is blocked on that rather than independently
	// ready: presenting five "ready" steps at once is how they get done out
	// of order.
	//
	// Steps whose state cannot be read from a manifest do not become the
	// "next" step and do not block what follows. Doing either would be a
	// claim the tool cannot support — and blocking would hide the later
	// steps that are decidable, which on a late-stage target is the whole
	// remaining plan.
	// Retiring a version destroys the ability to read objects at it, and
	// whether that is safe rests entirely on the steps this command cannot
	// check: has storage been migrated, has storedVersions been pruned.
	// Offering "stop serving v1" as the next thing to do, on the strength
	// of a manifest, would be the one recommendation here that cannot be
	// walked back. So it is withheld until the operator says they ran the
	// verify commands.
	if !assumeVerified {
		var unknown []string
		for i := range steps {
			if steps[i].Status == StepUnknown {
				unknown = append(unknown, strconv.Itoa(steps[i].Number))
			}
		}
		if len(unknown) > 0 {
			for _, i := range retirementIdx {
				if i < len(steps) && steps[i].Status == StepReady {
					steps[i].Status = StepBlocked
					steps[i].BlockedBy = fmt.Sprintf("step(s) %s need a cluster to confirm and retiring a version is irreversible; run their verify commands, then re-run with --assume-verified", strings.Join(unknown, ", "))
				}
			}
		}
	}

	firstPending := -1
	for i := range steps {
		if steps[i].Status == StepReady {
			firstPending = i
			break
		}
	}
	for i := range steps {
		if firstPending >= 0 && i > firstPending && steps[i].Status == StepReady {
			steps[i].Status = StepBlocked
			// The reason a later step is blocked is the earlier step, not
			// the restatement of its own unfinished condition: "v1 is still
			// served" is what makes step 7 outstanding, while what makes it
			// unsafe to do right now is that step 3 has not happened.
			steps[i].BlockedBy = fmt.Sprintf("step %d (%s) has not been done yet", steps[firstPending].Number, steps[firstPending].Title)
		}
	}
	return steps
}

// clusterOnly marks a step whose completion cannot be read out of a
// manifest. Saying so is better than presenting it as merely "next": the
// operator needs to know the tool is not claiming to have checked.
const clusterOnly = "cannot be determined from files alone; confirm against the cluster with the verify command"

// retiringVersions lists the versions this migration is retiring.
//
// A version is retiring when it is the hub being replaced, or it is already
// marked deprecated, or it has already stopped being served — in the last
// case the only step left is dropping the block, and a plan that omitted it
// would report "nothing outstanding" for a target that still has work.
//
// A served, undeprecated spoke is deliberately not retired: keeping old
// versions readable is the entire point of a conversion webhook, and
// retiring one is a separate decision the operator makes by deprecating it.
func retiringVersions(st planState, to string) []string {
	var out []string
	seen := map[string]bool{}
	appendOnce := func(v string) {
		if v == "" || v == to || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	if st.hub != to {
		appendOnce(st.hub)
	}
	for _, v := range st.versions {
		if v.deprecated || !v.served {
			appendOnce(v.name)
		}
	}
	return out
}

// hubField names the field that marks the hub on this kind of target: an
// XRD's referenceable version is the one Crossplane makes the storage
// version of the generated CRD.
func (st planState) hubField() string {
	if st.native {
		return "storage"
	}
	return "referenceable"
}

func (st planState) resourceType() string {
	if st.native {
		return "customresourcedefinition"
	}
	return "compositeresourcedefinition"
}

func (st planState) targetFlag() string {
	if st.native {
		return "--crd <crd.yaml>"
	}
	return "--xrd <xrd.yaml>"
}

func (st planState) targetFlagName() string {
	if st.native {
		return "--crd"
	}
	return "--xrd"
}

// promotionBlocker names which half of the promotion is outstanding.
func promotionBlocker(st planState, to, oldHub string, hadHub bool) string {
	switch {
	case st.hub == to && st.hasConfig && st.configHub != to:
		return fmt.Sprintf("%s is the %s version but the config still has spec.hubVersion: %s", to, st.hubField(), st.configHub)
	case hadHub:
		return "the hub is still " + oldHub
	default:
		return fmt.Sprintf("no version is marked %s: true", st.hubField())
	}
}

func versionByName(vs []planVersion, name string) *planVersion {
	for i := range vs {
		if vs[i].name == name {
			return &vs[i]
		}
	}
	return nil
}

func doneIf(b bool) string {
	if b {
		return StepDone
	}
	return StepReady
}

func blockedBecause(cond bool, why string) string {
	if cond {
		return why
	}
	return ""
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// NextStep returns the step to do now, or nil when nothing is outstanding
// that the tool can determine.
//
// A step whose state cannot be read from a manifest is never offered as
// "next": the operator would have no way to tell whether the tool checked it
// or merely could not.
func (r *PlanReport) NextStep() *PlanStep {
	for i := range r.Steps {
		if r.Steps[i].Status == StepReady {
			return &r.Steps[i]
		}
	}
	return nil
}

// hasWithheldRetirement reports whether a retirement step is blocked only on
// the cluster-only gates, which is the case the --assume-verified flag is
// for.
func (r *PlanReport) hasWithheldRetirement() bool {
	for _, s := range r.Steps {
		if s.Status == StepBlocked && strings.Contains(s.BlockedBy, "--assume-verified") {
			return true
		}
	}
	return false
}

// WriteTable renders the plan.
func (r *PlanReport) WriteTable(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%s: %s\n", r.Kind, r.Resource)
	if r.Config != "" {
		_, _ = fmt.Fprintf(w, "Config: %s\n", r.Config)
	}
	_, _ = fmt.Fprintf(w, "Hub: %s → %s\n", orNone(r.From), r.To)
	if r.PackageManaged {
		_, _ = fmt.Fprintln(w, "PACKAGE-MANAGED: the conversion config must be applied BEFORE the package upgrade lands,")
		_, _ = fmt.Fprintln(w, "                 or the new version is served with no conversion at all.")
	}
	_, _ = fmt.Fprintln(w)

	next := r.NextStep()
	for i := range r.Steps {
		s := &r.Steps[i]
		marker := "  "
		if next != nil && s.Number == next.Number {
			marker = "▶ "
		}
		_, _ = fmt.Fprintf(w, "%s%d. [%s] %s\n", marker, s.Number, strings.ToUpper(s.Status), s.Title)
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		if s.Run != "" {
			_, _ = fmt.Fprintf(tw, "     run:\t%s\n", s.Run)
		}
		_, _ = fmt.Fprintf(tw, "     gate:\t%s\n", s.Gate)
		if s.Verify != "" {
			_, _ = fmt.Fprintf(tw, "     verify:\t%s\n", s.Verify)
		}
		if s.BlockedBy != "" {
			_, _ = fmt.Fprintf(tw, "     blocked:\t%s\n", s.BlockedBy)
		}
		_ = tw.Flush()
		_, _ = fmt.Fprintln(w)
	}

	if next == nil {
		unknown := 0
		for _, s := range r.Steps {
			if s.Status == StepUnknown {
				unknown++
			}
		}
		if unknown > 0 {
			_, _ = fmt.Fprintf(w, "Nothing outstanding that can be determined from files. %d step(s) need a cluster to confirm — run their verify commands", unknown)
			if r.hasWithheldRetirement() {
				_, _ = fmt.Fprint(w, ", then re-run with --assume-verified to plan the retirement steps")
			}
			_, _ = fmt.Fprintln(w, ".")
			return
		}
		_, _ = fmt.Fprintf(w, "Nothing to do: %s is already the hub and the path is complete.\n", r.To)
		return
	}
	_, _ = fmt.Fprintf(w, "NEXT: step %d — %s\n", next.Number, next.Title)
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
