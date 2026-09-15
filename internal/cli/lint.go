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
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"

	sigsyaml "sigs.k8s.io/yaml"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// LintOptions configures a whole-tree lint.
type LintOptions struct {
	// Paths are the directories (or files) to walk. Empty means ".".
	Paths []string
	// SchemaDirs are additional trees to search for XRDs and CRDs, for
	// repositories that keep schemas apart from conversion configs.
	SchemaDirs []string
	// Exclude are glob patterns matched against each path; a match is
	// skipped entirely.
	Exclude []string
	// Concurrency bounds the parallel analysis. Zero means one per CPU.
	Concurrency int
	// PackageRef pairs every config in the tree against the XRDs a
	// Crossplane package ships, rather than against schema files. A whole
	// package against a whole config tree, one command, is the
	// Configuration repository's CI gate.
	PackageRef string
}

// LintPairResult is one config and the schema it was paired with.
type LintPairResult struct {
	Config     string `json:"config"`
	ConfigName string `json:"configName"`
	Target     string `json:"target"`
	Schema     string `json:"schema,omitempty"`
	Kind       string `json:"kind"` // XRD | CRD
	// Status is "ok", "unpaired", "duplicate", or "invalid".
	Status   string    `json:"status"`
	Findings []Finding `json:"findings,omitempty"`
}

// LintReport is the result over a whole tree.
type LintReport struct {
	Configs   int              `json:"configs"`
	Schemas   int              `json:"schemas"`
	Unpaired  int              `json:"unpaired"`
	Duplicate int              `json:"duplicate"`
	Errors    int              `json:"errors"`
	Warnings  int              `json:"warnings"`
	Results   []LintPairResult `json:"results"`
}

// Findings flattens every pair's findings, for the CI output formats.
func (r *LintReport) Findings() []Finding {
	var out []Finding
	for _, res := range r.Results {
		out = append(out, res.Findings...)
	}
	sortFindings(out)
	return out
}

// discovered is one file the walk recognised.
type discovered struct {
	path string
	kind string // XRDConversionConfig | CRDConversionConfig | XRD | CRD
	// name is metadata.name for a schema, and the target name for a config.
	name string
	// configName is metadata.name for a config.
	configName string
	// display overrides path in the report. A schema staged out of a
	// package lives in a temporary directory, and printing that path tells
	// the reader nothing about where the schema came from.
	display string
}

// RunLint walks the given trees and checks every conversion config it finds
// against the schema it targets.
//
// Deliberately offline: it constructs no Kubernetes client at all. This is
// the check that runs on every commit, and a check that needs cluster
// credentials does not run on every commit.
func RunLint(opts LintOptions) (*LintReport, error) {
	paths := opts.Paths
	if len(paths) == 0 {
		paths = []string{"."}
	}

	var found []discovered
	for _, root := range append(append([]string{}, paths...), opts.SchemaDirs...) {
		items, err := discoverIn(root, opts.Exclude)
		if err != nil {
			return nil, err
		}
		found = append(found, items...)
	}

	// Schemas from the package, when one was named. They are written to a
	// temporary directory rather than held as objects because every
	// downstream check reads a path, and a package is read once here
	// rather than once per config.
	var cleanup func()
	if opts.PackageRef != "" {
		pkgSchemas, done, err := stagePackageXRDs(opts.PackageRef)
		if err != nil {
			return nil, err
		}
		cleanup = done
		found = append(found, pkgSchemas...)
	}
	if cleanup != nil {
		defer cleanup()
	}

	// Schemas first, so a config can be paired as soon as it is seen. A
	// name collision between two schema files is itself worth reporting,
	// but the first wins deterministically because the walk is sorted.
	schemas := map[string]discovered{}
	var configs []discovered
	for _, d := range found {
		switch d.kind {
		case "XRD", "CRD":
			key := d.kind + "/" + d.name
			if _, exists := schemas[key]; !exists {
				schemas[key] = d
			}
		default:
			configs = append(configs, d)
		}
	}

	rep := &LintReport{Configs: len(configs), Schemas: len(schemas)}

	// A second config targeting the same resource is reported rather than
	// analyzed: the operator enforces one config per target, and finding
	// that out from an admission rejection after merge is the whole thing
	// this command exists to prevent.
	byTarget := map[string][]discovered{}
	for _, c := range configs {
		byTarget[targetKey(c)] = append(byTarget[targetKey(c)], c)
	}

	results := make([]LintPairResult, len(configs))
	workers := opts.Concurrency
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers > len(configs) {
		workers = len(configs)
	}
	var wg sync.WaitGroup
	jobs := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = lintOne(configs[i], schemas, byTarget)
			}
		}()
	}
	for i := range configs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	// Results are collected by index, so the report order is the walk
	// order regardless of which worker finished first.
	rep.Results = results
	for _, res := range results {
		switch res.Status {
		case "unpaired":
			rep.Unpaired++
		case "duplicate":
			rep.Duplicate++
		}
		for _, f := range res.Findings {
			switch f.Severity {
			case SeverityFindingError:
				rep.Errors++
			case SeverityFindingWarning:
				rep.Warnings++
			}
		}
	}
	return rep, nil
}

