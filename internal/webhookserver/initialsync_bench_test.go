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

package webhookserver

import (
	"context"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// leafySchema is an openAPIV3Schema with leaves spec.<prefix>N string
// leaves, shaped the way pkg/engine's own compile benchmark shapes its
// fixture so the two curves are comparable.
func leafySchema(prefix string, leaves int) map[string]any {
	props := make(map[string]any, leaves)
	for i := 0; i < leaves; i++ {
		props[fmt.Sprintf("%s%d", prefix, i)] = map[string]any{"type": "string"}
	}
	return map[string]any{"openAPIV3Schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"spec": map[string]any{"type": "object", "properties": props},
		},
	}}
}

// leafyXRD is establishedXRD at an arbitrary schema size: hub v2 declares
// spec.fN, spoke v1 declares spec.gN, and the config below renames each
// pair. One rule per leaf is the common case the Phase 9 compile benchmark
// measures, so the per-target cost here is that benchmark's cost plus the
// reconcile around it.
func leafyXRD(name string, leaves int) *unstructured.Unstructured {
	xrd := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name, "generation": int64(1)},
		"spec": map[string]any{
			"scope": "Namespaced",
			"versions": []any{
				map[string]any{"name": "v2", "served": true, "referenceable": true, "schema": leafySchema("f", leaves)},
				map[string]any{"name": "v1", "served": true, "referenceable": false, "schema": leafySchema("g", leaves)},
			},
		},
		"status": map[string]any{
			"conditions": []any{map[string]any{"type": "Established", "status": "True"}},
		},
	}}
	xrd.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	return xrd
}

func leafyXRDConfig(name, targetXRD string, leaves int) *teraskyv1alpha1.XRDConversionConfig {
	rules := make([]teraskyv1alpha1.ConversionRule, leaves)
	for i := 0; i < leaves; i++ {
		rules[i] = teraskyv1alpha1.ConversionRule{
			Strategy: teraskyv1alpha1.StrategyFieldRename,
			FieldRename: &teraskyv1alpha1.FieldRenameParams{
				HubPath:   fmt.Sprintf("spec.f%d", i),
				SpokePath: fmt.Sprintf("spec.g%d", i),
			},
		}
	}
	cfg := renameRuleXRDConfig(name, targetXRD)
	cfg.Spec.Spokes[0].Rules = rules
	return cfg
}

// benchFleet is coldStartFleet without the deliberately-missing XRD: a
// cold-start benchmark should measure the work a healthy replica does, not
// the cost of a config nobody would leave broken.
func benchFleet(n, leaves int) []runtime.Object {
	server := &teraskyv1alpha1.ConversionWebhookServer{}
	server.Name = "srv"
	server.Spec.Default = true

	objs := []runtime.Object{server}
	for i := 0; i < n; i++ {
		target := fmt.Sprintf("xfoos%d.example.org", i)
		objs = append(objs, leafyXRD(target, leaves), leafyXRDConfig(fmt.Sprintf("cfg-%d", i), target, leaves))
	}
	return objs
}

// BenchmarkInitialSync is the cold-start curve: how long a replica spends
// compiling every assigned plan before it can serve, against the number of
// targets assigned to it and the size of the worker pool.
//
// The fake client stands in for an informer-backed cache, so the API-read
// term is understated relative to a real cluster and the compile term is
// the honest one. That is the right bias for this measurement: compile is
// what scales with the schemas, and it is what parallelising addresses.
// See docs/operations/capacity.md for the published numbers.
func BenchmarkInitialSync(b *testing.B) {
	// 50 leaves per version is a modest real XRD — the Phase 9 ladder runs
	// 10/100/1000 leaves for a single compile, and multiplying the top of
	// that by a thousand targets would measure the benchmark harness's
	// patience rather than anything operational.
	const leaves = 50
	for _, targets := range []int{10, 100, 1000} {
		objs := benchFleet(targets, leaves)
		for _, workers := range []int{1, 0} {
			name := fmt.Sprintf("targets=%d/workers=%d", targets, workers)
			if workers == 0 {
				name = fmt.Sprintf("targets=%d/workers=GOMAXPROCS", targets)
			}
			b.Run(name, func(b *testing.B) {
				c := newFakeClient(objs...).Build()
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r := &Reconciler{
						Client: c, ServerName: "srv", Registry: NewRegistry(),
						EnableXRDSupport: true, InitialSyncWorkers: workers,
					}
					if _, err := r.InitialSync(context.Background()); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
