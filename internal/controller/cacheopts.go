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
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ManagedByLabel and ManagedByValue are the label every child object this
// operator creates carries. They are what makes the owned-workload
// informers scopable: without an object-level label there is nothing for
// the cache to select on, and controller-runtime would fall back to
// watching every Deployment, Service, HPA and PDB in the cluster.
const (
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "declarative-conversion-operator"
)

// OwnedWorkloadSelector selects only the child objects this operator
// manages.
func OwnedWorkloadSelector() labels.Selector {
	req, err := labels.NewRequirement(ManagedByLabel, selection.Equals, []string{ManagedByValue})
	if err != nil {
		// Unreachable: the key and value are compile-time constants and
		// both are valid label syntax. Returning Nothing() rather than
		// panicking keeps a hypothetical future typo from taking the
		// process down at startup — it would instead cache nothing, which
		// surfaces loudly and immediately in the reconcile logs.
		return labels.Nothing()
	}
	return labels.NewSelector().Add(*req)
}

// ManagerCacheOptions scopes the manager's informers.
//
// The default is a cluster-wide informer per watched type, which for this
// operator means holding every Deployment, Service, HorizontalPodAutoscaler
// and PodDisruptionBudget in the cluster resident in memory for the sake of
// the handful it actually owns. Each owned type is therefore label-scoped
// to this operator's own children.
//
// Secrets are handled separately, by ManagerClientOptions: they are read,
// never watched, so the right answer is to not cache them at all.
func ManagerCacheOptions() cache.Options {
	owned := cache.ByObject{Label: OwnedWorkloadSelector()}
	return cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&appsv1.Deployment{}:                     owned,
			&corev1.Service{}:                        owned,
			&autoscalingv2.HorizontalPodAutoscaler{}: owned,
			&policyv1.PodDisruptionBudget{}:          owned,
		},
	}
}

// ManagerClientOptions keeps Secrets out of the cache entirely.
//
// The operator reads exactly one key (ca.crt, or tls.crt as a fallback)
// out of one Secret per ConversionWebhookServer, on the cold path of an
// apply. Caching that costs a cluster-wide Secret informer — typically the
// single largest object class in a cluster by total bytes — to save a
// handful of GETs per reconcile, and it is what made the broad Secret grant
// in docs/security/rbac.md alarming: the grant has to stay broad because
// the namespace is not known ahead of time, but the process no longer holds
// the contents.
//
// Reading through to the API server also removes the cache-staleness window
// on certificate rotation, so this is a correctness improvement as well as
// a memory one.
func ManagerClientOptions() client.Options {
	return client.Options{
		Cache: &client.CacheOptions{
			DisableFor: []client.Object{&corev1.Secret{}},
		},
	}
}

// CacheScopeDescription is a one-line, human-readable summary of the above,
// logged at startup so an operator can confirm what is and is not held in
// memory without reading the source.
func CacheScopeDescription() string {
	return "Secrets: uncached (read-through to the API server); " +
		"Deployments/Services/HorizontalPodAutoscalers/PodDisruptionBudgets: " +
		ManagedByLabel + "=" + ManagedByValue + "; " +
		"XRDConversionConfigs/CRDConversionConfigs/ConversionWebhookServers/CRDs/XRDs: cluster-wide"
}
