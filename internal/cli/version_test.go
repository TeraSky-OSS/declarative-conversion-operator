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
	"runtime"
	"strings"
	"testing"
)

// A bug report needs to say which convctl, and a release build has all of it
// from ldflags.
func TestVersionInfo_UsesLdflagsWhenPresent(t *testing.T) {
	origV, origC, origD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = origV, origC, origD })

	Version, Commit, Date = "v1.2.3", "abcdef1234567890", "2026-01-02T03:04:05Z"
	got := versionInfo()
	if got.Version != "v1.2.3" || got.Commit != "abcdef1234567890" || got.Date != "2026-01-02T03:04:05Z" {
		t.Errorf("ldflags were not used: %+v", got)
	}
	if got.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q", got.GoVersion)
	}
	if got.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("Platform = %q", got.Platform)
	}
}

// A `go install` build has no ldflags. Reporting "dev" for it is how a bug
// report arrives with a version nobody can map to a commit — the toolchain
// embeds the module version and VCS stamps, so use them.
func TestVersionInfo_FallsBackToEmbeddedBuildInfo(t *testing.T) {
	origV, origC, origD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = origV, origC, origD })

	Version, Commit, Date = "dev", "", ""
	got := versionInfo()
	if got.Version == "" {
		t.Error("version is empty, which is worse than dev")
	}
	// Under `go test` the binary carries build settings, so at minimum the
	// fallback must not regress what ldflags would have given.
	if got.Version == "dev" && got.Commit != "" {
		t.Errorf("a commit was found but the version stayed dev: %+v", got)
	}
}

// The one-line form is what a human reads first; it has to identify the
// build without being a paragraph.
func TestVersionInfo_StringIsOneUsefulLine(t *testing.T) {
	v := VersionInfo{Version: "v1.2.3", Commit: "abcdef1234567890abcdef", GoVersion: "go1.26.6", Platform: "linux/amd64"}
	got := v.String()
	if strings.Contains(got, "\n") {
		t.Errorf("version line spans lines: %q", got)
	}
	for _, want := range []string{"v1.2.3", "abcdef123456", "linux/amd64", "go1.26.6"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing %q", got, want)
		}
	}
	if strings.Contains(got, "abcdef1234567890abcdef") {
		t.Errorf("the full commit was printed rather than a short one: %q", got)
	}
}

func TestVersionInfo_StringWithoutACommit(t *testing.T) {
	v := VersionInfo{Version: "dev", GoVersion: "go1.26.6", Platform: "darwin/arm64"}
	if got := v.String(); !strings.HasPrefix(got, "dev ") || strings.Contains(got, "()") {
		t.Errorf("empty commit rendered badly: %q", got)
	}
}
