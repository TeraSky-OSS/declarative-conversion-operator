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
	"os"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/spf13/cobra"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// defaultMigrateFieldManager is the SSA field manager empty patches are
// applied under. A dedicated name means a second run is a no-op once
// objects are already stored at the current version, and it never fights
// the operator's own `declarative-conversion-operator` manager.
const defaultMigrateFieldManager = "convctl"

// MigrateStorageOptions configures RunMigrateStorage.
type MigrateStorageOptions struct {
	// XRDName and CRDName are cluster resource names (not local file
	// paths), mutually exclusive. Exactly one must be set.
	XRDName string
	CRDName string

	Kubeconfig  string
	KubeContext string

	// Namespace, when set, limits the list to that namespace. Ignored for
	// cluster-scoped types. Empty means all namespaces.
	Namespace string

	// DryRun applies the same empty SSA patch with DryRun: All, so the
	// apiserver exercises conversion without persisting.
	DryRun bool

	// Concurrency is how many objects to patch at once. Zero or negative
	// means 1 — storage migration is a write, so the default is sequential.
	Concurrency int

	// FieldManager is the SSA field manager. Empty defaults to "convctl".
	FieldManager string

	// PruneStoredVersions, after every apply succeeds, sets the target
	// CRD's status.storedVersions to the current storage version only.
	// Skipped (with a warning) on dry-run or if any object failed.
	PruneStoredVersions bool

	Quiet bool
}

func (o MigrateStorageOptions) fieldManager() string {
	if o.FieldManager == "" {
		return defaultMigrateFieldManager
	}
	return o.FieldManager
}

func (o MigrateStorageOptions) effectiveConcurrency(n int) int {
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

// MigrateObjectResult is one empty-SSA apply against a live CR/XR.
type MigrateObjectResult struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Error     string `json:"error,omitempty"`
	// CRD names which generated CRD this object belongs to. Only set when
	// the run covered more than one (a claim-offering XRD), so existing
	// consumers of the single-CRD shape see no change.
	CRD string `json:"crd,omitempty"`
}

// MigrateCRDReport is the per-CRD half of a report. One XRD can generate
// two CRDs — the composite and, on a LegacyCluster XRD with
// spec.claimNames, the claim — and each has its own objects, its own
// status.storedVersions, and its own prune outcome.
type MigrateCRDReport struct {
	CRD            string                `json:"crd"`
	Role           string                `json:"role"` // "composite" | "claim"
	Kind           string                `json:"kind"`
	Plural         string                `json:"plural"`
	Namespaced     bool                  `json:"namespaced"`
	StorageVersion string                `json:"storageVersion"`
	StoredVersions []string              `json:"storedVersions,omitempty"`
	Objects        []MigrateObjectResult `json:"objects"`
	Succeeded      int                   `json:"succeeded"`
	Failed         int                   `json:"failed"`
	Pruned         bool                  `json:"pruned"`
	PruneError     string                `json:"pruneError,omitempty"`
}

// MigrateStorageReport is the result of rewriting every instance of a
// target XRD/CRD to the current storage version.
type MigrateStorageReport struct {
	ResourceKind   string                `json:"resourceKind"` // "XRD" or "CRD"
	Resource       string                `json:"resource"`
	CRD            string                `json:"crd"`
	Group          string                `json:"group"`
	Kind           string                `json:"kind"`
	Plural         string                `json:"plural"`
	StorageVersion string                `json:"storageVersion"`
	StoredVersions []string              `json:"storedVersions,omitempty"`
	Namespaced     bool                  `json:"namespaced"`
	DryRun         bool                  `json:"dryRun"`
	FieldManager   string                `json:"fieldManager"`
	Objects        []MigrateObjectResult `json:"objects"`
	Succeeded      int                   `json:"succeeded"`
	Failed         int                   `json:"failed"`
	Pruned         bool                  `json:"pruned"`
	PruneError     string                `json:"pruneError,omitempty"`
	Warnings       []string              `json:"warnings,omitempty"`

	// CRDs is the per-CRD breakdown, present only when the run covered
	// more than one — i.e. a LegacyCluster XRD with spec.claimNames, which
	// generates a claim CRD alongside the composite. The fields above
	// describe the composite (or, for a --crd run, the CRD itself) exactly
	// as they always have; Objects, Succeeded and Failed are the totals
	// across every CRD, and each object carries the CRD it came from.
	CRDs []MigrateCRDReport `json:"crds,omitempty"`
}

