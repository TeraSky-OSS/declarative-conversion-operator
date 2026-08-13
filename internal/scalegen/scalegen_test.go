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

package scalegen

import (
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	v1a "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/crdadapter"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

func TestSlotsCount(t *testing.T) {
	t.Parallel()
	if n := len(slots()); n != 29 {
		t.Fatalf("catalog has %d strategies, want 29", n)
	}
}

func TestAssign_CoversAllStrategies(t *testing.T) {
	t.Parallel()
	v1, v2, err := Assign(4, 3, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[v1a.Strategy]int{}
	for i := range v1 {
		if n := len(v1[i]); n < 3 || n > 10 {
			t.Fatalf("v1[%d] has %d strategies, want 3–10", i, n)
		}
		if n := len(v2[i]); n < 3 || n > 10 {
			t.Fatalf("v2[%d] has %d strategies, want 3–10", i, n)
		}
		for _, s := range v1[i] {
			seen[s.Name]++
		}
		for _, s := range v2[i] {
			seen[s.Name]++
		}
	}
	for _, s := range slots() {
		if seen[s.Name] == 0 {
			t.Errorf("strategy %s never assigned", s.Name)
		}
	}
}

func TestAssign_TooSmallToCover(t *testing.T) {
	t.Parallel()
	_, _, err := Assign(1, 3, 10, 1)
	if err == nil {
		t.Fatal("expected error when 2*targets*max < 29")
	}
}

func TestBuildTargets_AnalyzeAndConvert(t *testing.T) {
	t.Parallel()
	targets, err := BuildTargets(4, 3, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if cov := StrategyCoverage(targets); len(cov) != 29 {
		t.Fatalf("coverage has %d strategies, want 29", len(cov))
	}
	for _, tgt := range targets {
		tgt := tgt
		t.Run(tgt.CRDName, func(t *testing.T) {
			t.Parallel()
			ruleSets, err := tgt.Config.ToRuleSets()
			if err != nil {
				t.Fatal(err)
			}
			report, err := engine.Analyze(engine.AnalyzeInput{
				Source: crdadapter.New(tgt.CRD), HubVersion: HubVersion, Spokes: ruleSets,
			})
			if err != nil {
				t.Fatal(err)
			}
			if report.HasErrors() {
				t.Fatalf("Analyze errors: %+v", report.SpokeReports)
			}
			obj := tgt.Instance("dco-scale", 0).Object
			for _, rs := range ruleSets {
				if rs.SpokeVersion != V1 {
					continue
				}
				hub := versionSchema(t, tgt, HubVersion)
				spoke := versionSchema(t, tgt, V1)
				plan, diags, err := engine.Compile(rs, hub, spoke)
				if err != nil {
					t.Fatal(err)
				}
				for _, d := range diags {
					if d.Severity == engine.SeverityError {
						t.Fatalf("compile: %s", d.Message)
					}
				}
				hubObj, err := engine.Convert(engine.ConvertInput{Plan: plan, Direction: engine.SpokeToHub, Object: obj})
				if err != nil {
					t.Fatalf("spoke→hub: %v", err)
				}
				if _, err := engine.Convert(engine.ConvertInput{Plan: plan, Direction: engine.HubToSpoke, Object: hubObj}); err != nil {
					t.Fatalf("hub→spoke: %v", err)
				}
			}
		})
	}
}

func TestRun_DryRun(t *testing.T) {
	t.Parallel()
	res, err := Run(t.Context(), Options{Targets: 4, Instances: 5, DryRun: true, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Targets != 4 || len(res.Coverage) != 29 {
		t.Fatalf("targets=%d coverage=%d, want 4 and 29", res.Targets, len(res.Coverage))
	}
}

func versionSchema(t *testing.T, tgt Target, name string) *extv1.JSONSchemaProps {
	t.Helper()
	for _, v := range tgt.CRD.Spec.Versions {
		if v.Name == name && v.Schema != nil {
			return v.Schema.OpenAPIV3Schema
		}
	}
	t.Fatalf("no schema for %s", name)
	return nil
}
