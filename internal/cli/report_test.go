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
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
)

func TestReport_WriteJUnit(t *testing.T) {
	rep, err := RunTest(TestOptions{
		XRDPath:    "testdata/xrd.yaml",
		ConfigPath: "testdata/config.yaml",
		SamplesDir: "testdata/samples",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	if err := rep.WriteJUnit(&buf); err != nil {
		t.Fatalf("WriteJUnit: %v", err)
	}

	var parsed junitTestSuites
	if err := xml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("output is not well-formed JUnit XML: %v\n%s", err, buf.String())
	}
	if parsed.Tests != rep.Summary.PathsTested {
		t.Fatalf("expected %d testcases (one per path tested), got %d", rep.Summary.PathsTested, parsed.Tests)
	}
	if parsed.Failures != 0 {
		t.Fatalf("expected 0 <failure> entries (the only lossy rule is acknowledged), got %d", parsed.Failures)
	}
	if len(parsed.Suites) != 1 {
		t.Fatalf("expected exactly 1 testsuite, got %d", len(parsed.Suites))
	}
	if !strings.Contains(buf.String(), "<?xml") {
		t.Fatalf("expected an XML declaration header, got: %s", buf.String())
	}
}

func TestReport_WriteSummaryLine(t *testing.T) {
	rep, err := RunTest(TestOptions{
		XRDPath:    "testdata/xrd.yaml",
		ConfigPath: "testdata/config.yaml",
		SamplesDir: "testdata/samples",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	rep.WriteSummaryLine(&buf)
	if !strings.HasPrefix(buf.String(), "SUMMARY:") {
		t.Fatalf("expected the summary line to start with SUMMARY:, got: %s", buf.String())
	}
	if strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("expected exactly one line, got: %q", buf.String())
	}
}
