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
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func objWithManagers(apiVersion string, managers ...[2]string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": "XThing"}}
	var mf []metav1.ManagedFieldsEntry
	for _, m := range managers {
		e := metav1.ManagedFieldsEntry{Manager: m[0], APIVersion: apiVersion}
		if m[1] != "" {
			ts, err := time.Parse(time.RFC3339, m[1])
			if err == nil {
				t := metav1.NewTime(ts)
				e.Time = &t
			}
		}
		mf = append(mf, e)
	}
	o.SetManagedFields(mf)
	return o
}

// LAST WRITTEN AT is the column that decides whether un-serving is safe, and
// it has to name who, not just that.
func TestCollectWriters_AggregatesByManagerAndKeepsTheLatest(t *testing.T) {
	latest := map[string]string{}
	collectWriters(objWithManagers("example.org/v1",
		[2]string{"argocd", "2026-01-01T00:00:00Z"},
		[2]string{"kubectl", "2026-03-01T00:00:00Z"},
	), latest)
	collectWriters(objWithManagers("example.org/v1",
		[2]string{"argocd", "2026-06-01T00:00:00Z"},
	), latest)

	if latest["argocd"] != "2026-06-01T00:00:00Z" {
		t.Errorf("argocd last-written = %q, want the later of the two writes", latest["argocd"])
	}
	if latest["kubectl"] != "2026-03-01T00:00:00Z" {
		t.Errorf("kubectl last-written = %q", latest["kubectl"])
	}
}

// Only managers that wrote at *this* version count: a manager that wrote the
// object at v2 says nothing about whether v1 is safe to un-serve.
func TestCollectWriters_IgnoresOtherVersions(t *testing.T) {
	o := objWithManagers("example.org/v2", [2]string{"argocd", "2026-01-01T00:00:00Z"})
	// A managedFields entry recorded against a different apiVersion.
	mf := o.GetManagedFields()
	mf = append(mf, metav1.ManagedFieldsEntry{Manager: "legacy-controller", APIVersion: "example.org/v1"})
	o.SetManagedFields(mf)

	latest := map[string]string{}
	collectWriters(o, latest)
	if _, present := latest["legacy-controller"]; present {
		t.Error("a manager that wrote a different version was counted")
	}
	if _, present := latest["argocd"]; !present {
		t.Error("the manager that wrote this version was not counted")
	}
}

