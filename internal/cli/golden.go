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
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	sigsyaml "sigs.k8s.io/yaml"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// GoldenManifestFile is the corpus's own metadata, recording what produced
// it. A corpus whose manifest no longer matches the config it is replayed
// against is not necessarily wrong, but it is no longer evidence about the
// current rules, and saying so is the whole point.
const GoldenManifestFile = "manifest.yaml"

// GoldenManifest records what produced a corpus.
//
// Deliberately carries no timestamp: the corpus is committed and diffed, so
// a field that changes on every re-record would make every re-record a noisy
// diff and defeat the feature.
type GoldenManifest struct {
	ConvctlVersion string `json:"convctlVersion"`
	PlanHash       string `json:"planHash"`
	SchemaHash     string `json:"schemaHash"`
	Resource       string `json:"resource"`
	Config         string `json:"config"`
	HubVersion     string `json:"hubVersion"`
}

// GoldenDrift is one difference between a recorded corpus and this run.
type GoldenDrift struct {
	// File is the corpus-relative path, e.g. "sample.yaml/v2-to-v1.yaml".
	File string `json:"file"`
	// Kind is "changed", "missing" (in the corpus but not produced now),
	// or "orphaned" (in the corpus with nothing producing it). Each is a
	// distinct failure with a distinct remedy, so they are not collapsed.
	Kind string `json:"kind"`
	// Fields are the leaf paths that differ, for Kind == "changed".
	Fields []string `json:"fields,omitempty"`
	Detail string   `json:"detail"`
}

// corpus records or replays conversion output.
//
// A conversion config change arrives in review as a YAML diff of the rules
// plus a green check. Neither shows what the change does to real objects,
// which is the only thing that matters. Recording the output of every
// conversion path into a committed corpus makes the next rule change show up
// in the PR diff as the behavioural change it is.
type corpus struct {
	dir  string
	mode string // "record" | "golden"

	mu sync.Mutex
	// seen maps each corpus path to the sample identity that claimed it, so
	// two samples that sanitize to the same path are reported rather than
	// silently overwriting each other's golden.
	seen    map[string]string
	drifts  []GoldenDrift
	written int
}

func newCorpus(dir, mode string) *corpus {
	return &corpus{dir: dir, mode: mode, seen: map[string]string{}}
}

// goldenRelPath is the corpus-relative path for one conversion.
//
// Deterministic and one object per file, so a diff is readable and a moved
// or renamed sample shows up as exactly that rather than as churn across
// unrelated files.
func goldenRelPath(sample, from, to string) string {
	return filepath.Join(sanitizeForPath(sample), fmt.Sprintf("%s-to-%s.yaml", from, to))
}

// sanitizeForPath keeps a sample's identity in the filename while ensuring
// it stays a single, safe path segment. --live sample identifiers can carry
// a namespace separator.
//
// Only ".yaml" is stripped. Stripping ".yml" as well would map foo.yaml and
// foo.yml onto one directory, and the second one recorded would overwrite
// the first one's goldens.
func sanitizeForPath(s string) string {
	s = strings.TrimSuffix(s, ".yaml")
	repl := strings.NewReplacer(string(filepath.Separator), "_", "/", "_", "\\", "_", ":", "_", " ", "_")
	s = repl.Replace(s)
	// "." and ".." are directory references, not names. A sample file
	// literally called "...yaml" would otherwise become "..", and joining
	// that against the corpus directory writes outside it.
	if s == "" || strings.Trim(s, ".") == "" {
		return "_" + s
	}
	return s
}

// marshalStable renders an object so that recording the same conversion
// twice produces byte-identical output.
//
// sigs.k8s.io/yaml routes through encoding/json, which sorts map keys and
// formats floats deterministically — the two properties a committed corpus
// needs and a plain YAML marshaller does not guarantee.
func marshalStable(obj map[string]any) ([]byte, error) {
	return sigsyaml.Marshal(obj)
}

