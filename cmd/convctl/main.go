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

// Command convctl is a thin bootstrap around internal/cli — kept
// minimal so all real logic stays testable as a library, per Go CLI
// convention.
package main

import (
	"os"

	"github.com/terasky-oss/declarative-conversion-operator/internal/cli"
)

// Overridden at build time via -ldflags "-X main.version=..." and friends.
// Absent those — a `go install` build — internal/cli falls back to the build
// information the Go toolchain embeds, so the binary still reports something
// that maps to a commit.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	cli.Version = version
	cli.Commit = commit
	cli.Date = date
	os.Exit(cli.Execute())
}