func TestUnserveBlockers(t *testing.T) {
	cases := []struct {
		name     string
		row      VersionRow
		wantSafe bool
		wantSays string
	}{
		{
			name:     "clean",
			row:      VersionRow{Name: "v1", Served: true},
			wantSafe: true,
		},
		{
			name:     "still the hub",
			row:      VersionRow{Name: "v1", Served: true, Hub: true},
			wantSays: "promote another version first",
		},
		{
			name:     "still in storedVersions",
			row:      VersionRow{Name: "v1", Served: true, Stored: true},
			wantSays: "migrate-storage",
		},
		{
			// A list at a served version returns every object converted to
			// it, not the objects stored at it -- the count is identical on
			// every served row. Treating it as version-specific evidence
			// would block un-serving any spoke for as long as a single
			// object exists anywhere, which is a gate nobody can pass.
			name:     "live objects alone do not block",
			row:      VersionRow{Name: "v1", Served: true, LiveObjects: 3},
			wantSafe: true,
		},
		{
			name:     "still being written",
			row:      VersionRow{Name: "v1", Served: true, Writers: []VersionWriter{{Manager: "argocd"}}},
			wantSays: "argocd",
		},
		{
			name:     "an incomplete walk is itself a blocker",
			row:      VersionRow{Name: "v1", Served: true, Truncated: true},
			wantSays: "--max-samples",
		},
		{
			// The dangerous reading of a failed list is "zero objects, safe
			// to unserve". RBAC that denies the list has to block the gate,
			// not pass it.
			name:     "a list that failed is not an answer of zero",
			row:      VersionRow{Name: "v1", Served: true, Unreadable: `listing xthings at v1: xthings.example.org is forbidden`},
			wantSays: "a count of zero does not mean there are none",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := &VersionsReport{Resource: "xthings.example.org", Rows: []VersionRow{tc.row}}
			got := rep.UnserveBlockers("v1")
			if tc.wantSafe {
				if len(got) != 0 {
					t.Fatalf("expected safe, got blockers: %v", got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatal("expected a blocker")
			}
			if !strings.Contains(strings.Join(got, " "), tc.wantSays) {
				t.Errorf("blockers %v do not mention %q", got, tc.wantSays)
			}
		})
	}
}

func TestUnserveBlockers_UnknownVersionSaysSo(t *testing.T) {
	rep := &VersionsReport{Resource: "xthings.example.org", Rows: []VersionRow{{Name: "v1"}}}
	got := rep.UnserveBlockers("v9")
	if len(got) == 0 || !strings.Contains(got[0], "not declared") {
		t.Errorf("an undeclared version should say so: %v", got)
	}
}

func TestVersionsReport_TableHasEveryColumn(t *testing.T) {
	rep := &VersionsReport{
		Resource: "xthings.example.org", Kind: "XRD", Config: "things-conversion",
		Rows: []VersionRow{
			{Name: "v2", Served: true, Hub: true, SpokeRules: true, LiveObjects: 7, Stored: true},
			{
				Name: "v1", Served: true, Deprecated: true, DeprecationWarning: "use v2",
				SpokeRules: true, LiveObjects: 2, Stored: true,
				Writers: []VersionWriter{{Manager: "argocd", LastWrittenAt: "2026-06-01T00:00:00Z"}},
			},
		},
	}
	var buf bytes.Buffer
	rep.WriteTable(&buf)
	out := buf.String()

	for _, want := range []string{
		"VERSION", "SERVED", "HUB", "DEPRECATED", "SPOKE RULES", "LIVE OBJECTS", "STORED", "LAST WRITTEN AT",
		"xthings.example.org", "things-conversion",
		"argocd @ 2026-06-01T00:00:00Z",
		"use v2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table is missing %q:\n%s", want, out)
		}
	}
}

// A truncated walk must be visibly truncated: a small count mistaken for
// "nothing is there" is exactly how a version gets un-served too early.
func TestVersionsReport_TruncatedCountIsMarked(t *testing.T) {
	rep := &VersionsReport{
		Resource: "x", Kind: "XRD",
		Rows: []VersionRow{{Name: "v1", LiveObjects: 5000, Truncated: true}},
	}
	var buf bytes.Buffer
	rep.WriteTable(&buf)
	if !strings.Contains(buf.String(), "5000+") {
		t.Errorf("a bounded count should be marked with +:\n%s", buf.String())
	}
}

func TestDeprecationFromXRD(t *testing.T) {
	xrd := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"versions": []any{
				map[string]any{"name": "v2"},
				map[string]any{"name": "v1", "deprecated": true, "deprecationWarning": "use v2 instead"},
			},
		},
	}}
	dep, warn := deprecationFromXRD(xrd)
	if !dep["v1"] {
		t.Error("v1 should be deprecated")
	}
	if dep["v2"] {
		t.Error("v2 should not be deprecated")
	}
	if warn["v1"] != "use v2 instead" {
		t.Errorf("deprecation warning = %q", warn["v1"])
	}
}

// The count column has to distinguish "none" from "I could not look".
func TestVersionsReport_WriteTable_MarksAnUnreadableCount(t *testing.T) {
	rep := &VersionsReport{
		Resource: "xthings.example.org", Kind: "XRD",
		Rows: []VersionRow{
			{Name: "v2", Served: true, Hub: true, LiveObjects: 4},
			{Name: "v1", Served: true, Unreadable: "listing xthings at v1: forbidden"},
		},
	}
	var out bytes.Buffer
	rep.WriteTable(&out)
	got := out.String()
	if !strings.Contains(got, "0?") {
		t.Errorf("an unreadable count is not marked in the table:\n%s", got)
	}
	if !strings.Contains(got, "a floor rather than an answer") {
		t.Errorf("the table does not explain the unreadable count:\n%s", got)
	}
	if strings.Contains(got, "4?") {
		t.Errorf("a readable count was marked unreadable:\n%s", got)
	}
}
