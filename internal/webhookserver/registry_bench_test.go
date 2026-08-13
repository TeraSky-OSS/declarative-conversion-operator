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

package webhookserver

import (
	"fmt"
	"testing"
)

func primedRegistry(n int) *Registry {
	r := NewRegistry()
	for i := 0; i < n; i++ {
		r.Set(fmt.Sprintf("target-%d.example.org", i), &CompiledEntry{PlanHash: "v1"})
	}
	return r
}

func BenchmarkRegistrySet(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			r := primedRegistry(n)
			entry := &CompiledEntry{PlanHash: "hot"}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r.Set("target-0.example.org", entry)
			}
		})
	}
}

func BenchmarkRegistryGet(b *testing.B) {
	r := primedRegistry(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := r.Get("target-0.example.org"); !ok {
			b.Fatal("missing")
		}
	}
}

func BenchmarkRegistryGetParallel(b *testing.B) {
	r := primedRegistry(1000)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r.Get("target-0.example.org")
		}
	})
}
