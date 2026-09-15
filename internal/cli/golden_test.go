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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fullFixture(t *testing.T) TestOptions {
	t.Helper()
	return TestOptions{
		XRDPath:    "testdata/full/xrd.yaml",
		ConfigPath: "testdata/full/config.yaml",
		SamplesDir: "testdata/full/samples",
		Quiet:      true,
	}
}

// The committed corpus is this repo dogfooding the feature: if a change to
// the engine or the fixture alters what any conversion produces, this test
// says which file and which field, rather than leaving it to be noticed in
// production.
func TestGolden_CommittedCorpusReplaysClean(t *testing.T) {
	opts := fullFixture(t)
	opts.GoldenDir = "testdata/full/golden"
	rep, err := RunTest(opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Golden == nil {
		t.Fatal("no golden report")
	}
	for _, d := range rep.Golden.Drifts {
		t.Errorf("%s: %s %s %v", d.Kind, d.File, d.Detail, d.Fields)
	}
	if rep.Golden.driftFatal() {
		t.Error("the committed corpus no longer matches what the engine produces; re-record with --record after confirming the change is intended")
	}
}

// Byte-stability is what makes the corpus reviewable: a re-record that
// reshuffled keys or reformatted numbers would bury the real change in
// noise.
func TestGolden_RecordIsByteStable(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, dir := range []string{a, b} {
		opts := fullFixture(t)
		opts.RecordDir = dir
		if _, err := RunTest(opts); err != nil {
			t.Fatal(err)
		}
	}
	files, err := listCorpusFiles(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("recorded nothing")
	}
	for _, rel := range append(files, GoldenManifestFile) {
		x, err := os.ReadFile(filepath.Join(a, rel))
		if err != nil {
			t.Fatal(err)
		}
		y, err := os.ReadFile(filepath.Join(b, rel))
		if err != nil {
			t.Fatal(err)
		}
		if string(x) != string(y) {
			t.Errorf("%s differs between two recordings of the same input", rel)
		}
	}
}

// Each drift kind has its own remedy, so each gets its own message rather
// than collapsing into "the corpus does not match".
func TestGolden_DistinguishesDriftKinds(t *testing.T) {
	base := t.TempDir()
	opts := fullFixture(t)
	opts.RecordDir = base
	if _, err := RunTest(opts); err != nil {
		t.Fatal(err)
	}

	t.Run("changed value names the field", func(t *testing.T) {
		dir := copyCorpus(t, base)
		target := filepath.Join(dir, "hub-v3", "v3-to-v1.yaml")
		data, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		mutated := strings.Replace(string(data), "cpuLimit: \"4\"", "cpuLimit: \"999\"", 1)
		if mutated == string(data) {
			t.Skip("fixture no longer contains the field this test perturbs")
		}
		if err := os.WriteFile(target, []byte(mutated), 0o644); err != nil {
			t.Fatal(err)
		}

		rep := replay(t, dir)
		d := findDrift(rep, "changed")
		if d == nil {
			t.Fatal("a changed value was not detected")
		}
		if !goldenHasField(d.Fields, "spec.cpuLimit") {
			t.Errorf("drift does not name the changed field: %v", d.Fields)
		}
	})

	t.Run("orphaned golden", func(t *testing.T) {
		dir := copyCorpus(t, base)
		src := filepath.Join(dir, "hub-v3", "v3-to-v1.yaml")
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		// A conversion path nothing produces any more.
		if err := os.WriteFile(filepath.Join(dir, "hub-v3", "v9-to-v1.yaml"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if findDrift(replay(t, dir), "orphaned") == nil {
			t.Error("a golden with nothing producing it was not reported")
		}
	})

	t.Run("missing golden", func(t *testing.T) {
		dir := copyCorpus(t, base)
		if err := os.Remove(filepath.Join(dir, "spoke-v1", "v1-to-v2.yaml")); err != nil {
			t.Fatal(err)
		}
		if findDrift(replay(t, dir), "missing") == nil {
			t.Error("a conversion with no golden was not reported")
		}
	})
}

// A manifest mismatch is a warning, not a failure: the corpus may
// legitimately predate a config edit, and the field diffs are the real
// evidence either way.
func TestGolden_ManifestMismatchWarnsButDoesNotFail(t *testing.T) {
	dir := t.TempDir()
	opts := fullFixture(t)
	opts.RecordDir = dir
	if _, err := RunTest(opts); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, GoldenManifestFile)
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(data), "planHash: sha256:", "planHash: sha256:XX", 1)
	if err := os.WriteFile(manifest, []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := replay(t, dir)
	if findDrift(rep, "manifest") == nil {
		t.Fatal("a corpus recorded from a different config was not flagged")
	}
	if rep.Golden.driftFatal() {
		t.Error("a manifest mismatch alone must not fail the run")
	}
}

func TestGolden_MissingCorpusDirectoryIsAClearError(t *testing.T) {
	opts := fullFixture(t)
	opts.GoldenDir = filepath.Join(t.TempDir(), "does-not-exist")
	_, err := RunTest(opts)
	if err == nil {
		t.Fatal("expected an error for a corpus directory that does not exist")
	}
	if !strings.Contains(err.Error(), "--record") {
		t.Errorf("the error should say how to create one: %v", err)
	}
}

func replay(t *testing.T, dir string) *Report {
	t.Helper()
	opts := fullFixture(t)
	opts.GoldenDir = dir
	rep, err := RunTest(opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Golden == nil {
		t.Fatal("no golden report")
	}
	return rep
}

func findDrift(rep *Report, kind string) *GoldenDrift {
	for i := range rep.Golden.Drifts {
		if rep.Golden.Drifts[i].Kind == kind {
			return &rep.Golden.Drifts[i]
		}
	}
	return nil
}

func copyCorpus(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	files, err := listCorpusFiles(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range append(files, GoldenManifestFile) {
		data, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			t.Fatal(err)
		}
		full := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func goldenHasField(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// A sample file named "...yaml" sanitizes to ".." unless that is caught, and
// joining ".." against the corpus directory writes outside it.
func TestSanitizeForPath_NeverProducesADirectoryReference(t *testing.T) {
	for _, in := range []string{"...yaml", ".yaml", "..", ".", "../escape.yaml", "a/b.yaml"} {
		got := sanitizeForPath(in)
		if got == "." || got == ".." || got == "" {
			t.Errorf("sanitizeForPath(%q) = %q, which is a directory reference", in, got)
		}
		if strings.ContainsRune(got, filepath.Separator) || strings.Contains(got, "/") {
			t.Errorf("sanitizeForPath(%q) = %q, which is more than one path segment", in, got)
		}
		rel := goldenRelPath(in, "v1", "v2")
		full := filepath.Clean(filepath.Join("corpus", rel))
		if !strings.HasPrefix(full, "corpus"+string(filepath.Separator)) {
			t.Errorf("goldenRelPath(%q) resolves to %q, outside the corpus directory", in, full)
		}
	}
}

// foo.yaml and foo.yml are two different samples and need two different
// corpus paths, or whichever records second silently overwrites the first.
func TestGoldenRelPath_DistinguishesYamlFromYml(t *testing.T) {
	if a, b := goldenRelPath("foo.yaml", "v1", "v2"), goldenRelPath("foo.yml", "v1", "v2"); a == b {
		t.Errorf("foo.yaml and foo.yml both map to %q", a)
	}
}

// Two samples whose names sanitize to the same path would otherwise
// overwrite each other's golden, and the corpus would silently describe only
// one of them.
func TestCorpus_ReportsAPathCollisionInsteadOfOverwriting(t *testing.T) {
	c := newCorpus(t.TempDir(), "record")
	c.observe("a/b.yaml", "v1", "v2", map[string]any{"x": 1})
	c.observe("a_b.yaml", "v1", "v2", map[string]any{"x": 2})
	if len(c.drifts) != 1 {
		t.Fatalf("drifts = %+v, want exactly one collision report", c.drifts)
	}
	if !strings.Contains(c.drifts[0].Detail, "both map to this corpus path") {
		t.Errorf("drift does not report the collision: %+v", c.drifts[0])
	}
}

// Re-recording has to delete what it no longer produces. Otherwise the next
// replay reports an orphan and tells the operator to re-record — which is
// exactly what they just did.
func TestCorpus_RecordPrunesGoldensItNoLongerProduces(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "gone", "v1-to-v2.yaml")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("x: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newCorpus(dir, "record")
	c.observe("kept.yaml", "v1", "v2", map[string]any{"x": 1})
	if err := c.finish(GoldenManifest{}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale golden survived a re-record (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kept", "v1-to-v2.yaml")); err != nil {
		t.Errorf("the golden this run produced was not kept: %v", err)
	}
}

// A corpus whose manifest is missing has no provenance, and reporting "no
// drift" for it would overstate what the replay proved.
func TestCorpus_ReplayReportsAnUnreadableManifest(t *testing.T) {
	dir := t.TempDir()
	c := newCorpus(dir, "golden")
	if err := c.finish(GoldenManifest{PlanHash: "sha256:abc", SchemaHash: "sha256:def"}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	found := false
	for _, d := range c.drifts {
		if d.Kind == "manifest" && strings.Contains(d.Detail, "could not be read") {
			found = true
		}
	}
	if !found {
		t.Errorf("a missing manifest produced no drift: %+v", c.drifts)
	}
}

// A golden without the destination apiVersion cannot tell a conversion to
// the wrong version from a correct one, which is one of the failures the
// corpus exists to catch.
func TestGoldenCorpus_RecordsTheDestinationAPIVersion(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "full", "golden", "hub-v3", "v3-to-v1.yaml"))
	if err != nil {
		t.Fatalf("reading a committed golden: %v", err)
	}
	if !strings.Contains(string(data), "apiVersion: example.org/v1") {
		t.Errorf("the v3→v1 golden does not record the destination apiVersion:\n%s", firstLines(string(data), 3))
	}
}

// And the stamp must not reach the object the round-trip converts back.
func TestWithDestAPIVersion_DoesNotMutateTheConvertedObject(t *testing.T) {
	converted := map[string]any{"kind": "Widget"}
	stamped := withDestAPIVersion(converted, map[string]any{"apiVersion": "example.org/v3"}, "v1")
	if _, ok := converted["apiVersion"]; ok {
		t.Error("the source object used for the round trip was mutated")
	}
	if stamped["apiVersion"] != "example.org/v1" {
		t.Errorf("apiVersion = %v, want example.org/v1", stamped["apiVersion"])
	}
	// An object with no usable apiVersion is passed through rather than
	// stamped with a group of "".
	plain := map[string]any{"kind": "Widget"}
	if got := withDestAPIVersion(plain, map[string]any{}, "v1"); got["apiVersion"] != nil {
		t.Errorf("apiVersion = %v, want it left alone when the source has no group", got["apiVersion"])
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
