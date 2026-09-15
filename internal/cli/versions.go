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
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// VersionWriter is a field manager still writing a version, and when it last
// did.
//
// This is the column that actually decides whether un-serving is safe.
// "Nothing is stored at v1" says the data has moved; it says nothing about
// the controller that still PUTs v1 objects every reconcile and will start
// failing the moment the version stops being served. managedFields records
// the apiVersion each manager used, so the answer is already in the cluster
// — it has just never been surfaced.
type VersionWriter struct {
	Manager string `json:"manager"`
	// LastWrittenAt is RFC3339, from the managedFields entry's own
	// timestamp. Empty when the entry carries none.
	LastWrittenAt string `json:"lastWrittenAt,omitempty"`
}

// VersionRow is one version's inventory.
type VersionRow struct {
	Name       string `json:"name"`
	Served     bool   `json:"served"`
	Hub        bool   `json:"hub"`
	Deprecated bool   `json:"deprecated"`
	// DeprecationWarning is Crossplane's own per-version message, copied
	// into the generated CRD by xcrd.genCrdVersion and surfaced nowhere
	// else in this project until now.
	DeprecationWarning string `json:"deprecationWarning,omitempty"`
	SpokeRules         bool   `json:"spokeRules"`
	// LiveObjects counts instances across every generated CRD: a
	// claim-offering XRD has two, and a count that silently covered one of
	// them would answer the safety question wrongly.
	//
	// It is not evidence about THIS version. The apiserver converts on
	// read, so a list at any served version returns every object, and the
	// count is therefore the same on every served row. What is
	// version-specific is Stored and Writers.
	LiveObjects int             `json:"liveObjects"`
	Stored      bool            `json:"stored"`
	Writers     []VersionWriter `json:"writers,omitempty"`
	// Truncated records that the object walk stopped at the sample bound,
	// so a zero or small count is not mistaken for "nothing is there".
	Truncated bool `json:"truncated,omitempty"`
	// Unreadable records why the objects at this version could not be
	// listed -- RBAC, a network failure, anything that is not "the
	// apiserver does not serve this version". The count that accompanies it
	// is a floor, not an answer, and this is the one command where
	// mistaking "I could not look" for "there are none" gets a version
	// unserved while clients are still reading it.
	Unreadable string `json:"unreadable,omitempty"`
}

// VersionsReport answers "is it safe to drop this version yet?".
type VersionsReport struct {
	Resource string       `json:"resource"`
	Kind     string       `json:"kind"`
	Config   string       `json:"config,omitempty"`
	Rows     []VersionRow `json:"versions"`
}

// VersionsOptions configures the inventory.
type VersionsOptions struct {
	XRDPath     string
	CRDPath     string
	ConfigPath  string
	Kubeconfig  string
	KubeContext string
	// MaxSamples bounds the object walk on a large cluster. Zero means the
	// default bound; the walk paginates either way.
	MaxSamples int
	// CheckUnserve, when set, makes the command a gate on stopping to serve
	// that version: exit non-zero if it is still the hub, still appears in
	// storedVersions, is still being written by some field manager, or
	// could not be inspected completely. Live object counts are inventory
	// and deliberately not a blocker -- see UnserveBlockers.
	CheckUnserve string
}

const defaultVersionsMaxSamples = 5000

// RunVersions builds the inventory. Read-only throughout.
func RunVersions(ctx context.Context, opts VersionsOptions) (*VersionsReport, error) {
	dyn, err := buildDynamicClient(KubeOptions{Kubeconfig: opts.Kubeconfig, Context: opts.KubeContext})
	if err != nil {
		return nil, err
	}
	if opts.XRDPath != "" {
		return versionsForXRD(ctx, dyn, opts)
	}
	return nil, errors.New("--xrd is required (native CRD targets are not yet supported by this command)")
}

