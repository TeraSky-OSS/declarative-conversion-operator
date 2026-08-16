/*
Copyright 2026 The declarative-conversion-operator Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

    10|Unless required by applicable law or agreed to in writing, software
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
	"strings"
)

// liveTarget is one cluster --live should hit.
type liveTarget struct {
	Label      string
	Kubeconfig string
	Context    string
}

// ClusterResult is one cluster's contribution to a fleet test run.
type ClusterResult struct {
	Label  string  `json:"label"`
	Error  string  `json:"error,omitempty"`
	Report *Report `json:"report,omitempty"`
}

// FleetReport aggregates --live results across kubeconfigs/contexts.
type FleetReport struct {
	Clusters []ClusterResult `json:"clusters"`
}

func resolveLiveTargets(opts TestOptions) ([]liveTarget, error) {
	if opts.KubeconfigDir != "" && opts.Kubeconfig != "" {
		return nil, fmt.Errorf("--kubeconfig and --kubeconfig-dir are mutually exclusive")
	}
	if len(opts.Contexts) > 0 && opts.KubeContext != "" {
		return nil, fmt.Errorf("--context and --contexts are mutually exclusive")
	}

	if opts.KubeconfigDir == "" && len(opts.Contexts) == 0 {
		label := opts.KubeContext
		if label == "" {
			label = "current-context"
		}
		return []liveTarget{{
			Label:      label,
			Kubeconfig: opts.Kubeconfig,
			Context:    opts.KubeContext,
		}}, nil
	}

	var files []string
	if opts.KubeconfigDir != "" {
		listed, err := listKubeconfigFiles(opts.KubeconfigDir)
		if err != nil {
			return nil, err
		}
		if len(listed) == 0 {
			return nil, fmt.Errorf("no kubeconfig files under %s", opts.KubeconfigDir)
		}
		files = listed
	} else {
		files = []string{opts.Kubeconfig}
	}

	var out []liveTarget
	if len(opts.Contexts) == 0 {
		for _, f := range files {
			label := filepath.Base(f)
			if label == "." || label == "" {
				label = "current-context"
			}
			out = append(out, liveTarget{Label: label, Kubeconfig: f, Context: ""})
		}
		return out, nil
	}
	for _, f := range files {
		for _, ctx := range opts.Contexts {
			if ctx == "" {
				return nil, fmt.Errorf("--contexts contains an empty name")
			}
			label := ctx
			if f != "" {
				label = filepath.Base(f) + ":" + ctx
			}
			out = append(out, liveTarget{Label: label, Kubeconfig: f, Context: ctx})
		}
	}
	return out, nil
}

func listKubeconfigFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading --kubeconfig-dir %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".md") || strings.HasPrefix(lower, "readme") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out, nil
}

func (f FleetReport) WriteJUnit(w io.Writer) error {
	root := junitTestSuites{Name: "convctl fleet"}
	var totalTime float64
	for _, c := range f.Clusters {
		if c.Error != "" {
			suite := junitTestSuite{
				Name:   c.Label,
				Tests:  1,
				Errors: 1,
				Cases: []junitTestCase{{
					Name:      c.Label,
					Classname: "convctl.fleet",
					Error:     &junitMessage{Message: "cluster error", Content: c.Error},
				}},
			}
			root.Suites = append(root.Suites, suite)
			root.Tests++
			root.Errors++
			continue
		}
		if c.Report == nil {
			continue
		}
		suite := c.Report.junitSuite()
		suite.Name = c.Label + "/" + suite.Name
		for i := range suite.Cases {
			suite.Cases[i].Classname = c.Label + "." + suite.Cases[i].Classname
		}
		root.Suites = append(root.Suites, suite)
		root.Tests += suite.Tests
		root.Failures += suite.Failures
		root.Errors += suite.Errors
		var seconds float64
		_, _ = fmt.Sscanf(suite.Time, "%f", &seconds)
		totalTime += seconds
	}
	root.Time = fmt.Sprintf("%.6f", totalTime)
	return writeJUnitSuites(w, root)
}

func (f FleetReport) WriteTable(w io.Writer) {
	for _, c := range f.Clusters {
		_, _ = fmt.Fprintf(w, "=== %s ===\n", c.Label)
		if c.Error != "" {
			_, _ = fmt.Fprintf(w, "ERROR: %s\n\n", c.Error)
			continue
		}
		if c.Report != nil {
			c.Report.WriteTable(w)
			_, _ = fmt.Fprintln(w)
		}
	}
	f.WriteSummaryLine(w)
}

func (f FleetReport) decideExitCode(failOn string, strict bool) int {
	code := ExitOK
	for _, c := range f.Clusters {
		if c.Error != "" {
			return ExitTestFailure
		}
		if c.Report == nil {
			continue
		}
		if decideExitCode(c.Report, failOn, strict) != ExitOK {
			code = ExitTestFailure
		}
	}
	return code
}

func (f FleetReport) WriteSummaryLine(w io.Writer) {
	failed := 0
	for _, c := range f.Clusters {
		if c.Error != "" || (c.Report != nil && decideExitCode(c.Report, failOnLoss, false) != ExitOK) {
			failed++
		}
	}
	_, _ = fmt.Fprintf(w, "FLEET: %d clusters, %d failed\n", len(f.Clusters), failed)
}
