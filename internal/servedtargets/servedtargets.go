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

// Package servedtargets is the wire format for the one piece of state
// that flows from webhook-server replicas back to the operator: which
// targets each replica can actually serve right now.
//
// The operator's reconcile loop deliberately makes no network calls to
// webhook-server pods (see docs/limitations.md), so this cannot be a
// query. Instead each replica publishes its own registry contents into a
// coordination.k8s.io Lease, and the operator reads those the same way it
// reads anything else — through an informer.
//
// A Lease rather than a ConfigMap or the Pod's own annotations:
//
//   - It is owned by the Pod, so it is garbage-collected with it and
//     needs no reaping of its own.
//   - renewTime is a first-class staleness signal, which is exactly the
//     backstop needed for a pod that is alive but wedged — the case
//     ownership cannot cover.
//   - It is a small, dedicated object, so a 30-second heartbeat is not
//     rewriting something other controllers watch.
//
// Both the publisher (internal/webhookserver) and the readers
// (internal/controller) live on this package so the encoding is defined
// once.
package servedtargets

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
)

const (
	// WebhookServerLabel carries the ConversionWebhookServer name a
	// published Lease belongs to. It is what scopes the operator's Lease
	// informer — without a label there is nothing to select on, and the
	// cache would hold every Lease in the cluster, which on any cluster
	// means one per node.
	WebhookServerLabel = "conversion.terasky.com/webhook-server"

	// TargetsAnnotation holds the encoded set of targets this replica has
	// a compiled, servable plan for.
	TargetsAnnotation = "conversion.terasky.com/served-targets"

	// TruncatedAnnotation is "true" when the replica's target set did not
	// fit in MaxEncodedBytes. Readers must treat a truncated Lease as "I
	// cannot tell you what I serve" rather than as a partial answer —
	// silently reading a truncated list as complete would report a target
	// as unserved and, worse, could report one as served that is not in
	// the part that survived.
	TruncatedAnnotation = "conversion.terasky.com/served-targets-truncated"

	// LeaseNamePrefix distinguishes these Leases from leader-election
	// ones sharing the namespace.
	LeaseNamePrefix = "dco-served-"

	// MaxEncodedBytes caps the annotation. Kubernetes limits an object's
	// total annotations to 256 KiB; this leaves room for the rest.
	// Encoded, a thousand typical target names come to a few kilobytes,
	// so the cap is reached somewhere north of fifty thousand targets on
	// one replica — far outside any envelope this project claims, which
	// is why exceeding it degrades to "unknown" rather than to a more
	// elaborate chunking scheme.
	MaxEncodedBytes = 192 * 1024

	// Heartbeat is how often a replica renews its Lease, and LeaseDuration
	// is what it advertises as the validity window. A reader treats a
	// Lease whose renewTime is older than StaleAfter as gone.
	Heartbeat     = 30 * time.Second
	LeaseDuration = 90 * time.Second
	StaleAfter    = 3 * Heartbeat
)

// LeaseName is the Lease a given replica publishes to. Keyed by pod name,
// which is unique within a namespace, so two instances sharing a
// namespace cannot collide.
func LeaseName(podName string) string { return LeaseNamePrefix + podName }

// Encode packs a target set into an annotation value: sorted, joined,
// gzipped and base64'd. Sorted so an unchanged set encodes to an
// unchanged string and the publisher can skip the write; gzipped because
// target names are long, highly similar strings and compress by roughly
// four to one.
//
// The second return is true when the result exceeded MaxEncodedBytes, in
// which case the value is empty and the caller must set TruncatedAnnotation
// instead of publishing a partial set.
func Encode(targets []string) (string, bool) {
	sorted := append([]string(nil), targets...)
	sort.Strings(sorted)

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	// gzip.Writer only fails if the underlying writer does, and
	// bytes.Buffer does not.
	_, _ = zw.Write([]byte(strings.Join(sorted, "\n")))
	_ = zw.Close()

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	if len(encoded) > MaxEncodedBytes {
		return "", true
	}
	return encoded, false
}

// Decode reverses Encode. An empty value decodes to an empty set, which
// is what a replica serving nothing publishes.
func Decode(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decoding served-targets annotation: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decompressing served-targets annotation: %w", err)
	}
	defer func() { _ = zr.Close() }()
	// Bounded by the same cap the writer honours: an annotation is
	// attacker-influenced only by whoever can already write Leases in the
	// namespace, but an unbounded decompress is not something to leave in
	// a reconcile loop regardless.
	out, err := io.ReadAll(io.LimitReader(zr, MaxEncodedBytes*8))
	if err != nil {
		return nil, fmt.Errorf("reading served-targets annotation: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return strings.Split(string(out), "\n"), nil
}

// Aggregate reduces one instance's replica Leases to what the instance as
// a whole can serve.
//
// Served is the INTERSECTION across live replicas, not the union: a
// target that two replicas out of three can serve is a target that fails
// one request in three, so it is not something the instance serves.
// Reporting is how many live Leases contributed. Truncated is true if any
// live replica could not state its set, in which case Served is not a
// complete answer and callers must not treat an absence as a negative.
//
// A Lease is live if its renewTime is within StaleAfter of now. Leases are
// owned by their Pod and normally vanish with it; the staleness check is
// the backstop for a pod that is running but no longer publishing.
func Aggregate(leases []coordinationv1.Lease, now time.Time) (served []string, reporting int32, truncated bool) {
	// Counts are int32 to match reporting: a count can never exceed the
	// number of live leases, which is a replica count.
	var counts map[string]int32
	for i := range leases {
		l := &leases[i]
		if l.Spec.RenewTime == nil || now.Sub(l.Spec.RenewTime.Time) > StaleAfter {
			continue
		}
		reporting++
		if l.Annotations[TruncatedAnnotation] == "true" {
			truncated = true
			continue
		}
		names, err := Decode(l.Annotations[TargetsAnnotation])
		if err != nil {
			// An undecodable Lease is not evidence of anything. Treating
			// it as truncated makes callers fall back to "cannot tell",
			// which is the fail-closed reading.
			truncated = true
			continue
		}
		if counts == nil {
			counts = map[string]int32{}
		}
		for _, n := range names {
			counts[n]++
		}
	}
	if truncated || reporting == 0 {
		return nil, reporting, truncated
	}
	for name, n := range counts {
		if n == reporting {
			served = append(served, name)
		}
	}
	sort.Strings(served)
	return served, reporting, false
}

// Contains reports whether name is in a sorted set, as returned by
// Aggregate or read back from status.servedTargets.
func Contains(sortedSet []string, name string) bool {
	i := sort.SearchStrings(sortedSet, name)
	return i < len(sortedSet) && sortedSet[i] == name
}
