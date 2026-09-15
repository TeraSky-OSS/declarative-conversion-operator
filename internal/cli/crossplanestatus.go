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
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

var compositionGVR = schema.GroupVersionResource{
	Group: "apiextensions.crossplane.io", Version: "v1", Resource: "compositions",
}

// VersionStatus is one XRD version's place in a migration.
type VersionStatus struct {
	Version       string `json:"version"`
	Served        bool   `json:"served"`
	Referenceable bool   `json:"referenceable"`
	Deprecated    bool   `json:"deprecated"`
	// DeprecationWarning is the XRD's own message, if it set one.
	DeprecationWarning string `json:"deprecationWarning,omitempty"`
	// HasSpokeRules reports whether the applied XRDConversionConfig
	// declares a spoke rule set for this version — i.e. whether anything
	// can convert it. A served version with no rules is the shape that
	// breaks reads.
	HasSpokeRules bool `json:"hasSpokeRules"`
}

// Deliberately no per-version object count. Listing a generated type
// returns every object converted to whichever version was asked for, so a
// count bucketed by the returned apiVersion would put the entire population
// on the hub and zero everywhere else — which reads as a finished
// migration regardless of what is actually in etcd. What objects are STORED
// at is status.storedVersions, which the generated-CRD table prints. See
// convctl versions for the per-object breakdown that needs managedFields.

// CompositionStatus is one Composition and the version it targets.
type CompositionStatus struct {
	Name string `json:"name"`
	// CompositeTypeRefVersion is the version in spec.compositeTypeRef —
	// immutable, which is why a hub promotion needs a NEW Composition.
	CompositeTypeRefVersion string `json:"compositeTypeRefVersion"`
	// XRs counts live objects pinned to this Composition by name.
	XRs int `json:"xrs"`
}

// GeneratedCRDStatusView is one generated CRD's conversion and storage
// state.
type GeneratedCRDStatusView struct {
	CRD            string   `json:"crd"`
	Role           string   `json:"role"`
	Namespaced     bool     `json:"namespaced"`
	Exists         bool     `json:"exists"`
	Strategy       string   `json:"strategy"`
	StorageVersion string   `json:"storageVersion,omitempty"`
	StoredVersions []string `json:"storedVersions,omitempty"`
	Objects        int      `json:"objects"`
}

// ConfigStatusView is the XRDConversionConfig's own health, if one exists.
type ConfigStatusView struct {
	Name       string            `json:"name"`
	Phase      string            `json:"phase"`
	HubVersion string            `json:"hubVersion"`
	Conditions map[string]string `json:"conditions,omitempty"`
	Message    string            `json:"message,omitempty"`
}

