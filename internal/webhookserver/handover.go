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
	"time"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

// DefaultTargetDrainPeriod is how long a replica keeps serving a target
// after that target has stopped naming it.
//
// It exists for the same reason the pod's preStop sleep does, one layer
// up. The apiserver refreshes a CRD's conversion configuration
// asynchronously after the write that changed it, so for a short window
// after the operator repoints a target the apiserver is still calling the
// old Service. Dropping the plan the instant the object changes answers
// those calls with a 503 — and the apiserver reports that as a failed read
// or write on a resource that was merely being rebalanced.
//
// This is not hypothetical: hack/e2e-reassign.sh caught exactly one failed
// write in 9,456 during three reassignments before this existed, with the
// registry-miss message. One in ten thousand is small and it is not zero,
// and a rebalance is not supposed to cost anything.
//
// Thirty seconds is chosen the way the preStop sleep is: comfortably
// longer than the propagation it waits out, and cheap to be wrong about in
// the safe direction. The only cost of an over-long drain is that a
// replica holds one compiled plan — about 18 KiB — a little longer than it
// needs to.
const DefaultTargetDrainPeriod = 30 * time.Second

// A replica serves a target when EITHER of two things is true: the shared
// resolver assigns the target to this instance, or the target's live
// spec.conversion still points its webhook at this instance's Service.
//
// The second clause is what makes a handover safe from the losing end.
// When a target moves from instance A to instance B — an edited
// webhookServerRef, or sharding rebalancing after an instance is added —
// A's assignment changes the instant the object does, but the target's
// spec.conversion still names A until the operator gets round to patching
// it. Dropping the plan on the assignment change alone leaves that window
// with a webhook configured to call A and an A that answers "unknown
// target". Holding it until the target stops naming this instance closes
// the window from A's side, exactly as the operator's handover gate closes
// it from B's.
//
// Matching on the Service name alone, not on namespace: instance names are
// cluster-unique and the Service name is derived from them, so the name is
// already unambiguous — and a replica does not otherwise need to know
// which namespace its own Service lives in.

// xrdPointsAtServer reports whether an XRD's spec.conversion webhook still
// names this instance's Service. Unstructured because Crossplane's XRD
// type is not vendored here; a missing or differently-shaped
// spec.conversion simply reads as "no".
func xrdPointsAtServer(xrd *unstructured.Unstructured, serverName string) bool {
	if xrd == nil {
		return false
	}
	name, found, err := unstructured.NestedString(xrd.Object, "spec", "conversion", "webhook", "clientConfig", "service", "name")
	if err != nil || !found {
		return false
	}
	return name == teraskyv1alpha1.WebhookServerServiceName(serverName)
}

// crdPointsAtServer is xrdPointsAtServer for a native CRD.
func crdPointsAtServer(crd *extv1.CustomResourceDefinition, serverName string) bool {
	if crd == nil || crd.Spec.Conversion == nil || crd.Spec.Conversion.Webhook == nil {
		return false
	}
	svc := crd.Spec.Conversion.Webhook.ClientConfig
	if svc == nil || svc.Service == nil {
		return false
	}
	return svc.Service.Name == teraskyv1alpha1.WebhookServerServiceName(serverName)
}