func versionsForXRD(ctx context.Context, dyn dynamic.Interface, opts VersionsOptions) (*VersionsReport, error) {
	xrd, err := LoadXRD(opts.XRDPath)
	if err != nil {
		return nil, err
	}
	source := xrdadapter.New(xrd)
	versions, err := source.Versions()
	if err != nil {
		return nil, err
	}
	generated, err := xrdadapter.GeneratedCRDNames(xrd)
	if err != nil {
		return nil, err
	}

	spokeRules := map[string]bool{}
	configName := ""
	if opts.ConfigPath != "" {
		cfg, cerr := LoadConfig(opts.ConfigPath)
		if cerr != nil {
			return nil, cerr
		}
		configName = cfg.Name
		for _, s := range cfg.Spec.Spokes {
			spokeRules[s.Version] = true
		}
		spokeRules[cfg.Spec.HubVersion] = true
	}

	deprecated, warnings := deprecationFromXRD(xrd)
	stored, err := storedVersions(ctx, dyn, generated)
	if err != nil {
		return nil, err
	}

	maxSamples := opts.MaxSamples
	if maxSamples <= 0 {
		maxSamples = defaultVersionsMaxSamples
	}

	rep := &VersionsReport{Resource: xrdName(xrd), Kind: "XRD", Config: configName}
	for _, v := range versions {
		row := VersionRow{
			Name:               v.Name,
			Served:             v.Served,
			Hub:                v.Storage,
			Deprecated:         deprecated[v.Name],
			DeprecationWarning: warnings[v.Name],
			SpokeRules:         spokeRules[v.Name],
			Stored:             stored[v.Name],
		}
		count, writers, truncated, werr := walkObjects(ctx, dyn, generated, v.Name, maxSamples)
		if werr != nil {
			// Recorded per version rather than aborting: one unreadable
			// version should not deny the answer for the others, and the
			// row says plainly that its count is a floor.
			row.Unreadable = werr.Error()
		}
		row.LiveObjects = count
		row.Writers = writers
		row.Truncated = truncated
		rep.Rows = append(rep.Rows, row)
	}
	return rep, nil
}

// deprecationFromXRD reads Crossplane's own per-version deprecation fields.
func deprecationFromXRD(xrd *unstructured.Unstructured) (map[string]bool, map[string]string) {
	dep := map[string]bool{}
	warn := map[string]string{}
	raw, found, err := unstructured.NestedSlice(xrd.Object, "spec", "versions")
	if err != nil || !found {
		return dep, warn
	}
	for _, r := range raw {
		vm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(vm, "name")
		if name == "" {
			continue
		}
		if d, ok, _ := unstructured.NestedBool(vm, "deprecated"); ok {
			dep[name] = d
		}
		if w, ok, _ := unstructured.NestedString(vm, "deprecationWarning"); ok {
			warn[name] = w
		}
	}
	return dep, warn
}

// storedVersions reads status.storedVersions from every generated CRD.
//
// A version present there is one the apiserver believes objects are still
// persisted at, and it is the hard blocker on un-serving: the apiserver
// refuses to drop a version still listed.
func storedVersions(ctx context.Context, dyn dynamic.Interface, generated []xrdadapter.GeneratedCRD) (map[string]bool, error) {
	out := map[string]bool{}
	gvr := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	for _, g := range generated {
		crd, err := dyn.Resource(gvr).Get(ctx, g.Name, metav1.GetOptions{})
		if err != nil {
			// A generated CRD that does not exist yet is not an error: the
			// XRD may simply not have been reconciled. It contributes no
			// stored versions.
			if apierrors.IsNotFound(err) {
				continue
			}
			// Anything else -- forbidden, a timeout -- is a failure to
			// read, and storedVersions is the single most load-bearing
			// input to the un-serve gate. An empty map here would read as
			// "nothing is stored anywhere", which is the answer that makes
			// unserving look safe.
			return nil, fmt.Errorf("reading storedVersions from %s: %w", g.Name, err)
		}
		vs, _, _ := unstructured.NestedStringSlice(crd.Object, "status", "storedVersions")
		for _, v := range vs {
			out[v] = true
		}
	}
	return out, nil
}

// walkObjects counts instances at one version and aggregates the field
// managers still writing it.
//
// Paginated and bounded: on a cluster with a large object population an
// unbounded List is a way to take the apiserver down while asking whether a
// version is safe to drop.
func walkObjects(ctx context.Context, dyn dynamic.Interface, generated []xrdadapter.GeneratedCRD, version string, maxSamples int) (int, []VersionWriter, bool, error) {
	latest := map[string]string{}
	total := 0
	truncated := false
	var unreadable error

	for _, g := range generated {
		gvr := schema.GroupVersionResource{Group: g.Group, Version: version, Resource: g.Plural}
		cont := ""
		for {
			list, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 500, Continue: cont})
			if err != nil {
				// A version the apiserver does not serve cannot be listed;
				// that is information, not a failure. Anything else --
				// forbidden, a timeout, a partial list -- is a failure to
				// look, and reporting it as a count of zero is how this
				// command would tell someone it is safe to unserve a
				// version that half the fleet is still reading.
				if !apierrors.IsNotFound(err) && unreadable == nil {
					unreadable = fmt.Errorf("listing %s at %s: %w", gvr.Resource, version, err)
				}
				break
			}
			for i := range list.Items {
				total++
				collectWriters(&list.Items[i], latest)
				if total >= maxSamples {
					truncated = true
					break
				}
			}
			cont = list.GetContinue()
			if cont == "" || truncated {
				break
			}
		}
		if truncated {
			break
		}
	}

	writers := make([]VersionWriter, 0, len(latest))
	for m, at := range latest {
		writers = append(writers, VersionWriter{Manager: m, LastWrittenAt: at})
	}
	sort.SliceStable(writers, func(i, j int) bool { return writers[i].Manager < writers[j].Manager })
	return total, writers, truncated, unreadable
}

