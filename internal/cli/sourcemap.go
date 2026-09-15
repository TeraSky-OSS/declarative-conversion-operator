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
	"fmt"
	"os"

	yaml "go.yaml.in/yaml/v3"
)

// SourceLocation is where something in a report came from in the file the
// user actually edits.
//
// A finding without one is still a finding — it renders against the file
// with no line — but a finding WITH one lands on the diff in a code review
// rather than in a log nobody opens.
type SourceLocation struct {
	File   string `json:"file"`
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
}

// Known reports whether the location is usable as a file:line reference.
func (l SourceLocation) Known() bool { return l.File != "" && l.Line > 0 }

func (l SourceLocation) String() string {
	if !l.Known() {
		return l.File
	}
	return fmt.Sprintf("%s:%d", l.File, l.Line)
}

// ConfigSourceMap maps a conversion config's structure back to the lines it
// was written on.
//
// sigs.k8s.io/yaml routes through encoding/json, which is what makes the
// strict typed decode elsewhere possible and also what throws positions
// away. This is a second, position-preserving parse of the same bytes: it
// decodes nothing and validates nothing, so the typed loader stays the only
// thing that decides whether a config is valid.
type ConfigSourceMap struct {
	File string
	// doc is the whole document, used as the fallback location so a
	// finding that cannot be attributed more precisely still points
	// somewhere real.
	doc    SourceLocation
	hub    SourceLocation
	spokes map[string]SourceLocation
	rules  map[string]map[int]SourceLocation
}

// SourceMapForConfig parses path for positions. A parse failure is not an
// error the caller has to handle: the typed loader has already accepted
// these bytes, and a report with no line numbers is worth more than no
// report, so the zero map (which answers every lookup with the file alone)
// is returned instead.
func SourceMapForConfig(path string) *ConfigSourceMap {
	m := &ConfigSourceMap{
		File:   path,
		doc:    SourceLocation{File: path, Line: 1, Column: 1},
		spokes: map[string]SourceLocation{},
		rules:  map[string]map[int]SourceLocation{},
	}
	// #nosec G304 -- the same path the typed loader was just given.
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return m
	}
	m.index(&root)
	return m
}

func (m *ConfigSourceMap) index(root *yaml.Node) {
	doc := documentRoot(root)
	if doc == nil {
		return
	}
	spec := mappingValue(doc, "spec")
	if spec == nil {
		return
	}
	if hub := mappingValue(spec, "hubVersion"); hub != nil {
		m.hub = m.at(hub)
	}
	spokes := mappingValue(spec, "spokes")
	if spokes == nil || spokes.Kind != yaml.SequenceNode {
		return
	}
	for _, entry := range spokes.Content {
		version := ""
		if v := mappingValue(entry, "version"); v != nil {
			version = v.Value
		}
		if version == "" {
			continue
		}
		m.spokes[version] = m.at(entry)
		rules := mappingValue(entry, "rules")
		if rules == nil || rules.Kind != yaml.SequenceNode {
			continue
		}
		byIndex := map[int]SourceLocation{}
		for i, rule := range rules.Content {
			byIndex[i] = m.at(rule)
		}
		m.rules[version] = byIndex
	}
}

func (m *ConfigSourceMap) at(n *yaml.Node) SourceLocation {
	return SourceLocation{File: m.File, Line: n.Line, Column: n.Column}
}

// Document is the location to use when nothing more specific is known.
func (m *ConfigSourceMap) Document() SourceLocation {
	if m == nil {
		return SourceLocation{}
	}
	return m.doc
}

// HubVersion is where spec.hubVersion is declared.
func (m *ConfigSourceMap) HubVersion() SourceLocation {
	if m == nil || !m.hub.Known() {
		return m.Document()
	}
	return m.hub
}

// Spoke is where a spoke's entry begins.
func (m *ConfigSourceMap) Spoke(version string) SourceLocation {
	if m == nil {
		return SourceLocation{}
	}
	if l, ok := m.spokes[version]; ok {
		return l
	}
	return m.doc
}

// Rule is where one rule of one spoke is declared. Falls back to the spoke,
// then to the document, so the answer is always somewhere the reader can
// open rather than nowhere.
func (m *ConfigSourceMap) Rule(version string, index int) SourceLocation {
	if m == nil {
		return SourceLocation{}
	}
	if byIndex, ok := m.rules[version]; ok {
		if l, ok := byIndex[index]; ok {
			return l
		}
	}
	return m.Spoke(version)
}

// documentRoot unwraps the document node YAML parsing wraps everything in.
func documentRoot(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil
		}
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	return n
}

// mappingValue returns the value node for key in a mapping node. Mapping
// content is a flat key, value, key, value sequence.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
