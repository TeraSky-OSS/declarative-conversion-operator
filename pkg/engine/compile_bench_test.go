/*
Copyright 2026 The declarative-conversion-operator Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

    10|Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package engine

import (
	"fmt"
	"testing"
)

func BenchmarkCompile_LeafCount(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("leaves=%d", n), func(b *testing.B) {
			hub := nLeafSchema(n)
			spoke := nLeafSchema(n)
			rs := RuleSet{HubVersion: "v2", SpokeVersion: "v1", Rules: nRenameRules(n)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := Compile(rs, &hub, &spoke); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
