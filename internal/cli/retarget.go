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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

const defaultRetargetFieldManager = "convctl-retarget"

// RetargetOptions configures RunRetarget.
type RetargetOptions struct {
	// XRDName is a cluster resource name, not a file path.
	XRDName string
	// To is the hub version the Compositions should now be selected by.
	To string

	Kubeconfig  string
	KubeContext string

	// DryRun sends the same patch with DryRun: All, so the apiserver
	// exercises conversion and admission without persisting.
	DryRun bool
	// Concurrency defaults to 1: this is a write.
	Concurrency int
	// Canary limits the run to a prefix of the objects, as a count ("25")
	// or a percentage ("10%"). Selection is deterministic — the listing
	// order, which the apiserver returns sorted by name — so a second run
	// with the same value touches the same objects.
	//
	// On a claim-offering XRD that order is every composite, then every
	// claim, so a small canary lands entirely on composites. That is the
	// right shape for proving a change (composites are what Crossplane
	// re-selects) but it does mean a canary run is not a sample of both
	// classes; the report's per-object CRD column makes which is which
	// visible.
	Canary string
	// LabelKey is the Composition label the selector matches on. Defaults
	// to the same key `generate kyverno` uses.
	LabelKey string

	FieldManager string
	Quiet        bool
}

func (o RetargetOptions) fieldManager() string {
	if o.FieldManager == "" {
		return defaultRetargetFieldManager
	}
	return o.FieldManager
}

func (o RetargetOptions) labelKey() string {
	if o.LabelKey == "" {
		return defaultXRDAPIVersionLabel
	}
	return o.LabelKey
}

// patchOptions is the options every retarget request carries. --dry-run
// still sends the request: that is how the apiserver's conversion and
// admission path gets exercised — it just does not persist.
func (o RetargetOptions) patchOptions() metav1.PatchOptions {
	po := metav1.PatchOptions{FieldManager: o.fieldManager()}
	if o.DryRun {
		po.DryRun = []string{metav1.DryRunAll}
	}
	return po
}

func (o RetargetOptions) effectiveConcurrency(n int) int {
	c := o.Concurrency
	if c <= 0 {
		c = 1
	}
	if c > n {
		c = n
	}
	if c < 1 {
		c = 1
	}
	return c
}

