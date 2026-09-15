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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const testPackage = "testdata/package/platform.xpkg"

// The fixture is a real `crossplane xpkg build` output, so this asserts the
// reader agrees with the format Crossplane actually produces rather than
// with the assumptions the reader was written from.
func TestReadPackage_ReadsARealXPKG(t *testing.T) {
	pkg, err := ReadPackage(testPackage)
	if err != nil {
		t.Fatalf("reading the package: %v", err)
	}
	if pkg.Meta == nil {
		t.Error("the package meta object was not found")
	} else if got := pkg.Meta.GetKind(); got != "Configuration" {
		t.Errorf("meta kind = %q, want Configuration", got)
	}
	names := pkg.XRDNames()
	if len(names) != 2 {
		t.Fatalf("XRDs = %v, want the fixture's two", names)
	}
	if names[0] != "xbuckets.example.org" || names[1] != "xwidgets.example.org" {
		t.Errorf("XRD names = %v", names)
	}
	// The XRDs have to arrive usable, not just counted.
	for _, x := range pkg.XRDs {
		if _, found, _ := unstructured.NestedSlice(x.Object, "spec", "versions"); !found {
			t.Errorf("%s has no spec.versions, so it did not survive the round trip", xrdName(x))
		}
	}
}

// Picking the first of several would make the answer depend on the order the
// package happened to be built in.
func TestPackageContents_SelectXRDRequiresATargetWhenAmbiguous(t *testing.T) {
	pkg, err := ReadPackage(testPackage)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pkg.SelectXRD("")
	if err == nil {
		t.Fatal("a package with two XRDs selected one without being told which")
	}
	for _, want := range []string{"--target", "xwidgets.example.org", "xbuckets.example.org"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name the candidates, got: %v", err)
		}
	}

	got, err := pkg.SelectXRD("xwidgets.example.org")
	if err != nil {
		t.Fatalf("selecting by name: %v", err)
	}
	if xrdName(got) != "xwidgets.example.org" {
		t.Errorf("selected %s", xrdName(got))
	}

	_, err = pkg.SelectXRD("xnope.example.org")
	if err == nil || !strings.Contains(err.Error(), "ships no XRD named") {
		t.Errorf("an unknown target should say so and list what is there: %v", err)
	}
}

// A single-XRD package needs no --target: that is the common case and
// requiring a flag for it would be friction with no purpose.
func TestPackageContents_SelectXRDIsUnambiguousWithOne(t *testing.T) {
	one, err := ReadPackage(testPackage)
	if err != nil {
		t.Fatal(err)
	}
	single := &PackageContents{Ref: "single.xpkg", XRDs: one.XRDs[:1]}
	got, err := single.SelectXRD("")
	if err != nil {
		t.Fatalf("a single-XRD package still demanded a target: %v", err)
	}
	if got == nil {
		t.Error("no XRD returned")
	}
}

// The three failures have to be distinguishable, because the fix for each is
// different: find the file, build a package, add an XRD to it.
func TestReadPackage_DistinguishesItsFailures(t *testing.T) {
	if _, err := ReadPackage("testdata/package/nope.xpkg"); err == nil || !strings.Contains(err.Error(), "reading package") {
		t.Errorf("a missing file should say so: %v", err)
	}
	if _, err := ReadPackage("testdata"); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("a directory should say so: %v", err)
	}

	dir := t.TempDir()
	notAPackage := filepath.Join(dir, "plain.xpkg")
	if err := os.WriteFile(notAPackage, []byte("this is not a tar at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackage(notAPackage); err == nil {
		t.Error("a non-tar file was accepted as a package")
	}
}

// A package with no XRDs is not the same failure as a file that is not a
// package, and a config cannot be checked against either.
func TestPackageContents_NoXRDsSaysWhatIsThere(t *testing.T) {
	empty := &PackageContents{Ref: "p.xpkg", Others: 4}
	_, err := empty.SelectXRD("")
	if err == nil || !strings.Contains(err.Error(), "no CompositeResourceDefinitions") {
		t.Errorf("want a message naming the absence: %v", err)
	}
	if !strings.Contains(err.Error(), "4 other object(s)") {
		t.Errorf("want the count of what the package does contain: %v", err)
	}
}

// The registry and cluster forms are named rather than silently
// unsupported, and the error says what to do instead.
func TestReadPackage_NamesTheUnimplementedForms(t *testing.T) {
	for ref, want := range map[string]string{
		"ghcr.io/org/platform:v1.4.0":        "crossplane xpkg pull",
		"configuration/platform":             "not implemented yet",
		"configurationrevision/platform-abc": "not implemented yet",
	} {
		_, err := ReadPackage(ref)
		if err == nil {
			t.Errorf("%s was accepted", ref)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %q does not contain %q", ref, err, want)
		}
	}
}