// otherTargets names the competing configs, bounded: a tree with a config
// per environment produces a list nobody reads, and the first few plus a
// count says the same thing.
func otherTargets(peers []discovered, self string) string {
	const show = 3
	var others []string
	for _, p := range peers {
		if p.path != self {
			others = append(others, p.path)
		}
	}
	if len(others) <= show {
		return strings.Join(others, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(others[:show], ", "), len(others)-show)
}

func targetKey(c discovered) string {
	kind := "XRD"
	if c.kind == "CRDConversionConfig" {
		kind = "CRD"
	}
	return kind + "/" + c.name
}

func lintOne(c discovered, schemas map[string]discovered, byTarget map[string][]discovered) LintPairResult {
	res := LintPairResult{Config: c.path, ConfigName: c.configName, Target: c.name}
	key := targetKey(c)
	res.Kind = strings.SplitN(key, "/", 2)[0]
	sm := SourceMapForConfig(c.path)

	if peers := byTarget[key]; len(peers) > 1 && peers[0].path != c.path {
		res.Status = "duplicate"
		res.Findings = append(res.Findings, Finding{
			RuleID: FindingConfigError, Severity: SeverityFindingError, Location: sm.Document(),
			Message: fmt.Sprintf("%s is also targeted by %s; the operator enforces one config per target and will reject the second at admission",
				res.Target, otherTargets(peers, c.path)),
		})
		return res
	}

	schema, ok := schemas[key]
	if !ok {
		// Never a silent skip. A config paired with nothing is exactly the
		// case where the reader most needs to be told, because it looks
		// identical to a clean result.
		res.Status = "unpaired"
		res.Findings = append(res.Findings, Finding{
			RuleID: FindingConfigError, Severity: SeverityFindingError, Location: sm.Document(),
			Message: fmt.Sprintf("no %s named %q was found in the tree, so this config could not be checked against a schema; pass --schema-dir if its schema lives elsewhere",
				res.Kind, res.Target),
		})
		return res
	}
	res.Schema = schema.path
	if schema.display != "" {
		res.Schema = schema.display
	}

	var out *ValidateResult
	var err error
	if res.Kind == "CRD" {
		out, err = RunValidate(c.path, "", schema.path)
	} else {
		out, err = RunValidate(c.path, schema.path, "")
	}
	if err != nil {
		res.Status = "invalid"
		res.Findings = append(res.Findings, Finding{
			RuleID: FindingConfigError, Severity: SeverityFindingError, Location: sm.Document(),
			Message: err.Error(),
		})
		return res
	}
	res.Findings = validateFindings(out, c.path)
	if out.Analysis != nil {
		res.Findings = findingsFromAnalyze(*out.Analysis, sm)
		if len(out.Errors) > 0 && len(res.Findings) == 0 {
			res.Findings = validateFindings(out, c.path)
		}
	}
	res.Status = "ok"
	for _, f := range res.Findings {
		if f.Severity == SeverityFindingError {
			res.Status = "invalid"
		}
	}
	return res
}

// stagePackageXRDs writes a package's XRDs to a temporary directory and
// returns them as discovered schemas.
//
// Reading the package once and staging it keeps the pairing logic identical
// to the file case: every config in the tree is checked against the XRDs the
// package declares, which is the Configuration repository's gate.
func stagePackageXRDs(ref string) ([]discovered, func(), error) {
	pkg, err := ReadPackage(ref)
	if err != nil {
		return nil, nil, err
	}
	return stagePackageXRDsFrom(pkg, ref)
}

// stagePackageXRDsFrom is stagePackageXRDs with the package already read,
// so the staging rules can be tested against hostile contents without
// building a hostile package.
func stagePackageXRDsFrom(pkg *PackageContents, ref string) ([]discovered, func(), error) {
	if len(pkg.XRDs) == 0 {
		return nil, nil, fmt.Errorf("%s ships no XRDs to check configs against", ref)
	}
	dir, err := os.MkdirTemp("", "convctl-package-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	var out []discovered
	for i, x := range pkg.XRDs {
		data, merr := sigsyaml.Marshal(x.Object)
		if merr != nil {
			cleanup()
			return nil, nil, fmt.Errorf("%s: re-encoding %s: %w", ref, xrdName(x), merr)
		}
		// The name comes out of a package pulled from a registry or handed
		// over by a third party, so it is untrusted: an XRD called
		// ../../evil would otherwise have filepath.Join resolve outside the
		// temporary directory and write wherever the invoking user can. The
		// index keeps two XRDs of the same name from overwriting each
		// other, which the base name alone would not.
		path := filepath.Join(dir, fmt.Sprintf("%02d-%s.yaml", i, filepath.Base(xrdName(x))))
		if werr := os.WriteFile(path, data, 0o600); werr != nil {
			cleanup()
			return nil, nil, werr
		}
		out = append(out, discovered{path: path, kind: "XRD", name: xrdName(x), display: ref + " (" + xrdName(x) + ")"})
	}
	return out, cleanup, nil
}

// discoverIn walks one tree, recognising conversion configs and schemas by
// their own apiVersion/kind rather than by filename.
func discoverIn(root string, exclude []string) ([]discovered, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}
	var files []string
	if !info.IsDir() {
		files = []string{root}
	} else {
		err = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if fi.IsDir() {
				if excluded(p, exclude) || strings.HasPrefix(filepath.Base(p), ".") && filepath.Base(p) != "." {
					return filepath.SkipDir
				}
				return nil
			}
			if !isYAML(p) || excluded(p, exclude) {
				return nil
			}
			files = append(files, p)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	// Sorted, so the report order and the "first wins" tie-break do not
	// depend on the filesystem's iteration order.
	sort.Strings(files)

	var out []discovered
	for _, f := range files {
		out = append(out, classifyDocuments(f)...)
	}
	return out, nil
}

func isYAML(p string) bool {
	l := strings.ToLower(p)
	return strings.HasSuffix(l, ".yaml") || strings.HasSuffix(l, ".yml")
}

func excluded(p string, patterns []string) bool {
	for _, pat := range patterns {
		if ok, _ := filepath.Match(pat, p); ok {
			return true
		}
		if ok, _ := filepath.Match(pat, filepath.Base(p)); ok {
			return true
		}
	}
	return false
}

// classifyDocuments reads one file's documents and reports which of them this command
// cares about. A file that is not YAML, or is YAML this tool has no opinion
// about, contributes nothing and is not an error: a platform repository is
// full of manifests that are none of its business.
func classifyDocuments(path string) []discovered {
	docs, err := decodeAllDocuments(path)
	if err != nil {
		return nil
	}
	var out []discovered
	for _, doc := range docs {
		kind, _, _ := unstructured.NestedString(doc, "kind")
		apiVersion, _, _ := unstructured.NestedString(doc, "apiVersion")
		name, _, _ := unstructured.NestedString(doc, "metadata", "name")
		switch {
		case kind == "XRDConversionConfig":
			target, _, _ := unstructured.NestedString(doc, "spec", "targetXRD", "name")
			out = append(out, discovered{path: path, kind: kind, name: target, configName: name})
		case kind == "CRDConversionConfig":
			target, _, _ := unstructured.NestedString(doc, "spec", "targetCRD", "name")
			out = append(out, discovered{path: path, kind: kind, name: target, configName: name})
		case kind == "CompositeResourceDefinition" && strings.HasPrefix(apiVersion, "apiextensions.crossplane.io/"):
			out = append(out, discovered{path: path, kind: "XRD", name: name})
		case kind == "CustomResourceDefinition" && strings.HasPrefix(apiVersion, "apiextensions.k8s.io/"):
			out = append(out, discovered{path: path, kind: "CRD", name: name})
		}
	}
	return out
}

// WriteTable renders the lint result.
func (r *LintReport) WriteTable(w io.Writer) {
	_, _ = fmt.Fprintf(w, "convctl lint: %d config(s), %d schema(s)\n\n", r.Configs, r.Schemas)
	if len(r.Results) == 0 {
		_, _ = fmt.Fprintln(w, "No conversion configs found. Check the path, or --exclude.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "STATUS\tCONFIG\tTARGET\tSCHEMA\tFINDINGS")
	for _, res := range r.Results {
		schema := res.Schema
		if schema == "" {
			schema = "—"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", strings.ToUpper(res.Status), res.Config, res.Target, schema, len(res.Findings))
	}
	_ = tw.Flush()

	for _, res := range r.Results {
		if len(res.Findings) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "\n%s:\n", res.Config)
		for _, f := range res.Findings {
			_, _ = fmt.Fprintf(w, "  %-7s %s  %s\n", f.Severity, f.Location.String(), f.Message)
		}
	}
	_, _ = fmt.Fprintf(w, "\nSUMMARY: %d error(s), %d warning(s), %d unpaired, %d duplicate\n",
		r.Errors, r.Warnings, r.Unpaired, r.Duplicate)
}

// decideLintExitCode follows the same threshold matrix as test: errors
// always fail, warnings fail only at --fail-on warn.
func decideLintExitCode(r *LintReport, failOn string) int {
	if failOn == failOnNone {
		return ExitOK
	}
	if r.Errors > 0 || r.Unpaired > 0 || r.Duplicate > 0 {
		return ExitTestFailure
	}
	if failOn == failOnWarn && r.Warnings > 0 {
		return ExitTestFailure
	}
	return ExitOK
}
