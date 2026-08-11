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

import "testing"

func TestStripArrayIndices(t *testing.T) {
	cases := map[string]string{
		"spec.zones[0].name":    "spec.zones.name",
		"spec.zones[12].name":   "spec.zones.name",
		"spec.list[0][1].value": "spec.list.value",
		"spec.plain":            "spec.plain",
		"":                      "",
	}
	for in, want := range cases {
		if got := stripArrayIndices(in); got != want {
			t.Errorf("stripArrayIndices(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPathMatchesAny_ArrayIndex guards against a regression where a diff
// path for an array element (e.g. "spec.zones[0].name", which diffLeaves
// produces when comparing array-shaped fields) failed to match a rule's
// declared ancestor path ("spec.zones") because the literal "[0]" segment
// never equaled the bracket-free declared path — meaning any array-typed
// field a rule legitimately declared lossy could never actually be
// classified as "acknowledged", always showing up as an unacknowledged
// failure instead.
func TestPathMatchesAny_ArrayIndex(t *testing.T) {
	lossy := map[string]bool{"spec.zones": true}
	if !pathMatchesAny("spec.zones[0].name", lossy) {
		t.Fatalf("expected spec.zones[0].name to match declared lossy ancestor spec.zones")
	}
	if pathMatchesAny("spec.other[0].name", lossy) {
		t.Fatalf("expected spec.other[0].name not to match an unrelated declared lossy path")
	}
}