// CrossplaneStatusReport is the "where is my migration right now" view.
type CrossplaneStatusReport struct {
	XRD             string                   `json:"xrd"`
	Group           string                   `json:"group"`
	Kind            string                   `json:"kind"`
	Scope           string                   `json:"scope"`
	ScopeConfidence string                   `json:"scopeConfidence"`
	Versions        []VersionStatus          `json:"versions"`
	GeneratedCRDs   []GeneratedCRDStatusView `json:"generatedCRDs"`
	Compositions    []CompositionStatus      `json:"compositions"`
	// Unpinned counts live objects with no compositionRef, i.e. ones
	// Crossplane selects for by label.
	Unpinned int               `json:"unpinnedObjects"`
	Config   *ConfigStatusView `json:"config,omitempty"`
	// ConfigLookupFailed distinguishes "there is no config" from "we could
	// not tell" — the difference between a clean answer and a permissions
	// problem, which must not read the same.
	ConfigLookupFailed bool     `json:"configLookupFailed,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
}

// RunCrossplaneStatus assembles, in one read-only pass, the state an
// operator otherwise has to piece together by hand from the XRD, the
// Compositions, every XR's compositionRef, both generated CRDs'
// storedVersions, and the XRDConversionConfig's conditions.
//
// It constructs no write client and issues no write of any kind.
func RunCrossplaneStatus(ctx context.Context, dyn dynamic.Interface, xrdName string) (*CrossplaneStatusReport, error) {
	xrd, err := FetchLiveXRD(ctx, dyn, xrdName)
	if err != nil {
		return nil, err
	}
	scope := xrdadapter.ResolveScope(xrd)
	rep := &CrossplaneStatusReport{
		XRD: xrdName, Scope: string(scope.Scope), ScopeConfidence: string(scope.Confidence),
	}
	if scope.Confidence != xrdadapter.ConfidenceHigh {
		rep.Warnings = append(rep.Warnings, "scope: "+scope.Reason)
	}
	rep.Group, _, _ = unstructured.NestedString(xrd.Object, "spec", "group")
	rep.Kind, _, _ = unstructured.NestedString(xrd.Object, "spec", "names", "kind")

	var cfg *teraskyv1alpha1.XRDConversionConfig
	cfgName, err := conversionConfigNameForXRD(ctx, dyn, xrdName)
	switch {
	case err != nil:
		// "none" is a definite statement, and an RBAC denial is not
		// grounds for making it. Every other read failure here degrades to
		// a warning; this one did not, which made a permissions problem
		// look like a clean answer.
		rep.Warnings = append(rep.Warnings, "could not list XRDConversionConfigs, so the config column below may be wrong: "+err.Error())
		rep.ConfigLookupFailed = true
	case cfgName != "":
		if cfg, err = FetchLiveXRDConversionConfig(ctx, dyn, cfgName); err != nil {
			rep.Warnings = append(rep.Warnings, "could not read the XRDConversionConfig: "+err.Error())
			rep.ConfigLookupFailed = true
		}
	}
	spokeVersions := map[string]bool{}
	if cfg != nil {
		view := &ConfigStatusView{Name: cfg.Name, Phase: cfg.Status.Phase, HubVersion: cfg.Spec.HubVersion, Conditions: map[string]string{}}
		for _, c := range cfg.Status.Conditions {
			view.Conditions[c.Type] = string(c.Status)
		}
		view.Message = cfg.Status.Message
		rep.Config = view
		spokeVersions[cfg.Spec.HubVersion] = true
		for _, s := range cfg.Spec.Spokes {
			spokeVersions[s.Version] = true
		}
	}

	if err := collectVersions(xrd, spokeVersions, rep); err != nil {
		return nil, err
	}

	generated, err := xrdadapter.GeneratedCRDNames(xrd)
	if err != nil {
		return nil, err
	}
	refVersion, err := xrdReferenceableVersion(xrd)
	if err != nil {
		return nil, err
	}

	pinned := map[string]int{}
	for _, g := range generated {
		view := GeneratedCRDStatusView{CRD: g.Name, Role: string(g.Role), Namespaced: g.Namespaced, Strategy: "None"}
		crd, err := FetchLiveCRD(ctx, dyn, g.Name)
		switch {
		case apierrors.IsNotFound(err):
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("generated CRD %q does not exist yet", g.Name))
		case err != nil:
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("reading generated CRD %q: %v", g.Name, err))
		default:
			view.Exists = true
			if crd.Spec.Conversion != nil && crd.Spec.Conversion.Strategy != "" {
				view.Strategy = string(crd.Spec.Conversion.Strategy)
			}
			view.StoredVersions = append([]string(nil), crd.Status.StoredVersions...)
			if sv, err := crdStorageVersion(crd); err == nil {
				view.StorageVersion = sv
			}
		}

		items, err := listAllByGVR(ctx, dyn, schema.GroupVersionResource{Group: g.Group, Version: refVersion, Resource: g.Plural}, "")
		if err != nil {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("listing %s: %v", g.Name, err))
		}
		view.Objects = len(items)
		prefix := machineryPrefix(scope.Scope, g.Role)
		for i := range items {
			name, found, _ := unstructured.NestedString(items[i].Object, append(append([]string(nil), prefix...), "compositionRef", "name")...)
			if found && name != "" {
				pinned[name]++
			} else {
				rep.Unpinned++
			}
		}
		rep.GeneratedCRDs = append(rep.GeneratedCRDs, view)
	}

	comps, err := listAllByGVR(ctx, dyn, compositionGVR, "")
	if err != nil {
		rep.Warnings = append(rep.Warnings, "listing Compositions: "+err.Error())
	}
	for i := range comps {
		kind, _, _ := unstructured.NestedString(comps[i].Object, "spec", "compositeTypeRef", "kind")
		apiVersion, _, _ := unstructured.NestedString(comps[i].Object, "spec", "compositeTypeRef", "apiVersion")
		if kind != rep.Kind || !strings.HasPrefix(apiVersion, rep.Group+"/") {
			continue
		}
		rep.Compositions = append(rep.Compositions, CompositionStatus{
			Name:                    comps[i].GetName(),
			CompositeTypeRefVersion: versionFromAPIVersion(apiVersion),
			XRs:                     pinned[comps[i].GetName()],
		})
	}
	sort.Slice(rep.Compositions, func(i, j int) bool { return rep.Compositions[i].Name < rep.Compositions[j].Name })
	return rep, nil
}

func collectVersions(xrd *unstructured.Unstructured, spokeVersions map[string]bool, rep *CrossplaneStatusReport) error {
	raw, found, err := unstructured.NestedSlice(xrd.Object, "spec", "versions")
	if err != nil || !found {
		return fmt.Errorf("XRD %q has no spec.versions", xrd.GetName())
	}
	for i, item := range raw {
		vm, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("spec.versions[%d] is not an object", i)
		}
		v := VersionStatus{}
		v.Version, _, _ = unstructured.NestedString(vm, "name")
		served, foundServed, _ := unstructured.NestedBool(vm, "served")
		v.Served = served || !foundServed
		v.Referenceable, _, _ = unstructured.NestedBool(vm, "referenceable")
		v.Deprecated, _, _ = unstructured.NestedBool(vm, "deprecated")
		v.DeprecationWarning, _, _ = unstructured.NestedString(vm, "deprecationWarning")
		v.HasSpokeRules = spokeVersions[v.Version]
		rep.Versions = append(rep.Versions, v)
	}
	return nil
}

// conversionConfigNameForXRD finds the XRDConversionConfig targeting this
// XRD. There is at most one — the admission webhook enforces it — but the
// config's name is the user's choice, so it has to be looked up rather
// than derived.
func conversionConfigNameForXRD(ctx context.Context, dyn dynamic.Interface, xrdName string) (string, error) {
	list, err := dyn.Resource(xrdConversionConfigGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	for i := range list.Items {
		target, _, _ := unstructured.NestedString(list.Items[i].Object, "spec", "targetXRD", "name")
		if target == xrdName {
			return list.Items[i].GetName(), nil
		}
	}
	return "", nil
}

// WriteTable renders the whole migration state on one screen.
func (r *CrossplaneStatusReport) WriteTable(w io.Writer) {
	_, _ = fmt.Fprintf(w, "XRD: %s (%s.%s, scope %s", r.XRD, r.Kind, r.Group, r.Scope)
	if r.ScopeConfidence != string(xrdadapter.ConfidenceHigh) {
		_, _ = fmt.Fprintf(w, ", confidence %s", r.ScopeConfidence)
	}
	_, _ = fmt.Fprintln(w, ")")

	if r.Config != nil {
		_, _ = fmt.Fprintf(w, "Config: %s  phase=%s  hub=%s\n", r.Config.Name, r.Config.Phase, r.Config.HubVersion)
		keys := make([]string, 0, len(r.Config.Conditions))
		for k := range r.Config.Conditions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+r.Config.Conditions[k])
		}
		if len(parts) > 0 {
			_, _ = fmt.Fprintf(w, "  %s\n", strings.Join(parts, "  "))
		}
	} else if r.ConfigLookupFailed {
		_, _ = fmt.Fprintln(w, "Config: UNKNOWN — the XRDConversionConfig could not be read (see warnings below)")
	} else {
		_, _ = fmt.Fprintln(w, "Config: none — no XRDConversionConfig targets this XRD")
	}

	_, _ = fmt.Fprintln(w, "\nVERSIONS")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "VERSION\tSERVED\tREFERENCEABLE\tDEPRECATED\tRULES")
	for _, v := range r.Versions {
		rules := "-"
		if v.HasSpokeRules {
			rules = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%v\t%v\t%v\t%s\n", v.Version, v.Served, v.Referenceable, v.Deprecated, rules)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w, "  (object counts are per generated CRD below — a list returns every object converted to the version asked for, so it cannot say which version they are STORED at; that is storedVersions)")
	for _, v := range r.Versions {
		if v.Served && !v.HasSpokeRules {
			_, _ = fmt.Fprintf(w, "  WARNING: %s is served but no rule set covers it — reads at that version are not converted\n", v.Version)
		}
		if v.DeprecationWarning != "" {
			_, _ = fmt.Fprintf(w, "  %s: %s\n", v.Version, v.DeprecationWarning)
		}
	}

	_, _ = fmt.Fprintln(w, "\nGENERATED CRDs")
	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CRD\tROLE\tSCOPE\tCONVERSION\tSTORAGE\tSTORED VERSIONS\tOBJECTS")
	for _, c := range r.GeneratedCRDs {
		scope := "cluster"
		if c.Namespaced {
			scope = "namespaced"
		}
		strategy := c.Strategy
		if !c.Exists {
			strategy = "(missing)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			c.CRD, c.Role, scope, strategy, c.StorageVersion, strings.Join(c.StoredVersions, ","), c.Objects)
	}
	_ = tw.Flush()

	_, _ = fmt.Fprintln(w, "\nCOMPOSITIONS")
	if len(r.Compositions) == 0 {
		_, _ = fmt.Fprintln(w, "  (none target this XRD)")
	} else {
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "COMPOSITION\tcompositeTypeRef VERSION\tPINNED XRs")
		for _, c := range r.Compositions {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\n", c.Name, c.CompositeTypeRefVersion, c.XRs)
		}
		_ = tw.Flush()
	}
	_, _ = fmt.Fprintf(w, "  %d object(s) are not pinned to a Composition (selected by label)\n", r.Unpinned)

	if len(r.Warnings) > 0 {
		_, _ = fmt.Fprintln(w)
		for _, wmsg := range r.Warnings {
			_, _ = fmt.Fprintf(w, "WARNING: %s\n", wmsg)
		}
	}
}

func newCrossplaneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crossplane",
		Short: "Crossplane-specific read-only views",
	}
	cmd.AddCommand(newCrossplaneStatusCmd())
	return cmd
}

func newCrossplaneStatusCmd() *cobra.Command {
	var (
		kubeconfig, kubeContext, output string
	)
	cmd := &cobra.Command{
		Use:   "status <xrd-name>",
		Short: "Show where an XRD's version migration actually is",
		Long: `One screen answering "where is my migration right now?".

The information exists today, but it is spread across the XRD, the
Compositions, every XR's compositionRef, both generated CRDs' storedVersions,
and the XRDConversionConfig's conditions — so assembling it means half a dozen
kubectl invocations and some arithmetic.

Per version: served, referenceable, deprecated, whether a spoke rule set covers
it, and how many live objects read back at it. Plus each generated CRD's
conversion strategy and storedVersions (a LegacyCluster XRD with claimNames has
two), which Composition each XR is pinned to, which version each Composition
targets, and the conversion config's phase and conditions.

Strictly read-only: this constructs no write client and issues no write.

The invoking identity needs get/list on the XRD, the generated CRDs, the XR and
claim types, Compositions, and XRDConversionConfigs.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch output {
			case "table", "json":
			default:
				return fmt.Errorf("invalid --output value %q (want table or json)", output)
			}
			dyn, err := buildDynamicClient(KubeOptions{Kubeconfig: kubeconfig, Context: kubeContext})
			if err != nil {
				return err
			}
			rep, err := RunCrossplaneStatus(cmd.Context(), dyn, args[0])
			if err != nil {
				return err
			}
			if output == "json" {
				return writeJSON(cmd, rep)
			}
			rep.WriteTable(cmd.OutOrStdout())
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	cmd.Flags().StringVar(&kubeContext, "context", "", "Kubeconfig context to use (default: the kubeconfig's current-context)")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "Output format: table|json")
	registerKubeFlagCompletions(cmd)
	registerOutputCompletions(cmd, "table", "json")
	return cmd
}
