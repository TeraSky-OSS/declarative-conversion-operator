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

package engine

import "fmt"

// AnalyzeInput is everything Analyze needs to validate an entire
// XRDConversionConfig (one hub, N spokes) against live schemas.
type AnalyzeInput struct {
	Source     SchemaSource
	HubVersion string
	Spokes     []RuleSet
}

// Analyze validates every spoke's RuleSet against the hub and that spoke's
// schema, returning a full report. A Plan is only ever attached to a
// SpokeReport when that spoke's analysis produced zero errors — this is
// what makes "never execute an unvalidated plan" and "keep serving the
// last known-good plan on a failed re-validation" trivial for callers: a
// failing re-analysis simply never yields a replacement Plan.
func Analyze(in AnalyzeInput) (AnalyzeReport, error) {
	versions, err := in.Source.Versions()
	if err != nil {
		return AnalyzeReport{}, fmt.Errorf("analyze: reading versions: %w", err)
	}
	byName := map[string]VersionSchema{}
	for _, v := range versions {
		byName[v.Name] = v
	}
	hub, ok := byName[in.HubVersion]
	if !ok {
		return AnalyzeReport{}, fmt.Errorf("analyze: hub version %q not found among served versions", in.HubVersion)
	}
	if !hub.Storage {
		return AnalyzeReport{}, fmt.Errorf("analyze: hub version %q must be the storage/referenceable version", in.HubVersion)
	}

	report := AnalyzeReport{ResourceGeneration: in.Source.Describe().Generation}

	for _, rs := range in.Spokes {
		spoke, ok := byName[rs.SpokeVersion]
		if !ok {
			report.SpokeReports = append(report.SpokeReports, SpokeReport{
				Version: rs.SpokeVersion,
				Errors:  []Diagnostic{errorf(-1, "spoke version %q not found among the resource's versions", rs.SpokeVersion)},
			})
			continue
		}
		if !spoke.Served {
			report.SpokeReports = append(report.SpokeReports, SpokeReport{
				Version: rs.SpokeVersion,
				Errors:  []Diagnostic{errorf(-1, "spoke version %q is not served", rs.SpokeVersion)},
			})
			continue
		}

		rs.HubVersion = in.HubVersion
		h2s, s2h, results, diags, verdict := resolveAndBuildOps(rs.Rules, hub.Schema, spoke.Schema, effectivePolicy(rs.UnmappedFieldPolicy), rs.UnmappedFieldReason, 0)

		sr := SpokeReport{
			Version:     rs.SpokeVersion,
			Lossless:    verdict,
			RuleResults: results,
		}
		for _, d := range diags {
			if d.Severity == SeverityError {
				sr.Errors = append(sr.Errors, d)
				if d.FieldPath != "" {
					sr.Uncovered.UncoveredHub = append(sr.Uncovered.UncoveredHub, d.FieldPath)
				}
			} else {
				sr.Warnings = append(sr.Warnings, d)
			}
		}
		if len(sr.Errors) == 0 {
			sr.CompiledPlan = &Plan{HubVersion: in.HubVersion, SpokeVersion: rs.SpokeVersion, HubToSpoke: h2s, SpokeToHub: s2h}
		}
		report.SpokeReports = append(report.SpokeReports, sr)
	}

	return report, nil
}
