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
	"reflect"
	"sort"
	"strings"
)

// diffLeaves recursively compares two unstructured trees and returns the
// dotted paths of every leaf whose value differs (present-vs-absent counts
// as a difference too). apiVersion is deliberately ignored — it's expected
// to differ by design, that's the whole point of a version conversion.
func diffLeaves(a, b map[string]any) []string {
	var out []string
	diffInto("", any(a), any(b), &out)
	sort.Strings(out)
	return out
}

func diffInto(path string, a, b any, out *[]string) {
	if isIgnoredPath(path) {
		return
	}
	am, aIsMap := a.(map[string]any)
	bm, bIsMap := b.(map[string]any)
	if aIsMap && bIsMap {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for k := range keys {
			diffInto(joinPath(path, k), am[k], bm[k], out)
		}
		return
	}
	aArr, aIsArr := a.([]any)
	bArr, bIsArr := b.([]any)
	if aIsArr && bIsArr && len(aArr) == len(bArr) {
		for i := range aArr {
			diffInto(fmt.Sprintf("%s[%d]", path, i), aArr[i], bArr[i], out)
		}
		return
	}
	if !reflect.DeepEqual(a, b) {
		*out = append(*out, path)
	}
}

func joinPath(prefix, seg string) string {
	if prefix == "" {
		return seg
	}
	return prefix + "." + seg
}

// isIgnoredPath excludes fields whose divergence is expected and
// meaningless for lossy-conversion reporting: apiVersion (always rewritten
// by design) and metadata.resourceVersion/uid/creationTimestamp, which a
// sample file loaded from disk never has populated realistically anyway.
func isIgnoredPath(path string) bool {
	switch path {
	case "apiVersion", "kind":
		return true
	case "metadata.resourceVersion", "metadata.uid", "metadata.creationTimestamp", "metadata.generation":
		return true
	}
	return false
}

// pathMatchesAny reports whether path (or one of its ancestor paths, so a
// rule that claims a whole object subtree also covers its nested leaves)
// appears in the given set of dotted paths considered lossy.
func pathMatchesAny(path string, set map[string]bool) bool {
	if len(set) == 0 {
		return false
	}
	segs := strings.Split(path, ".")
	for i := len(segs); i > 0; i-- {
		if set[strings.Join(segs[:i], ".")] {
			return true
		}
	}
	return false
}

// countLeaves counts scalar leaves in an unstructured tree, used for the
// report's "fields converted" column.
func countLeaves(v any) int {
	switch t := v.(type) {
	case map[string]any:
		n := 0
		for _, vv := range t {
			n += countLeaves(vv)
		}
		return n
	case []any:
		n := 0
		for _, vv := range t {
			n += countLeaves(vv)
		}
		return n
	default:
		return 1
	}
}
