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

// Package chart holds tests about the Helm chart that are better expressed
// in Go than in a helm-unittest suite — principally the structural
// agreement between values.yaml and values.schema.json, which is a property
// of the two files rather than of any rendered template.
//
// Template behaviour lives in charts/declarative-conversion-operator/tests/
// and runs under `make helm-test`.
package chart