// RetargetObjectResult is one XR (or claim) patched.
type RetargetObjectResult struct {
	CRD       string `json:"crd,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Error     string `json:"error,omitempty"`
	// Skipped is set when the object already selects the target version,
	// so a second run is visibly a no-op rather than silently reported as
	// another success.
	Skipped bool `json:"skipped,omitempty"`
}

// RetargetReport is the result of retargeting a whole XRD's objects.
type RetargetReport struct {
	XRD          string `json:"xrd"`
	To           string `json:"to"`
	LabelKey     string `json:"labelKey"`
	Scope        string `json:"scope"`
	DryRun       bool   `json:"dryRun"`
	FieldManager string `json:"fieldManager"`
	// Selected and Total report what --canary picked out of what existed.
	Selected int                    `json:"selected"`
	Total    int                    `json:"total"`
	Objects  []RetargetObjectResult `json:"objects"`
	Patched  int                    `json:"patched"`
	Skipped  int                    `json:"skipped"`
	Failed   int                    `json:"failed"`
	Warnings []string               `json:"warnings,omitempty"`
}

// HasFailures is the signal for a non-zero exit code.
func (r *RetargetReport) HasFailures() bool { return r.Failed > 0 }

// WriteTable renders a human-readable report.
func (r *RetargetReport) WriteTable(w io.Writer) {
	mode := "applied"
	if r.DryRun {
		mode = "dry-run"
	}
	_, _ = fmt.Fprintf(w, "Composition retarget (%s)\n", mode)
	_, _ = fmt.Fprintf(w, "XRD: %s (scope %s)\n", r.XRD, r.Scope)
	_, _ = fmt.Fprintf(w, "selector: %s=%s\n", r.LabelKey, r.To)
	if r.Selected != r.Total {
		_, _ = fmt.Fprintf(w, "canary: %d of %d objects selected (first by name)\n", r.Selected, r.Total)
	}
	_, _ = fmt.Fprintf(w, "objects: %d patched, %d already on target, %d failed\n", r.Patched, r.Skipped, r.Failed)

	if len(r.Objects) > 0 {
		_, _ = fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "CRD\tNAMESPACE\tNAME\tRESULT")
		for _, o := range r.Objects {
			result := "patched"
			switch {
			case o.Error != "":
				result = o.Error
			case o.Skipped:
				result = "already on target"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", o.CRD, o.Namespace, o.Name, result)
		}
		_ = tw.Flush()
	}

	if len(r.Warnings) > 0 {
		_, _ = fmt.Fprintln(w)
		for _, wmsg := range r.Warnings {
			_, _ = fmt.Fprintf(w, "WARNING: %s\n", wmsg)
		}
	}
}

// machineryPrefix is where Crossplane's composition machinery lives for a
// given scope. The modern scopes nest it under spec.crossplane;
// LegacyCluster — and every claim, whatever the scope — keeps the v1 layout
// with the fields directly under spec.
func machineryPrefix(scope xrdadapter.Scope, role xrdadapter.GeneratedCRDRole) []string {
	if role == xrdadapter.RoleClaim || scope == xrdadapter.ScopeLegacyCluster {
		return []string{"spec"}
	}
	return []string{"spec", "crossplane"}
}

// RunRetarget points every live object of an XRD's generated types at the
// Compositions labelled for a version, by clearing the pin
// (compositionRef / compositionRevisionRef) and setting
// compositionSelector.matchLabels. Crossplane then re-selects, and that
// write also persists the object at the new referenceable version.
//
// This is the Kyverno-free path: `generate kyverno` remains the answer for
// clusters that run Kyverno, but a cluster without it had no supported way
// to do the retarget step the documented evolution sequence requires.
//
// It carries the same caveat `generate kyverno` documents: once the pin is
// cleared, Crossplane picks at random among every Composition the selector
// matches, so a version-only selector is safe only with one Composition per
// hub version.
func RunRetarget(ctx context.Context, dyn dynamic.Interface, opts RetargetOptions) (*RetargetReport, error) {
	if opts.XRDName == "" {
		return nil, fmt.Errorf("--xrd is required")
	}
	if opts.To == "" {
		return nil, fmt.Errorf("--to is required")
	}

	xrd, err := FetchLiveXRD(ctx, dyn, opts.XRDName)
	if err != nil {
		return nil, err
	}
	scope := xrdadapter.ResolveScope(xrd)
	generated, err := xrdadapter.GeneratedCRDNames(xrd)
	if err != nil {
		return nil, err
	}
	if err := retargetVersionExists(xrd, opts.To); err != nil {
		return nil, err
	}

	rep := &RetargetReport{
		XRD: opts.XRDName, To: opts.To, LabelKey: opts.labelKey(),
		Scope: string(scope.Scope), DryRun: opts.DryRun, FieldManager: opts.fieldManager(),
	}
	if scope.Indeterminate() {
		// Which paths the machinery fields sit at depends on the scope, so
		// this is not a cosmetic uncertainty — refuse rather than patch
		// the wrong subtree.
		return nil, fmt.Errorf("cannot retarget: the XRD's scope could not be determined (%s) and it decides where Crossplane's composition machinery sits", scope.Reason)
	}
	if scope.Confidence != xrdadapter.ConfidenceHigh {
		rep.Warnings = append(rep.Warnings, "scope was inferred rather than read directly: "+scope.Reason)
	}

	type target struct {
		gvr        schema.GroupVersionResource
		crd        string
		namespaced bool
		prefix     []string
	}
	refVersion, err := xrdReferenceableVersion(xrd)
	if err != nil {
		return nil, err
	}

	var (
		items   []unstructured.Unstructured
		targets []target
		owners  []int
	)
	for _, g := range generated {
		t := target{
			gvr:        schema.GroupVersionResource{Group: g.Group, Version: refVersion, Resource: g.Plural},
			crd:        g.Name,
			namespaced: g.Namespaced,
			prefix:     machineryPrefix(scope.Scope, g.Role),
		}
		got, err := listAllByGVR(ctx, dyn, t.gvr, "")
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", t.gvr.String(), err)
		}
		targets = append(targets, t)
		for range got {
			owners = append(owners, len(targets)-1)
		}
		items = append(items, got...)
	}

	rep.Total = len(items)
	selected, err := applyCanary(len(items), opts.Canary)
	if err != nil {
		return nil, err
	}
	items = items[:selected]
	owners = owners[:selected]
	rep.Selected = selected
	if selected < rep.Total {
		note := fmt.Sprintf("--canary %s limited this run to the first %d of %d objects by name; re-run without --canary to finish", opts.Canary, selected, rep.Total)
		if len(generated) > 1 {
			note += " (objects are ordered composites first, then claims, so a small canary lands entirely on composites)"
		}
		rep.Warnings = append(rep.Warnings, note)
	}

	patchOpts := opts.patchOptions()

	results := make([]RetargetObjectResult, len(items))
	var (
		mu       sync.Mutex
		done     int
		next     = make(chan int)
		wg       sync.WaitGroup
		progress = !opts.Quiet && len(items) > 1
	)
	go func() {
		defer close(next)
		for i := range items {
			next <- i
		}
	}()
	for w := 0; w < opts.effectiveConcurrency(len(items)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				t := targets[owners[i]]
				res := retargetOne(ctx, dyn, t.gvr, t.crd, t.namespaced, t.prefix, &items[i], opts, patchOpts)
				mu.Lock()
				results[i] = res
				done++
				if progress {
					_, _ = fmt.Fprintf(os.Stderr, "\rretargeted %d/%d objects", done, len(items))
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if progress {
		_, _ = fmt.Fprintln(os.Stderr)
	}

	rep.Objects = results
	for _, r := range results {
		switch {
		case r.Error != "":
			rep.Failed++
		case r.Skipped:
			rep.Skipped++
		default:
			rep.Patched++
		}
	}
	return rep, nil
}

func retargetOne(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, crd string, namespaced bool, prefix []string, obj *unstructured.Unstructured, opts RetargetOptions, patchOpts metav1.PatchOptions) RetargetObjectResult {
	res := RetargetObjectResult{CRD: crd, Name: obj.GetName(), Namespace: obj.GetNamespace()}
	if namespaced && res.Namespace == "" {
		res.Error = "namespaced object is missing metadata.namespace"
		return res
	}

	if retargetIsNoOp(obj, prefix, opts.labelKey(), opts.To) {
		res.Skipped = true
		return res
	}

	patch, err := json.Marshal(retargetPatch(prefix, opts.labelKey(), opts.To))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if namespaced {
		_, err = dyn.Resource(gvr).Namespace(res.Namespace).Patch(ctx, res.Name, types.MergePatchType, patch, patchOpts)
	} else {
		_, err = dyn.Resource(gvr).Patch(ctx, res.Name, types.MergePatchType, patch, patchOpts)
	}
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

// retargetPatch builds a JSON merge patch.
//
// Server-Side Apply, which `migrate-storage` uses and which this command
// otherwise mirrors, deliberately is NOT used here: SSA can only remove a
// field the applying manager already owns, and compositionRef is owned by
// Crossplane (or by whoever pinned it). An apply that simply omits the
// field leaves it in place, which would leave every object still pinned to
// its old Composition — the exact thing this command exists to undo. A
// merge patch with an explicit null removes it regardless of ownership, in
// the same request that sets the new selector.
func retargetPatch(prefix []string, labelKey, to string) map[string]any {
	machinery := map[string]any{
		"compositionRef":         nil,
		"compositionRevisionRef": nil,
		"compositionSelector": map[string]any{
			"matchLabels": map[string]any{labelKey: to},
		},
	}
	// Build the nesting from the inside out so both layouts
	// (spec.crossplane.* and the bare spec.*) come from one code path.
	out := machinery
	for i := len(prefix) - 1; i >= 1; i-- {
		out = map[string]any{prefix[i]: out}
	}
	return map[string]any{prefix[0]: out}
}

// retargetIsNoOp reports whether the object already selects the target
// version and carries no pin, so a re-run is visibly a no-op rather than a
// pile of writes that change nothing.
func retargetIsNoOp(obj *unstructured.Unstructured, prefix []string, labelKey, to string) bool {
	path := append(append([]string(nil), prefix...), "compositionRef")
	if _, found, _ := unstructured.NestedMap(obj.Object, path...); found {
		return false
	}
	path = append(append([]string(nil), prefix...), "compositionRevisionRef")
	if _, found, _ := unstructured.NestedMap(obj.Object, path...); found {
		return false
	}
	path = append(append([]string(nil), prefix...), "compositionSelector", "matchLabels")
	labels, found, _ := unstructured.NestedStringMap(obj.Object, path...)
	if !found {
		return false
	}
	return labels[labelKey] == to
}

// applyCanary resolves "--canary 25" or "--canary 10%" into a count.
// Selection is a prefix of the listing order, which the apiserver returns
// sorted by name, so the same value picks the same objects on a re-run.
func applyCanary(total int, canary string) (int, error) {
	if canary == "" {
		return total, nil
	}
	if strings.HasSuffix(canary, "%") {
		pct, err := strconv.Atoi(strings.TrimSuffix(canary, "%"))
		if err != nil || pct < 0 || pct > 100 {
			return 0, fmt.Errorf("invalid --canary %q: want a count (e.g. 25) or a percentage 0-100 (e.g. 10%%)", canary)
		}
		n := total * pct / 100
		// A non-zero percentage of a non-empty population must select at
		// least one object; rounding to zero would make --canary 1% on a
		// 50-object cluster silently do nothing.
		if n == 0 && pct > 0 && total > 0 {
			n = 1
		}
		return n, nil
	}
	n, err := strconv.Atoi(canary)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid --canary %q: want a count (e.g. 25) or a percentage 0-100 (e.g. 10%%)", canary)
	}
	if n > total {
		n = total
	}
	return n, nil
}

func retargetVersionExists(xrd *unstructured.Unstructured, version string) error {
	versions, _, err := unstructured.NestedSlice(xrd.Object, "spec", "versions")
	if err != nil {
		return fmt.Errorf("reading spec.versions: %w", err)
	}
	var names []string
	for _, raw := range versions {
		vm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(vm, "name")
		if name == version {
			return nil
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return fmt.Errorf("--to %q is not a version of XRD %q (have: %s)", version, xrd.GetName(), strings.Join(names, ", "))
}

func newRetargetCmd() *cobra.Command {
	var (
		opts   RetargetOptions
		output string
	)
	cmd := &cobra.Command{
		Use:   "retarget",
		Short: "Point live XRs at the Compositions labelled for a hub version",
		Long: `Retarget every live object of an XRD onto the Compositions labelled for a
version, without Kyverno.

This is a live, mutating command. --xrd takes a cluster resource name, not a
local YAML file, and no conversion config is required.

Promoting an XRD's hub version means a NEW Composition: Crossplane will not let
a Composition's compositeTypeRef change. Existing XRs stay pinned to the old
one until something rewrites them. retarget does that rewrite: for each object
it clears spec.crossplane.compositionRef and compositionRevisionRef (or the bare
spec.* equivalents under scope: LegacyCluster, and on every claim) and sets
compositionSelector.matchLabels to the target version. Crossplane re-selects,
and that write also persists the object at the new referenceable version — so
this is usually what makes migrate-storage's empty-SSA pass a no-op.

A scope: LegacyCluster XRD's claims are retargeted too: they carry their own
compositionRef and compositionSelector, in the bare spec.* layout.

CAVEAT, the same one generate kyverno documents: once the pin is cleared,
Crossplane picks at RANDOM among every Composition the selector matches. A
version-only selector is safe only if there is exactly one Composition per hub
version. Label your Compositions accordingly, or pin with a richer selector
instead of this command.

Start with --dry-run (exercises conversion and admission, persists nothing) and
--canary to prove the change on a subset before the rest.

The invoking identity needs get on the XRD, get/list on the generated CRDs, and
list/patch on the XR (and claim) types.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch output {
			case "table", "json":
			default:
				return fmt.Errorf("invalid --output value %q (want table or json)", output)
			}
			dyn, err := buildDynamicClient(KubeOptions{Kubeconfig: opts.Kubeconfig, Context: opts.KubeContext})
			if err != nil {
				return err
			}
			rep, err := RunRetarget(cmd.Context(), dyn, opts)
			if err != nil {
				return err
			}
			if output == "json" {
				if err := writeJSON(cmd, rep); err != nil {
					return err
				}
			} else {
				rep.WriteTable(cmd.OutOrStdout())
			}
			if rep.HasFailures() {
				exitCode = ExitTestFailure
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&opts.XRDName, "xrd", "x", "", "Cluster name of the CompositeResourceDefinition (not a file path)")
	cmd.Flags().StringVar(&opts.To, "to", "", "Hub version to retarget onto (must be a version of the XRD)")
	cmd.Flags().StringVar(&opts.Kubeconfig, "kubeconfig", "", "Path to a kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	cmd.Flags().StringVar(&opts.KubeContext, "context", "", "Kubeconfig context to use (default: the kubeconfig's current-context)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Send the same patch with server-side dry-run (exercises conversion, does not persist)")
	cmd.Flags().IntVar(&opts.Concurrency, "concurrency", 1, "Number of objects to patch in parallel")
	cmd.Flags().StringVar(&opts.Canary, "canary", "", "Retarget only the first N objects, or N% of them, by name (e.g. 25 or 10%)")
	cmd.Flags().StringVar(&opts.LabelKey, "label-key", defaultXRDAPIVersionLabel, "Composition label key the selector matches on")
	cmd.Flags().StringVar(&opts.FieldManager, "field-manager", defaultRetargetFieldManager, "Field manager name recorded on the patch")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "Output format: table|json")
	cmd.Flags().BoolVar(&opts.Quiet, "quiet", false, "Suppress the progress line written to stderr")
	_ = cmd.MarkFlagRequired("xrd")
	_ = cmd.MarkFlagRequired("to")
	registerKubeFlagCompletions(cmd)
	registerLiveResourceCompletions(cmd)
	registerOutputCompletions(cmd, "table", "json")
	return cmd
}