// observe records or compares one conversion result.
func (c *corpus) observe(sample, from, to string, obj map[string]any) {
	if c == nil {
		return
	}
	rel := goldenRelPath(sample, from, to)
	data, err := marshalStable(obj)
	if err != nil {
		c.addDrift(GoldenDrift{File: rel, Kind: "changed", Detail: fmt.Sprintf("serializing converted object: %v", err)})
		return
	}

	c.mu.Lock()
	if prev, ok := c.seen[rel]; ok && prev != sample {
		c.seen[rel] = prev
		c.mu.Unlock()
		c.addDrift(GoldenDrift{
			File: rel, Kind: "changed",
			Detail: fmt.Sprintf("samples %q and %q both map to this corpus path; rename one, because only one of their goldens can be kept", prev, sample),
		})
		return
	}
	c.seen[rel] = sample
	c.mu.Unlock()

	full := filepath.Join(c.dir, rel)
	if c.mode == "record" {
		// #nosec G301 -- a golden corpus is committed to the repository and
		// read by everyone on the team and by CI; 0750 would be wrong for
		// it in a way the report file in root.go's 0600 is not.
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			c.addDrift(GoldenDrift{File: rel, Kind: "changed", Detail: err.Error()})
			return
		}
		// #nosec G306 -- see the directory mode above; goldens are meant to
		// be world-readable source files.
		if err := os.WriteFile(full, data, 0o644); err != nil {
			c.addDrift(GoldenDrift{File: rel, Kind: "changed", Detail: err.Error()})
			return
		}
		c.mu.Lock()
		c.written++
		c.mu.Unlock()
		return
	}

	want, err := os.ReadFile(full) // #nosec G304 -- a corpus path the caller named, same trust as --samples
	if err != nil {
		if os.IsNotExist(err) {
			c.addDrift(GoldenDrift{
				File: rel, Kind: "missing",
				Detail: "this conversion produced output but the corpus has no golden for it; re-record with --record, or add the sample to the corpus",
			})
			return
		}
		c.addDrift(GoldenDrift{File: rel, Kind: "missing", Detail: err.Error()})
		return
	}
	if string(want) == string(data) {
		return
	}

	// Field-level, not a byte diff: "these three fields changed" is the
	// reviewable form; "the file changed" is not.
	var old map[string]any
	fields := []string{"(unparseable golden)"}
	if err := sigsyaml.Unmarshal(want, &old); err == nil {
		fields = diffLeaves(old, obj)
	}
	c.addDrift(GoldenDrift{
		File: rel, Kind: "changed", Fields: fields,
		Detail: fmt.Sprintf("converted output differs from the corpus in %d field(s)", len(fields)),
	})
}

func (c *corpus) addDrift(d GoldenDrift) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drifts = append(c.drifts, d)
}

// finish writes the manifest (record mode) or detects orphans (golden mode).
//
// An orphan is a golden whose conversion no longer happens at all — a sample
// deleted, a version dropped, a path no longer configured. It is a real
// change to behaviour and is reported rather than ignored, because a corpus
// that silently keeps stale entries stops being evidence.
func (c *corpus) finish(m GoldenManifest) error {
	if c == nil {
		return nil
	}
	if c.mode == "record" {
		// Re-recording has to remove what it no longer produces. Otherwise
		// a dropped sample or version leaves its golden behind, the next
		// replay reports it as orphaned, and the remedy that report gives
		// -- re-record -- does not fix it.
		if err := c.pruneUnseen(); err != nil {
			return err
		}
		data, err := sigsyaml.Marshal(m)
		if err != nil {
			return fmt.Errorf("serializing corpus manifest: %w", err)
		}
		// #nosec G301 -- see observe: a committed corpus directory.
		if err := os.MkdirAll(c.dir, 0o755); err != nil {
			return err
		}
		// #nosec G306 -- a committed manifest, read by everyone.
		return os.WriteFile(filepath.Join(c.dir, GoldenManifestFile), data, 0o644)
	}

	existing, err := listCorpusFiles(c.dir)
	if err != nil {
		return err
	}
	for _, rel := range existing {
		if _, ok := c.seen[rel]; !ok {
			c.addDrift(GoldenDrift{
				File: rel, Kind: "orphaned",
				Detail: "the corpus holds a golden for a conversion this run no longer produces; re-record with --record if the path was removed deliberately",
			})
		}
	}

	// A manifest mismatch is a loud warning rather than a failure: the
	// corpus may legitimately predate a config edit, and the field diffs
	// below are the real evidence either way. A manifest that cannot be
	// read at all is reported too -- silently skipping it would let a
	// corpus with no provenance report "no drift".
	got, err := readManifest(c.dir)
	switch {
	case err != nil:
		c.addDrift(GoldenDrift{
			File: GoldenManifestFile, Kind: "manifest",
			Detail: fmt.Sprintf("the corpus manifest could not be read (%v), so which config and schema produced these goldens is unknown; re-record with --record", err),
		})
	default:
		if got.PlanHash != m.PlanHash || got.SchemaHash != m.SchemaHash {
			c.addDrift(GoldenDrift{
				File: GoldenManifestFile, Kind: "manifest",
				Detail: fmt.Sprintf("corpus was recorded from a different config or schema (corpus planHash=%s schemaHash=%s; current planHash=%s schemaHash=%s) — the diffs below may reflect that rather than a regression",
					short(got.PlanHash), short(got.SchemaHash), short(m.PlanHash), short(m.SchemaHash)),
			})
		}
	}
	return nil
}

