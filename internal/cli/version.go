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
	"runtime/debug"
)

// Build metadata, set by -ldflags on a release build. Commit and Date are
// filled from the embedded build info when they are not, which is what makes
// `go install` produce something better than three empty strings.
var (
	// Commit is the git revision the binary was built from.
	Commit = ""
	// Date is the build date, RFC3339.
	Date = ""
)

// VersionInfo is everything `convctl version -o json` reports.
//
// The fields exist because they are what a bug report needs: "convctl says
// the conversion is lossy" is unactionable without knowing which convctl.
type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// versionInfo assembles the build metadata, falling back to the information
// the Go toolchain embeds in every module-built binary.
//
// A `go install ...@latest` build has no ldflags, and reporting "dev" for it
// is how a bug report arrives with a version nobody can map to a commit.
// debug.ReadBuildInfo knows the module version and the VCS stamps, so use
// them rather than a placeholder.
func versionInfo() VersionInfo {
	info := VersionInfo{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if info.Version == "" || info.Version == "dev" {
		// Main.Version is "(devel)" for a local build and the module
		// version for `go install module@version`.
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			info.Version = v
		}
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if info.Commit == "" {
				info.Commit = s.Value
			}
		case "vcs.time":
			if info.Date == "" {
				info.Date = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" && info.Commit != "" {
				info.Commit += "-dirty"
			}
		}
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	return info
}

// String is the one-line form the bare `convctl version` prints.
func (v VersionInfo) String() string {
	out := v.Version
	if v.Commit != "" {
		out += " (" + shortCommit(v.Commit) + ")"
	}
	return out + " " + v.Platform + " " + v.GoVersion
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