// collectWriters records, per field manager, the most recent time it wrote
// this object at this version.
func collectWriters(obj *unstructured.Unstructured, latest map[string]string) {
	want := obj.GetAPIVersion()
	for _, mf := range obj.GetManagedFields() {
		if mf.APIVersion != want {
			continue
		}
		at := ""
		if mf.Time != nil {
			at = mf.Time.UTC().Format("2006-01-02T15:04:05Z")
		}
		if cur, ok := latest[mf.Manager]; !ok || at > cur {
			latest[mf.Manager] = at
		}
	}
}

// UnserveBlockers returns the reasons a version cannot safely stop being
// served, or nil when it can.
func (r *VersionsReport) UnserveBlockers(version string) []string {
	var row *VersionRow
	for i := range r.Rows {
		if r.Rows[i].Name == version {
			row = &r.Rows[i]
		}
	}
	if row == nil {
		return []string{fmt.Sprintf("version %q is not declared on %s", version, r.Resource)}
	}

	var blockers []string
	if row.Hub {
		blockers = append(blockers, "it is the hub/storage version; promote another version first")
	}
	if row.Stored {
		blockers = append(blockers, "it appears in status.storedVersions, so the apiserver believes objects are still persisted at it — run `convctl migrate-storage` first")
	}
	// LiveObjects is deliberately NOT a blocker. A list at a served version
	// returns every object converted to that version, not the objects
	// stored at it -- so a single XR anywhere would block un-serving every
	// spoke forever, which would make this gate useless rather than strict.
	// Storage is what Stored answers, and current writers are what Writers
	// answers; both are version-specific and both are already here.
	if len(row.Writers) > 0 {
		names := make([]string, 0, len(row.Writers))
		for _, w := range row.Writers {
			names = append(names, w.Manager)
		}
		blockers = append(blockers, "still actively written by: "+strings.Join(names, ", "))
	}
	if row.Unreadable != "" {
		blockers = append(blockers, "objects at this version could not be listed, so a count of zero does not mean there are none: "+row.Unreadable)
	}
	if row.Truncated {
		blockers = append(blockers, "the object walk hit its bound, so this answer is incomplete; raise --max-samples")
	}
	return blockers
}

// WriteTable renders the inventory.
func (r *VersionsReport) WriteTable(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%s: %s", r.Kind, r.Resource)
	if r.Config != "" {
		_, _ = fmt.Fprintf(w, "\tConfig: %s", r.Config)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "VERSION\tSERVED\tHUB\tDEPRECATED\tSPOKE RULES\tLIVE OBJECTS\tSTORED\tLAST WRITTEN AT")
	for _, row := range r.Rows {
		count := strconv.Itoa(row.LiveObjects)
		switch {
		case row.Unreadable != "":
			count += "?"
		case row.Truncated:
			count += "+"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Name, yesNo(row.Served), yesNo(row.Hub), yesNo(row.Deprecated),
			yesNo(row.SpokeRules), count, yesNo(row.Stored), writerSummary(row.Writers))
	}
	_ = tw.Flush()

	for _, row := range r.Rows {
		if row.Unreadable != "" {
			_, _ = fmt.Fprintf(w, "\n%s: objects could not be listed, so its count is a floor rather than an answer: %s\n", row.Name, row.Unreadable)
		}
	}
	for _, row := range r.Rows {
		if row.DeprecationWarning != "" {
			_, _ = fmt.Fprintf(w, "\n%s is deprecated: %s\n", row.Name, row.DeprecationWarning)
		}
	}
}

func writerSummary(ws []VersionWriter) string {
	if len(ws) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		if w.LastWrittenAt == "" {
			parts = append(parts, w.Manager)
			continue
		}
		parts = append(parts, w.Manager+" @ "+w.LastWrittenAt)
	}
	return strings.Join(parts, ", ")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