// pruneUnseen deletes goldens the current run did not produce, so a
// re-recorded corpus describes only what the conversion does now.
func (c *corpus) pruneUnseen() error {
	// A first run, or a run that produced no conversions at all, has no
	// corpus directory yet. Checked here rather than on the error from
	// listCorpusFiles, which wraps the not-exist error in one of its own --
	// so os.IsNotExist would not match it and the run would fail.
	if _, err := os.Stat(c.dir); os.IsNotExist(err) {
		return nil
	}
	existing, err := listCorpusFiles(c.dir)
	if err != nil {
		return err
	}
	for _, rel := range existing {
		if _, ok := c.seen[rel]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(c.dir, rel)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing stale golden %s: %w", rel, err)
		}
	}
	return nil
}

func short(h string) string {
	h = strings.TrimPrefix(h, "sha256:")
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func readManifest(dir string) (GoldenManifest, error) {
	var m GoldenManifest
	// #nosec G304 -- a corpus path the caller named
	data, err := os.ReadFile(filepath.Join(dir, GoldenManifestFile))
	if err != nil {
		return m, err
	}
	return m, sigsyaml.Unmarshal(data, &m)
}

// listCorpusFiles returns every golden in the corpus, corpus-relative,
// excluding the manifest.
func listCorpusFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		if rel == GoldenManifestFile {
			return nil
		}
		if !strings.HasSuffix(rel, ".yaml") && !strings.HasSuffix(rel, ".yml") {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("golden corpus directory %q does not exist; record one first with --record %s", dir, dir)
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// hashPlan fingerprints the compiled rules, so a corpus knows which config
// produced it.
//
// Derived from the analysis report rather than the config object, so one
// implementation serves both XRDConversionConfig and CRDConversionConfig and
// the fingerprint reflects what was actually compiled. Rule order is part of
// the plan and is deliberately not sorted away.
func hashPlan(report engine.AnalyzeReport, hub string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "hub=%s;", hub)
	spokes := make([]engine.SpokeReport, len(report.SpokeReports))
	copy(spokes, report.SpokeReports)
	sort.SliceStable(spokes, func(i, j int) bool { return spokes[i].Version < spokes[j].Version })
	for _, sr := range spokes {
		_, _ = fmt.Fprintf(h, "spoke=%s;", sr.Version)
		for _, rr := range sr.RuleResults {
			_, _ = fmt.Fprintf(h, "rule[%d]=%s;hub=%v;spoke=%v;", rr.Index, rr.Strategy, rr.HubPaths, rr.SpokePaths)
		}
	}
	return "sha256:" + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// hashSchemas fingerprints the version schemas the corpus was recorded
// against, so a schema change that alters conversion is attributable.
func hashSchemas(versions []engine.VersionSchema) string {
	h := sha256.New()
	names := make([]string, 0, len(versions))
	byName := map[string]engine.VersionSchema{}
	for _, v := range versions {
		names = append(names, v.Name)
		byName[v.Name] = v
	}
	sort.Strings(names)
	for _, n := range names {
		v := byName[n]
		_, _ = fmt.Fprintf(h, "version=%s;served=%v;storage=%v;", v.Name, v.Served, v.Storage)
		if v.Schema != nil {
			if data, err := sigsyaml.Marshal(v.Schema); err == nil {
				_, _ = h.Write(data)
			}
		}
	}
	return "sha256:" + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