// A path must not be mistaken for a registry reference, or a local package
// would be rejected with advice about pulling it.
func TestLooksLikeImageRef(t *testing.T) {
	for ref, want := range map[string]bool{
		"ghcr.io/org/platform:v1": true,
		"localhost:5000/p:v1":     true,
		"./platform.xpkg":         false,
		"/abs/platform.xpkg":      false,
		"platform.xpkg":           false, // a bare filename, not a registry
		testPackage:               false,
	} {
		if got := looksLikeImageRef(ref); got != want {
			t.Errorf("looksLikeImageRef(%q) = %v, want %v", ref, got, want)
		}
	}
}

// --package has to slot in exactly where --xrd does, which is the whole
// reason it is a schema source rather than a new verb.
func TestXRDFromSource_PackageAndFileAreInterchangeable(t *testing.T) {
	fromPkg, err := XRDFromSource("", testPackage, "xwidgets.example.org")
	if err != nil {
		t.Fatalf("from package: %v", err)
	}
	fromFile, err := XRDFromSource("../../examples/crossplane-xr-multiversion/03-promote-v2/xrd.yaml", "", "")
	if err != nil {
		t.Fatalf("from file: %v", err)
	}
	if xrdName(fromPkg) != xrdName(fromFile) {
		t.Errorf("package gave %s, file gave %s", xrdName(fromPkg), xrdName(fromFile))
	}
}

// The composition that matters: schemas from the package about to be
// installed, checked by the same analysis a file would get.
func TestRunAnalyzeFrom_WorksAgainstAPackage(t *testing.T) {
	out, err := RunAnalyzeFrom("", "", "../../examples/crossplane-xr-multiversion/03-promote-v2/xrdconversionconfig.yaml", testPackage, "xwidgets.example.org")
	if err != nil {
		t.Fatalf("analyze from package: %v", err)
	}
	if out.Resource != "xwidgets.example.org" {
		t.Errorf("resource = %q", out.Resource)
	}
	if len(out.Spokes) == 0 {
		t.Error("no spokes analyzed")
	}
}

func TestRunTest_WorksAgainstAPackage(t *testing.T) {
	rep, err := RunTest(TestOptions{
		PackagePath:   testPackage,
		PackageTarget: "xwidgets.example.org",
		ConfigPath:    "../../examples/crossplane-xr-multiversion/03-promote-v2/xrdconversionconfig.yaml",
		SamplesDir:    "../../examples/crossplane-xr-multiversion/03-promote-v2/samples",
		Quiet:         true,
	})
	if err != nil {
		t.Fatalf("test from package: %v", err)
	}
	if rep.Summary.Samples == 0 {
		t.Error("no samples tested")
	}
	if rep.Summary.Errors != 0 {
		t.Errorf("errors = %d, want a clean run", rep.Summary.Errors)
	}
}

// A whole package against a whole config tree, one command, is the
// Configuration repository's gate.
func TestRunLint_PairsATreeAgainstAPackage(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "conversion.yaml"),
		mustRead(t, "../../examples/crossplane-xr-multiversion/03-promote-v2/xrdconversionconfig.yaml"))

	rep, err := RunLint(LintOptions{Paths: []string{dir}, PackageRef: testPackage})
	if err != nil {
		t.Fatalf("lint against a package: %v", err)
	}
	if rep.Unpaired != 0 {
		t.Fatalf("a config was not paired against the package's XRDs: %+v", rep.Results)
	}
	if !strings.Contains(rep.Results[0].Schema, "platform.xpkg") {
		t.Errorf("schema should name the package, not a temporary path: %q", rep.Results[0].Schema)
	}
}

// A package is a file pulled from a registry or handed over by a third
// party, so the names inside it are untrusted. An XRD called ../../evil must
// not decide where this process writes.
func TestStagePackageXRDs_CannotEscapeTheStagingDirectory(t *testing.T) {
	pkg, err := ReadPackage(testPackage)
	if err != nil {
		t.Fatal(err)
	}
	// A target inside the test's own directory, so a failure cannot touch
	// anything else on the machine and two runs cannot collide.
	escape := filepath.Join(t.TempDir(), "escaped")
	hostile := "../../../../" + strings.TrimPrefix(escape, "/")
	pkg.XRDs[0].SetName(hostile)
	pkg.XRDs[1].SetName(hostile)

	staged, cleanup, err := stagePackageXRDsFrom(pkg, "hostile.xpkg")
	if err != nil {
		t.Fatalf("staging: %v", err)
	}
	defer cleanup()

	if len(staged) != 2 {
		t.Fatalf("staged %d files, want both XRDs kept distinct despite the same name", len(staged))
	}
	seen := map[string]bool{}
	for _, d := range staged {
		abs, err := filepath.Abs(d.path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(abs, escape) {
			t.Errorf("staged file escaped to %s", abs)
		}
		if !strings.Contains(abs, "convctl-package-") {
			t.Errorf("staged file %s is outside the staging directory", abs)
		}
		if seen[abs] {
			t.Errorf("two XRDs staged to the same path %s, so one overwrote the other", abs)
		}
		seen[abs] = true
		if _, err := os.Stat(d.path); err != nil {
			t.Errorf("staged file is not readable: %v", err)
		}
	}
	if _, err := os.Stat(escape + ".yaml"); err == nil {
		t.Errorf("a file was written outside the staging directory, at %s.yaml", escape)
	}
}