// HasFailures reports whether any object apply (or a requested prune)
// failed — the signal for exit code 1.
func (r *MigrateStorageReport) HasFailures() bool {
	if r.Failed > 0 || r.PruneError != "" {
		return true
	}
	for _, c := range r.CRDs {
		if c.Failed > 0 || c.PruneError != "" {
			return true
		}
	}
	return false
}

// WriteTable renders a human-readable terminal report.
func (r *MigrateStorageReport) WriteTable(w io.Writer) {
	mode := "applied"
	if r.DryRun {
		mode = "dry-run"
	}
	_, _ = fmt.Fprintf(w, "Storage version migration (%s)\n", mode)
	_, _ = fmt.Fprintf(w, "%s: %s\n", r.ResourceKind, r.Resource)
	_, _ = fmt.Fprintf(w, "CRD: %s\n", r.CRD)
	_, _ = fmt.Fprintf(w, "GVK: %s/%s %s (plural %s)\n", r.Group, r.StorageVersion, r.Kind, r.Plural)
	if len(r.StoredVersions) > 0 {
		_, _ = fmt.Fprintf(w, "stored versions: %s\n", strings.Join(r.StoredVersions, ", "))
	}
	_, _ = fmt.Fprintf(w, "objects: %d succeeded, %d failed\n", r.Succeeded, r.Failed)
	if len(r.CRDs) == 0 {
		switch {
		case r.Pruned:
			_, _ = fmt.Fprintln(w, "pruned stored versions: yes")
		case r.PruneError != "":
			_, _ = fmt.Fprintf(w, "pruned stored versions: error: %s\n", r.PruneError)
		}
	} else {
		// A claim-offering XRD generates two CRDs with independent
		// storedVersions, and "the version can now be dropped" is only
		// true if both were pruned — so report them separately rather
		// than collapsing to one line.
		_, _ = fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "CRD\tROLE\tSCOPE\tOBJECTS\tFAILED\tSTORED VERSIONS\tPRUNED")
		for _, c := range r.CRDs {
			scope := "cluster"
			if c.Namespaced {
				scope = "namespaced"
			}
			pruned := "no"
			switch {
			case c.Pruned:
				pruned = "yes"
			case c.PruneError != "":
				pruned = "error: " + c.PruneError
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
				c.CRD, c.Role, scope, c.Succeeded+c.Failed, c.Failed, strings.Join(c.StoredVersions, ","), pruned)
		}
		_ = tw.Flush()
	}

	if len(r.Objects) > 0 {
		_, _ = fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		if r.Namespaced {
			_, _ = fmt.Fprintln(tw, "NAMESPACE\tNAME\tRESULT")
			for _, o := range r.Objects {
				result := "ok"
				if o.Error != "" {
					result = o.Error
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", o.Namespace, o.Name, result)
			}
		} else {
			_, _ = fmt.Fprintln(tw, "NAME\tRESULT")
			for _, o := range r.Objects {
				result := "ok"
				if o.Error != "" {
					result = o.Error
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\n", o.Name, result)
			}
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

type migrateTarget struct {
	resourceKind   string
	resource       string
	crdName        string
	role           string
	group          string
	kind           string
	plural         string
	storageVersion string
	storedVersions []string
	namespaced     bool
}

// RunMigrateStorage lists every instance of the target type at its current
// storage version and server-side-applies an identity-only patch so the
// apiserver re-encodes each object in etcd. It does not use the Kubernetes
// 1.30+ StorageVersionMigration API.
//
// An XRD can generate two CRDs — the composite and, on a LegacyCluster XRD
// with spec.claimNames, the claim — and both are covered. Claims are their
// own stored object class with their own status.storedVersions, so
// migrating only the composite left the old version un-droppable, which is
// the entire reason anyone runs --prune-stored-versions.
func RunMigrateStorage(ctx context.Context, dyn dynamic.Interface, opts MigrateStorageOptions) (*MigrateStorageReport, error) {
	if (opts.XRDName == "") == (opts.CRDName == "") {
		return nil, fmt.Errorf("exactly one of --xrd or --crd is required")
	}

	targets, warnings, err := resolveMigrateTargets(ctx, dyn, opts)
	if err != nil {
		return nil, err
	}

	applyOpts := metav1.ApplyOptions{
		FieldManager: opts.fieldManager(),
		Force:        true,
	}
	if opts.DryRun {
		applyOpts.DryRun = []string{metav1.DryRunAll}
	}

	// Refuse the unsupported combination before a single object is
	// written. This used to live inside the per-target loop, where on a
	// claim-offering XRD the composite pass (cluster-scoped, so listNS is
	// empty) had already listed and applied every composite by the time
	// the claim pass returned the error — and RunMigrateStorage returns a
	// nil report with an error, so the caller lost the record of writes
	// that had already happened.
	if opts.PruneStoredVersions && opts.Namespace != "" {
		for _, t := range targets {
			if t.namespaced {
				return nil, fmt.Errorf("--prune-stored-versions cannot be combined with --namespace: objects in other namespaces may still be stored at an older version")
			}
		}
	}

	primary := targets[0]
	rep := &MigrateStorageReport{
		ResourceKind:   primary.resourceKind,
		Resource:       primary.resource,
		CRD:            primary.crdName,
		Group:          primary.group,
		Kind:           primary.kind,
		Plural:         primary.plural,
		StorageVersion: primary.storageVersion,
		StoredVersions: primary.storedVersions,
		Namespaced:     primary.namespaced,
		DryRun:         opts.DryRun,
		FieldManager:   opts.fieldManager(),
	}
	multi := len(targets) > 1

	for _, target := range targets {
		listNS := ""
		if target.namespaced {
			listNS = opts.Namespace
		} else if opts.Namespace != "" {
			warnings = append(warnings, fmt.Sprintf("--namespace %q ignored for %s: it is cluster-scoped", opts.Namespace, target.crdName))
		}
		gvr := schema.GroupVersionResource{Group: target.group, Version: target.storageVersion, Resource: target.plural}
		items, err := listAllByGVR(ctx, dyn, gvr, listNS)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", gvr.String(), err)
		}

		results := migrateAll(ctx, dyn, gvr, target, items, applyOpts, opts, multi)

		crdRep := MigrateCRDReport{
			CRD:            target.crdName,
			Role:           target.role,
			Kind:           target.kind,
			Plural:         target.plural,
			Namespaced:     target.namespaced,
			StorageVersion: target.storageVersion,
			StoredVersions: target.storedVersions,
			Objects:        results,
		}
		for _, r := range results {
			if r.Error != "" {
				crdRep.Failed++
			} else {
				crdRep.Succeeded++
			}
		}
		rep.CRDs = append(rep.CRDs, crdRep)
		rep.Objects = append(rep.Objects, results...)
		rep.Succeeded += crdRep.Succeeded
		rep.Failed += crdRep.Failed
	}

	if !multi {
		// A single-CRD run keeps exactly the shape it always had, so a
		// consumer that never sees a claim-offering XRD is untouched.
		rep.CRDs = nil
	}
	rep.Warnings = warnings

	if !opts.PruneStoredVersions {
		return rep, nil
	}
	switch {
	case opts.DryRun:
		rep.Warnings = append(rep.Warnings, "--prune-stored-versions is ignored with --dry-run")
		return rep, nil
	case rep.Failed > 0:
		// A half-pruned pair is worse than none: prune one CRD and the
		// other still lists the old version, so the version cannot be
		// dropped anyway — but the successful prune has already discarded
		// the record of which objects were still stored at it. So a
		// failure anywhere blocks the prune everywhere, and the message
		// says which CRD failed.
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("skipping --prune-stored-versions: %d object(s) failed%s", rep.Failed, failedCRDNote(rep)))
		return rep, nil
	}

	for i := range rep.CRDs {
		if err := pruneStoredVersions(ctx, dyn, rep.CRDs[i].CRD, rep.CRDs[i].StorageVersion); err != nil {
			rep.CRDs[i].PruneError = err.Error()
			continue
		}
		rep.CRDs[i].Pruned = true
		rep.CRDs[i].StoredVersions = []string{rep.CRDs[i].StorageVersion}
	}
	if !multi {
		if err := pruneStoredVersions(ctx, dyn, primary.crdName, primary.storageVersion); err != nil {
			rep.PruneError = err.Error()
		} else {
			rep.Pruned = true
			rep.StoredVersions = []string{primary.storageVersion}
		}
		return rep, nil
	}
	// Mirror the composite's outcome onto the top-level fields, which have
	// always described the composite.
	rep.Pruned = rep.CRDs[0].Pruned
	rep.PruneError = rep.CRDs[0].PruneError
	rep.StoredVersions = rep.CRDs[0].StoredVersions
	return rep, nil
}

// failedCRDNote names which CRDs contributed failures, so "skipping the
// prune" is actionable on a two-CRD run.
func failedCRDNote(rep *MigrateStorageReport) string {
	var names []string
	for _, c := range rep.CRDs {
		if c.Failed > 0 {
			names = append(names, fmt.Sprintf("%s (%s): %d", c.CRD, c.Role, c.Failed))
		}
	}
	if len(names) == 0 {
		return ""
	}
	return " — " + strings.Join(names, ", ")
}

// migrateAll runs the worker pool for one CRD's objects.
func migrateAll(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, target migrateTarget, items []unstructured.Unstructured, applyOpts metav1.ApplyOptions, opts MigrateStorageOptions, tagCRD bool) []MigrateObjectResult {
	results := make([]MigrateObjectResult, len(items))
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
				item := items[i]
				res := migrateOne(ctx, dyn, gvr, target, &item, applyOpts)
				if tagCRD {
					res.CRD = target.crdName
				}
				mu.Lock()
				results[i] = res
				done++
				if progress {
					_, _ = fmt.Fprintf(os.Stderr, "\rmigrated %d/%d objects in %s", done, len(items), target.crdName)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if progress {
		_, _ = fmt.Fprintln(os.Stderr)
	}
	return results
}

func migrateOne(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, target migrateTarget, item *unstructured.Unstructured, applyOpts metav1.ApplyOptions) MigrateObjectResult {
	name := item.GetName()
	ns := item.GetNamespace()
	res := MigrateObjectResult{Name: name, Namespace: ns}
	if target.namespaced && ns == "" {
		res.Error = "namespaced object is missing metadata.namespace"
		return res
	}

	patch := migrateEmptyPatch(target.group, target.storageVersion, target.kind, name, ns)
	var err error
	if target.namespaced {
		_, err = dyn.Resource(gvr).Namespace(ns).Apply(ctx, name, patch, applyOpts)
	} else {
		_, err = dyn.Resource(gvr).Apply(ctx, name, patch, applyOpts)
	}
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

// migrateEmptyPatch is the identity-only SSA object: apiVersion, kind,
// metadata.name, and metadata.namespace when namespaced. Nothing else —
// no spec, status, annotations, or resourceVersion — so the apply claims
// only identity fields while still going through the persist path.
func migrateEmptyPatch(group, version, kind, name, namespace string) *unstructured.Unstructured {
	meta := map[string]any{"name": name}
	if namespace != "" {
		meta["namespace"] = namespace
	}
	apiVersion := version
	if group != "" {
		apiVersion = group + "/" + version
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   meta,
	}}
}

// resolveMigrateTargets resolves everything a run has to cover. For a
// --crd run that is one CRD. For a --xrd run it is every CRD Crossplane
// generates from the XRD: the composite, plus the claim when
// spec.claimNames is present.
func resolveMigrateTargets(ctx context.Context, dyn dynamic.Interface, opts MigrateStorageOptions) ([]migrateTarget, []string, error) {
	if opts.CRDName != "" {
		crd, err := FetchLiveCRD(ctx, dyn, opts.CRDName)
		if err != nil {
			return nil, nil, err
		}
		t, err := targetFromCRD("CRD", crd.Name, crd)
		if err != nil {
			return nil, nil, err
		}
		t.role = string(xrdadapter.RoleComposite)
		return []migrateTarget{t}, nil, nil
	}

	xrd, err := FetchLiveXRD(ctx, dyn, opts.XRDName)
	if err != nil {
		return nil, nil, err
	}
	generated, err := xrdadapter.GeneratedCRDNames(xrd)
	if err != nil {
		return nil, nil, err
	}
	refVersion, err := xrdReferenceableVersion(xrd)
	if err != nil {
		return nil, nil, err
	}

	var (
		targets  []migrateTarget
		warnings []string
	)
	for _, g := range generated {
		crd, err := FetchLiveCRD(ctx, dyn, g.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("getting generated %s CRD %q for XRD %q: %w", g.Role, g.Name, xrd.GetName(), err)
		}
		storage, err := crdStorageVersion(crd)
		if err != nil {
			return nil, nil, err
		}
		if refVersion != storage {
			warnings = append(warnings, fmt.Sprintf("XRD %q referenceable version %q differs from generated CRD %q storage version %q; using the CRD (that is what etcd stores)",
				xrd.GetName(), refVersion, g.Name, storage))
		}
		targets = append(targets, migrateTarget{
			resourceKind:   "XRD",
			resource:       xrd.GetName(),
			crdName:        g.Name,
			role:           string(g.Role),
			group:          g.Group,
			kind:           g.Kind,
			plural:         g.Plural,
			storageVersion: storage,
			storedVersions: append([]string(nil), crd.Status.StoredVersions...),
			// Read the scope off the generated CRD rather than deriving it
			// again: this is the object the apiserver actually serves, and
			// it is authoritative about its own scope.
			namespaced: crd.Spec.Scope == extv1.NamespaceScoped,
		})
	}
	return targets, warnings, nil
}

func targetFromCRD(resourceKind, resourceName string, crd *extv1.CustomResourceDefinition) (migrateTarget, error) {
	if crd.Spec.Group == "" {
		return migrateTarget{}, fmt.Errorf("crd %q is missing spec.group", crd.Name)
	}
	if crd.Spec.Names.Plural == "" {
		return migrateTarget{}, fmt.Errorf("crd %q is missing spec.names.plural", crd.Name)
	}
	if crd.Spec.Names.Kind == "" {
		return migrateTarget{}, fmt.Errorf("crd %q is missing spec.names.kind", crd.Name)
	}
	storage, err := crdStorageVersion(crd)
	if err != nil {
		return migrateTarget{}, err
	}
	return migrateTarget{
		resourceKind:   resourceKind,
		resource:       resourceName,
		crdName:        crd.Name,
		group:          crd.Spec.Group,
		kind:           crd.Spec.Names.Kind,
		plural:         crd.Spec.Names.Plural,
		storageVersion: storage,
		storedVersions: append([]string(nil), crd.Status.StoredVersions...),
		namespaced:     crd.Spec.Scope == extv1.NamespaceScoped,
	}, nil
}

func crdStorageVersion(crd *extv1.CustomResourceDefinition) (string, error) {
	var storage []string
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			storage = append(storage, v.Name)
		}
	}
	switch len(storage) {
	case 1:
		return storage[0], nil
	case 0:
		return "", fmt.Errorf("crd %q has no storage version", crd.Name)
	default:
		return "", fmt.Errorf("crd %q has multiple storage versions: %s", crd.Name, strings.Join(storage, ", "))
	}
}

func xrdReferenceableVersion(xrd *unstructured.Unstructured) (string, error) {
	versions, found, err := unstructured.NestedSlice(xrd.Object, "spec", "versions")
	if err != nil {
		return "", fmt.Errorf("xrd %q spec.versions: %w", xrd.GetName(), err)
	}
	if !found {
		return "", fmt.Errorf("xrd %q has no spec.versions", xrd.GetName())
	}
	var refs []string
	for i, raw := range versions {
		vm, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("xrd %q spec.versions[%d] is not an object", xrd.GetName(), i)
		}
		referenceable, _, err := unstructured.NestedBool(vm, "referenceable")
		if err != nil {
			return "", fmt.Errorf("xrd %q spec.versions[%d].referenceable: %w", xrd.GetName(), i, err)
		}
		if !referenceable {
			continue
		}
		name, _, err := unstructured.NestedString(vm, "name")
		if err != nil || name == "" {
			return "", fmt.Errorf("xrd %q spec.versions[%d] is referenceable but missing name", xrd.GetName(), i)
		}
		refs = append(refs, name)
	}
	switch len(refs) {
	case 1:
		return refs[0], nil
	case 0:
		return "", fmt.Errorf("xrd %q has no referenceable version", xrd.GetName())
	default:
		return "", fmt.Errorf("xrd %q has multiple referenceable versions: %s", xrd.GetName(), strings.Join(refs, ", "))
	}
}

