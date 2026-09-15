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

package chart

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const chartDir = "../../charts/declarative-conversion-operator"

func readValues(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(chartDir, "values.yaml"))
	if err != nil {
		t.Fatalf("reading values.yaml: %v", err)
	}
	var v map[string]any
	if err := yaml.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parsing values.yaml: %v", err)
	}
	return v
}

func readSchema(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(chartDir, "values.schema.json"))
	if err != nil {
		t.Fatalf("reading values.schema.json: %v", err)
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("parsing values.schema.json: %v", err)
	}
	return s
}

// resolve follows a local $ref. The schema uses them for the shapes that
// genuinely repeat (resources, certificate, podDisruptionBudget); without
// following them the walk below would stop at the reference and report
// every value underneath as missing.
func resolve(schema, node map[string]any) map[string]any {
	ref, ok := node["$ref"].(string)
	if !ok {
		return node
	}
	const prefix = "#/definitions/"
	if !strings.HasPrefix(ref, prefix) {
		return node
	}
	defs, ok := schema["definitions"].(map[string]any)
	if !ok {
		return node
	}
	target, ok := defs[strings.TrimPrefix(ref, prefix)].(map[string]any)
	if !ok {
		return node
	}
	return target
}

// TestEveryValueIsInTheSchema walks values.yaml and values.schema.json
// together.
//
// The schema's whole purpose is that a key not described by it is rejected.
// That makes an *undescribed* key in values.yaml a shipped chart that fails
// its own `helm install` — which the default-values render would catch — and
// makes a key described only in values.yaml and not the schema a documented
// knob nobody can set. Both are silent until somebody hits them.
func TestEveryValueIsInTheSchema(t *testing.T) {
	schema := readSchema(t)
	values := readValues(t)

	var missing []string

	var walk func(path string, vals map[string]any, node map[string]any)
	walk = func(path string, vals map[string]any, node map[string]any) {
		node = resolve(schema, node)
		props, _ := node["properties"].(map[string]any)

		// A node with no `properties` is deliberately free-form (podLabels,
		// nodeSelector, extraEnv, an arbitrary affinity). Nothing below it
		// is expected to be described, and descending would produce
		// nonsense.
		if props == nil {
			return
		}
		for key, val := range vals {
			full := key
			if path != "" {
				full = path + "." + key
			}
			sub, described := props[key].(map[string]any)
			if !described {
				missing = append(missing, full)
				continue
			}
			if child, isMap := val.(map[string]any); isMap {
				walk(full, child, sub)
			}
		}
	}
	walk("", values, schema)

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("values.yaml declares %d key(s) the schema does not describe, so `helm install` with the shipped defaults would be rejected:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// The inverse: a schema property with no corresponding entry in values.yaml
// is a knob that exists but is undocumented, which is how a value ends up
// being discovered from the source.
//
// Deliberate exceptions are listed rather than inferred, so adding one is a
// decision somebody made on purpose.
func TestEverySchemaPropertyIsInValues(t *testing.T) {
	schema := readSchema(t)
	values := readValues(t)

	// Optional by design: no sensible default exists, and rendering one
	// would change behaviour rather than document it.
	allowed := map[string]bool{
		// minAvailable is the documented default; the two are mutually
		// exclusive, so shipping both would be a values file that does not
		// install.
		"conversionWebhookServer.podDisruptionBudget.maxUnavailable": true,
		"manager.podDisruptionBudget.maxUnavailable":                 true,
		// Derived from the Service name when unset.
		"conversionWebhookServer.certificate.dnsNames": true,
		"admissionWebhook.certificate.dnsNames":        true,
		// corev1.ResourceRequirements.claims, accepted for completeness so
		// the pass-through is honest. Nothing in this chart uses it.
		"conversionWebhookServer.resources.claims": true,
		"manager.resources.claims":                 true,
	}

	var undocumented []string

	var walk func(path string, node map[string]any, vals map[string]any)
	walk = func(path string, node map[string]any, vals map[string]any) {
		node = resolve(schema, node)
		props, _ := node["properties"].(map[string]any)
		if props == nil {
			return
		}
		for key, rawSub := range props {
			sub, ok := rawSub.(map[string]any)
			if !ok {
				continue
			}
			full := key
			if path != "" {
				full = path + "." + key
			}
			val, present := vals[key]
			if !present {
				if !allowed[full] {
					undocumented = append(undocumented, full)
				}
				continue
			}
			child, isMap := val.(map[string]any)
			if !isMap {
				continue
			}
			// An empty map in values.yaml (`issuerRef: {}`, `cacheSelector:
			// {}`) documents the key and says its contents are deliberately
			// unset. Requiring each sub-key to appear would mean shipping
			// `issuerRef: {name: "", kind: ""}`, which reads as a
			// configured-but-blank issuer rather than an absent one.
			if len(child) == 0 {
				continue
			}
			walk(full, sub, child)
		}
	}
	walk("", schema, values)

	if len(undocumented) > 0 {
		sort.Strings(undocumented)
		t.Errorf("the schema describes %d key(s) values.yaml never mentions; add them to values.yaml with their default, or to the exception list above with a reason:\n  %s",
			len(undocumented), strings.Join(undocumented, "\n  "))
	}
}

// The free-form maps have to stay open, or a perfectly ordinary
// `--set manager.podLabels.team=platform` starts failing.
func TestFreeFormMapsStayOpen(t *testing.T) {
	schema := readSchema(t)

	openPaths := []string{
		"commonLabels",
		"manager.podLabels",
		"manager.podAnnotations",
		"manager.nodeSelector",
		"manager.affinity",
		"manager.image",
		"conversionWebhookServer.podLabels",
		"conversionWebhookServer.podAnnotations",
		"conversionWebhookServer.nodeSelector",
		"conversionWebhookServer.affinity",
		"serviceAccount.annotations",
		"conversionWebhookServer.service.annotations",
		"metrics.serviceMonitor.labels",
		"metrics.prometheusRule.labels",
		"dashboards.labels",
	}

	for _, p := range openPaths {
		t.Run(p, func(t *testing.T) {
			node := schema
			for _, seg := range strings.Split(p, ".") {
				node = resolve(schema, node)
				props, ok := node["properties"].(map[string]any)
				if !ok {
					t.Fatalf("no properties at %q while resolving %s", seg, p)
				}
				next, ok := props[seg].(map[string]any)
				if !ok {
					t.Fatalf("schema has no property %q while resolving %s", seg, p)
				}
				node = next
			}
			node = resolve(schema, node)
			// Closed means either additionalProperties:false or an explicit
			// properties list. Either one rejects an arbitrary user key.
			if ap, ok := node["additionalProperties"].(bool); ok && !ap {
				t.Errorf("%s is closed; an arbitrary key set by a user would be rejected", p)
			}
			if _, hasProps := node["properties"]; hasProps {
				t.Errorf("%s enumerates its properties, so it is not free-form", p)
			}
		})
	}
}
