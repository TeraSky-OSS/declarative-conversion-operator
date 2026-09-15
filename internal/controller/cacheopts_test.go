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

package controller

import (
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// The selector and the labels the reconciler actually stamps are two
// halves of one decision. If they drift, the controller stops seeing its
// own children — an informer that matches nothing looks exactly like a
// cluster with nothing in it, so nothing fails loudly.
func TestOwnedWorkloadSelector_MatchesWhatWeStamp(t *testing.T) {
	sel := OwnedWorkloadSelector()
	if sel.Empty() {
		t.Fatal("an empty selector matches everything, which is the bug this exists to prevent")
	}
	if !sel.Matches(labels.Set(podLabels("srv"))) {
		t.Errorf("selector %q does not match the labels reconcileDeployment stamps: %v", sel, podLabels("srv"))
	}
	if sel.Matches(labels.Set{"app.kubernetes.io/managed-by": "Helm"}) {
		t.Error("selector matches somebody else's Deployment")
	}
	if sel.Matches(labels.Set{}) {
		t.Error("selector matches an unlabelled object")
	}
}

func TestManagerCacheOptions_ScopesEveryOwnedType(t *testing.T) {
	opts := ManagerCacheOptions()

	// Exactly the types ConversionWebhookServerReconciler.SetupWithManager
	// passes to Owns(). Adding an Owns() without adding it here silently
	// reintroduces a cluster-wide informer.
	want := []string{
		fmt.Sprintf("%T", &appsv1.Deployment{}),
		fmt.Sprintf("%T", &corev1.Service{}),
		fmt.Sprintf("%T", &autoscalingv2.HorizontalPodAutoscaler{}),
		fmt.Sprintf("%T", &policyv1.PodDisruptionBudget{}),
	}
	got := map[string]bool{}
	for obj, by := range opts.ByObject {
		key := fmt.Sprintf("%T", obj)
		got[key] = true
		if by.Label == nil || by.Label.Empty() {
			t.Errorf("%s is in ByObject but is not label-scoped, so it caches the whole cluster", key)
		}
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("%s is owned by a controller but its informer is unscoped", w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("ByObject has %d entries, expected %d: %v", len(got), len(want), got)
	}
}

// A Secret entry in ByObject would establish a Secret informer — scoped,
// but still an informer. The whole point is that there is not one.
func TestManagerCacheOptions_DoesNotWatchSecrets(t *testing.T) {
	for obj := range ManagerCacheOptions().ByObject {
		if _, isSecret := obj.(*corev1.Secret); isSecret {
			t.Fatal("Secrets must not appear in the cache options at all; they are read through the API server")
		}
	}
}

func TestManagerClientOptions_DisablesTheSecretCache(t *testing.T) {
	opts := ManagerClientOptions()
	if opts.Cache == nil {
		t.Fatal("client cache options unset, so Secrets are cached by default")
	}
	found := false
	for _, obj := range opts.Cache.DisableFor {
		if _, isSecret := obj.(*corev1.Secret); isSecret {
			found = true
		}
	}
	if !found {
		t.Errorf("Secret is not in DisableFor: %v", opts.Cache.DisableFor)
	}
}

// The startup log line is the only way an operator can confirm any of this
// on a running process, so it has to actually say the thing.
func TestCacheScopeDescription_NamesTheSecretDecision(t *testing.T) {
	desc := CacheScopeDescription()
	for _, want := range []string{"Secrets", "uncached", ManagedByLabel, ManagedByValue} {
		if !strings.Contains(desc, want) {
			t.Errorf("description does not mention %q: %s", want, desc)
		}
	}
}
