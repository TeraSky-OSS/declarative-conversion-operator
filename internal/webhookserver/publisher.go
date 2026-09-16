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
	"context"
	"fmt"
	"sort"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/terasky-oss/declarative-conversion-operator/internal/servedtargets"
)

// TargetPublisher writes this replica's servable target set into its own
// Lease, so the operator can answer "is every replica of this instance
// already serving target X?" without ever calling a pod.
//
// That question is what makes a safe handover possible. When a config's
// assignment moves from one ConversionWebhookServer to another, the
// operator must not repoint the target's conversion webhook at the new
// instance until the new instance can already serve it — otherwise there
// is a window in which the apiserver sends ConversionReviews to a replica
// that has not compiled the plan yet, and every read and write of that
// resource fails.
//
// Publishing is best-effort by construction. A replica that cannot write
// its Lease still serves conversions perfectly well; what it loses is the
// ability to have work moved *onto* it safely, which the operator reports
// rather than works around.
type TargetPublisher struct {
	Client     client.Client
	Registry   *Registry
	ServerName string
	// Namespace, PodName and PodUID come from the downward API. PodUID
	// is what makes the Lease a child of this pod, so it is collected
	// with it — without an owner reference, every replaced replica would
	// leave a Lease behind claiming to serve things.
	Namespace string
	PodName   string
	PodUID    types.UID
	// Heartbeat defaults to servedtargets.Heartbeat.
	Heartbeat time.Duration

	notify chan struct{}
}

// Enabled reports whether this publisher has everything it needs,
// including PodUID. The downward-API values are absent on an older
// webhook-server Deployment the operator has not yet re-applied, and
// publishing nothing at all is better than publishing a Lease that no
// reader can attribute to a pod.
//
// PodUID is required rather than optional because it is the
// ownerReference: a Lease without one outlives the pod that wrote it,
// stops renewing, and sits in the namespace as a report from a replica
// that no longer exists. Readers do discount it after StaleAfter, but a
// Lease nothing ever collects is a leak, and during the window before it
// goes stale it is a report attributable to nobody.
func (p *TargetPublisher) Enabled() bool {
	return p != nil && p.Client != nil && p.Registry != nil &&
		p.ServerName != "" && p.Namespace != "" && p.PodName != "" && p.PodUID != ""
}

// Notify asks for an out-of-band publish, called after the registry
// changes. Non-blocking and coalescing: the channel holds one token, so a
// burst of registry writes during the initial sync produces one extra
// publish rather than hundreds.
func (p *TargetPublisher) Notify() {
	if p == nil || p.notify == nil {
		return
	}
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// Init prepares the notification channel. Called before the Reconciler
// starts so a Notify during the initial sync is not dropped on the floor.
func (p *TargetPublisher) Init() {
	if p != nil && p.notify == nil {
		p.notify = make(chan struct{}, 1)
	}
}

// Run publishes on every notification and at least once per heartbeat,
// until ctx is done. It never returns an error: a failed publish is
// logged and retried on the next tick, because the alternative — taking
// the process down over a Lease — would turn a bookkeeping problem into a
// conversion outage.
func (p *TargetPublisher) Run(ctx context.Context) {
	if !p.Enabled() {
		return
	}
	p.Init()
	logger := ctrl.LoggerFrom(ctx).WithName("served-targets")

	beat := p.Heartbeat
	if beat <= 0 {
		beat = servedtargets.Heartbeat
	}
	ticker := time.NewTicker(beat)
	defer ticker.Stop()

	publish := func() {
		if err := p.Publish(ctx); err != nil && ctx.Err() == nil {
			logger.Error(err, "unable to publish served targets; work cannot be moved onto this instance until it succeeds",
				"lease", servedtargets.LeaseName(p.PodName), "namespace", p.Namespace)
		}
	}
	publish()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			publish()
		case <-p.notify:
			publish()
		}
	}
}

// Publish upserts this replica's Lease. Exported for tests and for the
// one synchronous call cmd/webhook-server makes at startup, so a replica
// that has just finished its initial sync is immediately eligible to
// receive moved work rather than waiting out a heartbeat.
func (p *TargetPublisher) Publish(ctx context.Context) error {
	if !p.Enabled() {
		return nil
	}
	encoded, truncated := servedtargets.Encode(p.servableTargets())

	name := servedtargets.LeaseName(p.PodName)
	var lease coordinationv1.Lease
	err := p.Client.Get(ctx, types.NamespacedName{Name: name, Namespace: p.Namespace}, &lease)
	switch {
	case apierrors.IsNotFound(err):
		lease = coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace}}
		p.stamp(&lease, encoded, truncated)
		if err := p.Client.Create(ctx, &lease); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating served-targets lease: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("getting served-targets lease: %w", err)
	}

	p.stamp(&lease, encoded, truncated)
	if err := p.Client.Update(ctx, &lease); err != nil {
		return fmt.Errorf("updating served-targets lease: %w", err)
	}
	return nil
}

// stamp fills in everything the readers depend on. renewTime is refreshed
// on every publish including the ones where the target set has not
// changed — that is the whole point of the heartbeat, since a stale
// renewTime is how a reader tells a wedged replica from a working one.
func (p *TargetPublisher) stamp(lease *coordinationv1.Lease, encoded string, truncated bool) {
	if lease.Labels == nil {
		lease.Labels = map[string]string{}
	}
	lease.Labels[servedtargets.WebhookServerLabel] = p.ServerName
	lease.Labels[managedByLabel] = managedByValue

	if lease.Annotations == nil {
		lease.Annotations = map[string]string{}
	}
	if truncated {
		// Publishing a partial set would be worse than publishing none:
		// a reader cannot tell a missing name from an omitted one, and
		// would conclude a served target is unserved.
		delete(lease.Annotations, servedtargets.TargetsAnnotation)
		lease.Annotations[servedtargets.TruncatedAnnotation] = "true"
	} else {
		lease.Annotations[servedtargets.TargetsAnnotation] = encoded
		delete(lease.Annotations, servedtargets.TruncatedAnnotation)
	}

	// Enabled() guarantees PodUID, so every Lease this writes is owned by
	// the pod that wrote it and is collected with it.
	if len(lease.OwnerReferences) == 0 {
		lease.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "v1",
			Kind:       "Pod",
			Name:       p.PodName,
			UID:        p.PodUID,
		}}
	}

	holder := p.PodName
	seconds := int32(servedtargets.LeaseDuration / time.Second)
	now := metav1.NewMicroTime(time.Now())
	lease.Spec.HolderIdentity = &holder
	lease.Spec.LeaseDurationSeconds = &seconds
	lease.Spec.RenewTime = &now
	if lease.Spec.AcquireTime == nil {
		lease.Spec.AcquireTime = &now
	}
}

// servableTargets is the registry filtered to entries that can actually
// answer a ConversionReview. An error-only placeholder is in the registry
// but has no Router, so it is exactly the kind of entry that must not
// count as "this instance can serve it".
func (p *TargetPublisher) servableTargets() []string {
	snap := p.Registry.Snapshot()
	out := make([]string, 0, len(snap))
	for name, entry := range snap {
		if entry != nil && entry.Router != nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// managedByLabel/managedByValue mirror internal/controller's constants of
// the same name. Duplicated rather than imported for the same reason the
// target indexes are: cmd/webhook-server is a separate binary that should
// not link the operator's controller package.
const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "declarative-conversion-operator"
)
