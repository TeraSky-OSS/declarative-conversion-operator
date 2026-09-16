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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ReportSchemaVersion is bumped when a field changes meaning. The nightly
// workflow compares a run against the previous run's artifact, and
// comparing two reports written to different schemas would produce a
// confident, wrong regression verdict — so the comparison refuses when the
// versions differ.
const ReportSchemaVersion = 1

// Report is the machine-readable form of a Result: the scale run's
// numbers, shaped for a scheduled job to publish as an artifact, render in
// a job summary, and diff against the previous run.
//
// Durations are milliseconds as floats rather than Go duration strings,
// because everything downstream — the regression check, a spreadsheet,
// anything plotting a trend — wants a number.
type Report struct {
	SchemaVersion int    `json:"schemaVersion"`
	RecordedAt    string `json:"recordedAt"`

	Targets      int `json:"targets"`
	Instances    int `json:"instances"`
	TotalObjects int `json:"totalObjects"`

	CreateMs float64 `json:"createMs"`

	// Measurements is keyed by operation so the regression check can walk
	// it without knowing the operation names, and so adding one later does
	// not break a comparison against an older artifact.
	Measurements map[string]Measurement `json:"measurements"`

	// StrategyCoverage records how many of the fleet's conversions used
	// each strategy. A run whose coverage collapsed is measuring something
	// other than what the previous run measured.
	StrategyCoverage map[string]int `json:"strategyCoverage,omitempty"`

	// Observed is filled in by the harness around this package — peak
	// memory and cold-start time come from the cluster, not from the
	// client driving it. Absent when the harness did not collect them.
	Observed map[string]float64 `json:"observed,omitempty"`
}

// Measurement is one operation class's latency and error count.
type Measurement struct {
	N      int     `json:"n"`
	Errors int     `json:"errors"`
	P50Ms  float64 `json:"p50Ms"`
	P99Ms  float64 `json:"p99Ms"`
	MaxMs  float64 `json:"maxMs"`
	// ThroughputPerSecond is n divided by the wall-clock the operation
	// class took, so a run that got slower shows up here even if the
	// percentiles are noisy.
	ThroughputPerSecond float64 `json:"throughputPerSecond"`
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// ToReport converts a Result into its published form.
func (r *Result) ToReport(now time.Time) *Report {
	rep := &Report{
		SchemaVersion: ReportSchemaVersion,
		RecordedAt:    now.UTC().Format(time.RFC3339),
		Targets:       r.Targets,
		Instances:     r.Instances,
		TotalObjects:  r.Targets * r.Instances,
		CreateMs:      ms(r.Create),
		Measurements: map[string]Measurement{
			"listV1": measurement(r.ListV1),
			"listV2": measurement(r.ListV2),
			"getV1":  measurement(r.GetV1),
			"getV2":  measurement(r.GetV2),
		},
	}
	if len(r.Coverage) > 0 {
		rep.StrategyCoverage = make(map[string]int, len(r.Coverage))
		for strategy, n := range r.Coverage {
			rep.StrategyCoverage[string(strategy)] = n
		}
	}
	return rep
}

func measurement(s Stats) Measurement {
	m := Measurement{N: s.N, Errors: s.Errors, P50Ms: ms(s.P50), P99Ms: ms(s.P99), MaxMs: ms(s.Max)}
	// Throughput from the median rather than from a wall-clock the Stats
	// does not carry: with P workers each taking p50, the fleet rate is
	// P/p50. The worker count is not known here, so this is per-worker
	// throughput — comparable between runs at the same parallelism, which
	// is what the regression check uses it for.
	if s.P50 > 0 {
		m.ThroughputPerSecond = float64(time.Second) / float64(s.P50)
	}
	return m
}

// WriteReport writes the report to path, creating parent directories.
// Deliberately indented: these files are read by humans during an incident
// at least as often as by the comparison script.
func WriteReport(path string, rep *Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("creating the result directory: %w", err)
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the scale report: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// MeasurementNames returns the report's operation keys in a stable order,
// so a rendered summary does not reshuffle its rows between runs.
func (r *Report) MeasurementNames() []string {
	out := make([]string, 0, len(r.Measurements))
	for name := range r.Measurements {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