func pruneStoredVersions(ctx context.Context, dyn dynamic.Interface, crdName, storageVersion string) error {
	obj, err := dyn.Resource(crdGVR).Get(ctx, crdName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting CRD %q to prune storedVersions: %w", crdName, err)
	}
	if err := unstructured.SetNestedStringSlice(obj.Object, []string{storageVersion}, "status", "storedVersions"); err != nil {
		return fmt.Errorf("setting status.storedVersions on CRD %q: %w", crdName, err)
	}
	if _, err := dyn.Resource(crdGVR).UpdateStatus(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating CRD %q status.storedVersions: %w", crdName, err)
	}
	return nil
}

func newMigrateStorageCmd() *cobra.Command {
	var (
		opts   MigrateStorageOptions
		output string
	)
	cmd := &cobra.Command{
		Use:   "migrate-storage",
		Short: "Rewrite live CRs/XRs at the current storage version via empty SSA patches",
		Long: `Rewrite every live instance of a target XRD or CRD so etcd stores it at the
current storage version.

This is a live, mutating command. --xrd and --crd take cluster resource names,
not local YAML files. No conversion config is required.

Kubernetes stores each custom resource at exactly one version (storage: true on
a CRD, referenceable: true on an XRD). After you promote a new storage version,
objects already in etcd stay encoded at whichever version was storage when they
were last written — unless something writes them again. The apiserver serves
them correctly either way, but dropping an old version is rejected until
status.storedVersions no longer lists it.

On an XRD, promoting the hub already writes every XR: Crossplane requires a new
Composition (compositeTypeRef is immutable) and a compositionRef patch, which
persists objects at the new referenceable version. The generated CRD's
storedVersions still never shrinks, so --prune-stored-versions is the step that
unblocks deleting an old version block. The empty SSA pass is belt-and-suspenders
(stragglers you forgot to retarget).

A scope: LegacyCluster XRD with spec.claimNames generates TWO CRDs — the
cluster-scoped composite and the namespace-scoped claim — and both are covered.
Claims are their own stored object class with their own status.storedVersions,
so migrating only the composite leaves the old version un-droppable, which is
the entire reason to run --prune-stored-versions. A failure on either CRD blocks
the prune on both: a half-pruned pair still cannot drop the version, but has
already discarded the record of which objects were stored at it.

On a native CRD, flipping storage: true does not write existing objects. There
is no compositionRef equivalent. Empty SSA is the actual etcd rewrite and is
the critical path.

migrate-storage does that rewrite with an empty server-side-apply patch
(apiVersion, kind, metadata.name, and metadata.namespace only) under a dedicated
field manager, with force-conflicts. The apply claims only identity fields; the
write still goes through the persist path, so etcd is re-encoded at the current
storage version. Conversion webhooks — including this operator — run as they
would on any write.

This is not the Kubernetes 1.30+ StorageVersionMigration API, and it does not
need the storage-version-migrator. It is the standard empty-SSA approach that
works on any cluster.

The invoking identity needs get on the XRD/CRD, and list+patch on the target
CRs/XRs. --prune-stored-versions additionally needs update on
customresourcedefinitions/status.`,
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
			rep, err := RunMigrateStorage(cmd.Context(), dyn, opts)
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
	cmd.Flags().StringVar(&opts.CRDName, "crd", "", "Cluster name of the CustomResourceDefinition (not a file path)")
	cmd.Flags().StringVar(&opts.Kubeconfig, "kubeconfig", "", "Path to a kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	cmd.Flags().StringVar(&opts.KubeContext, "context", "", "Kubeconfig context to use (default: the kubeconfig's current-context)")
	cmd.Flags().StringVarP(&opts.Namespace, "namespace", "n", "", "Limit to this namespace (default: all namespaces; ignored for cluster-scoped types, which on a LegacyCluster XRD is the composite side — only its claims are namespaced)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Apply with server-side dry-run (exercises conversion, does not persist)")
	cmd.Flags().IntVar(&opts.Concurrency, "concurrency", 1, "Number of objects to patch in parallel")
	cmd.Flags().StringVar(&opts.FieldManager, "field-manager", defaultMigrateFieldManager, "SSA field manager name")
	cmd.Flags().BoolVar(&opts.PruneStoredVersions, "prune-stored-versions", false, "After every object succeeds, set every generated CRD's status.storedVersions to the current storage version only (refused with --namespace)")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "Output format: table|json")
	cmd.Flags().BoolVar(&opts.Quiet, "quiet", false, "Suppress the progress line written to stderr")
	cmd.MarkFlagsOneRequired("xrd", "crd")
	cmd.MarkFlagsMutuallyExclusive("xrd", "crd")
	registerKubeFlagCompletions(cmd)
	registerLiveResourceCompletions(cmd)
	registerOutputCompletions(cmd, "table", "json")
	return cmd
}
