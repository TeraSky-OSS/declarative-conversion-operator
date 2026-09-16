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
	"context"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/servedtargets"
)

// readServedTargets aggregates one instance's replica Leases into "what
// can every live replica of this instance actually serve right now".
//
// See internal/servedtargets for why this is published rather than
// queried, and why the aggregate is an intersection.
func readServedTargets(ctx context.Context, c client.Client, server *teraskyv1alpha1.ConversionWebhookServer, defaultNamespace string) (served []string, reporting int32, truncated bool, err error) {
	ns := server.Spec.Namespace
	if ns == "" {
		ns = defaultNamespace
	}
	var leases coordinationv1.LeaseList
	if err := c.List(ctx, &leases,
		client.InNamespace(ns),
		client.MatchingLabels{servedtargets.WebhookServerLabel: server.Name},
	); err != nil {
		return nil, 0, false, fmt.Errorf("listing served-target leases for %q: %w", server.Name, err)
	}
	served, reporting, truncated = servedtargets.Aggregate(leases.Items, time.Now())
	return served, reporting, truncated, nil
}

// HandoverVerdict answers the one question a move has to ask: may the
// operator repoint this target's conversion webhook at this instance yet?
type HandoverVerdict struct {
	// OK is true when the move may proceed.
	OK bool
	// Reason is a condition Reason; Message explains it.
	Reason, Message string
}

// canServeTarget decides whether it is safe to hand target over to
// server.
//
// The safe sequence is: the new instance compiles the plan (which it does
// on its own, from the same watch events the operator sees) → it publishes
// that it can serve the target → only then does the operator patch the
// target's spec.conversion to point at it. Until that patch lands, the
// target still names the *old* instance, and the old instance keeps
// serving it precisely because it is still named — see
// webhookserver.Reconciler, which holds a plan for as long as either the
// assignment or the live target points at it.
//
// The fallback when an instance publishes nothing at all is to proceed.
// That is a deliberate compatibility choice, not an oversight: a fleet
// mid-upgrade, or an instance in a namespace whose Role was never
// created, would otherwise have every move blocked forever. Proceeding
// restores exactly the behaviour every previous release had, and the
// caller reports it as a warning rather than passing it off as a verified
// handover.
func canServeTarget(served []string, reporting, readyReplicas int32, truncated bool, target, serverName string) HandoverVerdict {
	switch {
	case readyReplicas == 0:
		// Reachable only by bypassing the ConversionWebhookServer health
		// gate that runs before this, but "no ready replica" must never
		// read as "nothing objects": repointing a target at an instance
		// with nothing running is the outage this whole sequence exists
		// to avoid.
		return HandoverVerdict{
			Reason:  "HandoverPending",
			Message: fmt.Sprintf("ConversionWebhookServer %q has no ready replicas, so it cannot serve %q; the target stays on its current server", serverName, target),
		}
	case reporting == 0:
		return HandoverVerdict{
			OK:     true,
			Reason: "HandoverUnverified",
			Message: fmt.Sprintf("no replica of ConversionWebhookServer %q publishes its served targets, so the handover of %q could not be verified; "+
				"proceeding as earlier releases did. Check that the webhook-server replicas are up to date and permitted to write Leases in their namespace", serverName, target),
		}
	case truncated:
		return HandoverVerdict{
			Reason: "HandoverUnknown",
			Message: fmt.Sprintf("a replica of ConversionWebhookServer %q could not report its served targets, so whether it can serve %q is unknown; "+
				"the target stays on its current server", serverName, target),
		}
	case reporting < readyReplicas:
		return HandoverVerdict{
			Reason: "HandoverPending",
			Message: fmt.Sprintf("%d of %d ready replicas of ConversionWebhookServer %q have reported their served targets; waiting before repointing %q",
				reporting, readyReplicas, serverName, target),
		}
	case !servedtargets.Contains(served, target):
		return HandoverVerdict{
			Reason: "HandoverPending",
			Message: fmt.Sprintf("ConversionWebhookServer %q is not yet serving %q on all %d reporting replicas; the target stays on its current server until it is",
				serverName, target, reporting),
		}
	}
	return HandoverVerdict{
		OK:      true,
		Reason:  "HandoverReady",
		Message: fmt.Sprintf("all %d reporting replicas of ConversionWebhookServer %q already serve %q", reporting, serverName, target),
	}
}

// checkHandover is the call site's convenience wrapper: read the Leases,
// then judge. A List failure is returned as an error so the reconcile
// retries, rather than being silently read as "not ready" — which would
// stall a move on a transient API blip.
func checkHandover(ctx context.Context, c client.Client, server *teraskyv1alpha1.ConversionWebhookServer, defaultNamespace, target string) (HandoverVerdict, error) {
	served, reporting, truncated, err := readServedTargets(ctx, c, server, defaultNamespace)
	if err != nil {
		return HandoverVerdict{}, err
	}
	return canServeTarget(served, reporting, server.Status.ReadyReplicas, truncated, target, server.Name), nil
}
