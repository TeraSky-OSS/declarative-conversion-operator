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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/servedtargets"
)

// UnreportedGracePeriod is how long a move waits for the destination to
// publish anything at all before giving up on verifying it and proceeding
// unverified.
//
// Sized to be long against how quickly a working replica reports — it
// publishes on its own watch of the same objects the operator just
// changed, so a healthy fleet answers in well under a second — and short
// against how long an operator would tolerate a move stalling on a fleet
// that structurally cannot report. Thirty seconds is the same order as the
// drain period on the other side of the same move.
const UnreportedGracePeriod = 30 * time.Second

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
// The fallback when an instance publishes nothing at all is to proceed,
// but only after waiting UnreportedGracePeriod for a report that never
// comes. That ordering is the whole of it:
//
//   - Waiting first covers the case that looks identical from here and is
//     by far the more common one — the destination's replicas simply have
//     not processed the config update yet. Proceeding immediately on "no
//     reports" would repoint the target at replicas that have not compiled
//     it, which is the registry-miss outage this sequence exists to
//     prevent.
//   - Proceeding eventually covers a fleet that structurally cannot
//     report: mid-upgrade replicas running an image that predates Lease
//     publication, or an instance in a namespace whose Role was never
//     created. Blocking those forever would be a regression against every
//     previous release, which moved targets with no verification at all.
//
// The grace period is what tells the two apart without needing a
// capability flag that something would have to set correctly. A fleet that
// can report does so in well under it; a fleet that cannot never will, and
// says so in the condition.
func canServeTarget(served []string, reporting, readyReplicas int32, truncated bool, target, serverName string, blockedFor time.Duration) HandoverVerdict {
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
	case reporting == 0 && blockedFor < UnreportedGracePeriod:
		return HandoverVerdict{
			Reason: "HandoverAwaitingReports",
			Message: fmt.Sprintf("no replica of ConversionWebhookServer %q has published its served targets yet, so whether it can serve %q is unknown; "+
				"waiting up to %s for a report before moving the target", serverName, target, UnreportedGracePeriod),
		}
	case reporting == 0:
		return HandoverVerdict{
			OK:     true,
			Reason: "HandoverUnverified",
			Message: fmt.Sprintf("no replica of ConversionWebhookServer %q published its served targets within %s, so the handover of %q could not be verified; "+
				"proceeding as earlier releases did. Check that the webhook-server replicas are up to date and permitted to write Leases in their namespace",
				serverName, UnreportedGracePeriod, target),
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
func checkHandover(ctx context.Context, c client.Client, server *teraskyv1alpha1.ConversionWebhookServer, defaultNamespace, target string, conditions []metav1.Condition, now time.Time) (HandoverVerdict, error) {
	served, reporting, truncated, err := readServedTargets(ctx, c, server, defaultNamespace)
	if err != nil {
		return HandoverVerdict{}, err
	}
	return canServeTarget(served, reporting, server.Status.ReadyReplicas, truncated, target, server.Name, blockedFor(conditions, now)), nil
}

// blockedFor is how long this handover has already been refused.
//
// Read from the HandoverReady condition rather than from a status field of
// its own: the condition is already False for exactly as long as the move
// is blocked, and meta.SetStatusCondition only restamps LastTransitionTime
// when the status changes — so the timestamp survives the reason moving
// between waiting states, which is what "how long has this been blocked"
// should measure. A True or absent condition means this is the first
// refusal, and the clock starts now.
func blockedFor(conditions []metav1.Condition, now time.Time) time.Duration {
	c := meta.FindStatusCondition(conditions, teraskyv1alpha1.ConditionHandoverReady)
	if c == nil || c.Status != metav1.ConditionFalse || c.LastTransitionTime.IsZero() {
		return 0
	}
	if d := now.Sub(c.LastTransitionTime.Time); d > 0 {
		return d
	}
	return 0
}
