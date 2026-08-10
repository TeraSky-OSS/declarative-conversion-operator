/*
Copyright 2026 The xrd-conversion-operator Authors.

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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	teraskyv1alpha1 "github.com/vrabbi/xrd-conversion-operator/api/v1alpha1"
	"github.com/vrabbi/xrd-conversion-operator/pkg/engine"
	"github.com/vrabbi/xrd-conversion-operator/pkg/xrdadapter"
)

// runAnalyze runs the exact same engine.Analyze code path the controller
// and admission webhook use, against a locally loaded XRD and config.
func runAnalyze(xrd *unstructured.Unstructured, cfg *teraskyv1alpha1.XRDConversionConfig) (engine.AnalyzeReport, []engine.VersionSchema, error) {
	source := xrdadapter.New(xrd)
	versions, err := source.Versions()
	if err != nil {
		return engine.AnalyzeReport{}, nil, fmt.Errorf("reading XRD versions: %w", err)
	}
	ruleSets, err := cfg.ToRuleSets()
	if err != nil {
		return engine.AnalyzeReport{}, versions, fmt.Errorf("invalid rule configuration: %w", err)
	}
	report, err := engine.Analyze(engine.AnalyzeInput{Source: source, HubVersion: cfg.Spec.HubVersion, Spokes: ruleSets})
	if err != nil {
		return engine.AnalyzeReport{}, versions, fmt.Errorf("analysis failed: %w", err)
	}
	return report, versions, nil
}

// buildRouter assembles a Router from every spoke's compiled plan. Returns
// an error naming the first spoke that failed to compile — a Router built
// from a partially-valid report would silently mis-serve untested paths.
func buildRouter(cfg *teraskyv1alpha1.XRDConversionConfig, report engine.AnalyzeReport) (*engine.Router, error) {
	plans := map[string]*engine.Plan{}
	for _, sr := range report.SpokeReports {
		if sr.CompiledPlan == nil {
			return nil, fmt.Errorf("spoke %q failed validation, cannot test conversions for it: %d error(s)", sr.Version, len(sr.Errors))
		}
		plans[sr.Version] = sr.CompiledPlan
	}
	return &engine.Router{Hub: cfg.Spec.HubVersion, Plans: plans}, nil
}

func servedVersions(versions []engine.VersionSchema) []string {
	var out []string
	for _, v := range versions {
		if v.Served {
			out = append(out, v.Name)
		}
	}
	return out
}

func summarizeSpokeErrors(report engine.AnalyzeReport) string {
	msg := ""
	for _, sr := range report.SpokeReports {
		for _, e := range sr.Errors {
			msg += fmt.Sprintf("\n  [spoke %s] %s", sr.Version, e.Message)
		}
	}
	return msg
}

func xrdName(xrd *unstructured.Unstructured) string {
	if n := xrd.GetName(); n != "" {
		return n
	}
	return "(unnamed)"
}
