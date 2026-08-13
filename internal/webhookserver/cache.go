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
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

// CacheOptionsFromSelectorJSON builds controller-runtime cache options that
// restrict XRDConversionConfig and CRDConversionConfig informers to objects
// matching the given metav1.LabelSelector (JSON). An empty string leaves
// the cache unscoped (watch everything), matching today's default.
func CacheOptionsFromSelectorJSON(selJSON string) (cache.Options, error) {
	opts := cache.Options{}
	if selJSON == "" {
		return opts, nil
	}
	var ls metav1.LabelSelector
	if err := json.Unmarshal([]byte(selJSON), &ls); err != nil {
		return opts, fmt.Errorf("parse --cache-label-selector: %w", err)
	}
	if len(ls.MatchLabels) == 0 && len(ls.MatchExpressions) == 0 {
		return opts, nil
	}
	selector, err := metav1.LabelSelectorAsSelector(&ls)
	if err != nil {
		return opts, fmt.Errorf("compile --cache-label-selector: %w", err)
	}
	by := cache.ByObject{Label: selector}
	opts.ByObject = map[client.Object]cache.ByObject{
		&teraskyv1alpha1.XRDConversionConfig{}: by,
		&teraskyv1alpha1.CRDConversionConfig{}: by,
	}
	return opts, nil
}

// CountMatchingLabels reports how many label maps match sel. Used to show
// that a cacheSelector shrinks the set an informer would hold versus the
// unscoped (every object matches) default.
func CountMatchingLabels(all []labels.Set, sel labels.Selector) int {
	if sel == nil || sel.Empty() {
		return len(all)
	}
	n := 0
	for _, set := range all {
		if sel.Matches(set) {
			n++
		}
	}
	return n
}
