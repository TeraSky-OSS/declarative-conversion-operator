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

package servedtargets

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEncodeDecode_RoundTrips(t *testing.T) {
	in := []string{"zfoos.example.org", "afoos.example.org", "mfoos.example.org"}
	value, truncated := Encode(in)
	if truncated {
		t.Fatal("three names should not truncate")
	}
	got, err := Decode(value)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []string{"afoos.example.org", "mfoos.example.org", "zfoos.example.org"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %v, want %v (sorted)", got, want)
	}
}

// The publisher skips the write when the encoded value is unchanged, which
// only holds if the encoding does not depend on map iteration order.
func TestEncode_IsStableAcrossInputOrder(t *testing.T) {
	a, _ := Encode([]string{"a", "b", "c"})
	b, _ := Encode([]string{"c", "a", "b"})
	if a != b {
		t.Fatal("the same set in a different order encoded differently; every heartbeat would look like a change")
	}
}

func TestEncodeDecode_EmptySet(t *testing.T) {
	value, truncated := Encode(nil)
	if truncated {
		t.Fatal("the empty set should not truncate")
	}
	got, err := Decode(value)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("decoded %v, want nothing — a replica serving nothing is a legitimate state", got)
	}
}

func TestDecode_RejectsGarbage(t *testing.T) {
	if _, err := Decode("not base64 at all !!!"); err == nil {
		t.Fatal("expected an error on a value that is not base64")
	}
	if _, err := Decode("aGVsbG8="); err == nil {
		t.Fatal("expected an error on base64 that is not gzip")
	}
}

// lease builds a Lease renewed `age` ago. A negative age puts renewTime in
// the future, which is how the clock-skew cases are expressed.
func lease(name string, age time.Duration, targets []string) coordinationv1.Lease {
	value, truncated := Encode(targets)
	renew := metav1.NewMicroTime(time.Now().Add(-age))
	l := coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{}},
		Spec:       coordinationv1.LeaseSpec{RenewTime: &renew},
	}
	if truncated {
		l.Annotations[TruncatedAnnotation] = "true"
	} else {
		l.Annotations[TargetsAnnotation] = value
	}
	return l
}

// The aggregate is an intersection, not a union. A target two replicas out
// of three can serve is a target that fails one request in three, and
// reporting it as served would make the handover gate wave through exactly
// the move it exists to hold back.
func TestAggregate_IsAnIntersection(t *testing.T) {
	leases := []coordinationv1.Lease{
		lease("a", 0, []string{"x", "y", "z"}),
		lease("b", 0, []string{"x", "y"}),
		lease("c", 0, []string{"x", "z"}),
	}
	served, reporting, truncated := Aggregate(leases, time.Now())
	if truncated {
		t.Fatal("no lease was truncated")
	}
	if reporting != 3 {
		t.Fatalf("reporting = %d, want 3", reporting)
	}
	if !reflect.DeepEqual(served, []string{"x"}) {
		t.Fatalf("served = %v, want only [x]", served)
	}
}

func TestAggregate_IgnoresStaleLeases(t *testing.T) {
	leases := []coordinationv1.Lease{
		lease("live", 0, []string{"x"}),
		lease("wedged", StaleAfter+time.Minute, []string{"x", "y"}),
	}
	served, reporting, _ := Aggregate(leases, time.Now())
	if reporting != 1 {
		t.Fatalf("reporting = %d, want 1: a replica that stopped renewing is not reporting", reporting)
	}
	if !reflect.DeepEqual(served, []string{"x"}) {
		t.Fatalf("served = %v, want [x] from the live replica alone", served)
	}
}

func TestAggregate_NoLeases(t *testing.T) {
	served, reporting, truncated := Aggregate(nil, time.Now())
	if served != nil || reporting != 0 || truncated {
		t.Fatalf("Aggregate(nil) = %v/%d/%v, want nothing reported", served, reporting, truncated)
	}
}

// A truncated or undecodable Lease means "I cannot tell you what I serve".
// Reading the remaining replicas' intersection as the answer would report
// a target as unserved — or, worse, let a partial list look complete.
func TestAggregate_TruncationPoisonsTheAnswer(t *testing.T) {
	broken := lease("broken", 0, nil)
	delete(broken.Annotations, TargetsAnnotation)
	broken.Annotations[TruncatedAnnotation] = "true"

	served, reporting, truncated := Aggregate([]coordinationv1.Lease{lease("ok", 0, []string{"x"}), broken}, time.Now())
	if !truncated {
		t.Fatal("expected the aggregate to report truncation")
	}
	if served != nil {
		t.Fatalf("served = %v, want nothing when the answer is incomplete", served)
	}
	if reporting != 2 {
		t.Fatalf("reporting = %d, want 2: the replica is alive, it just cannot say what it serves", reporting)
	}
}

func TestAggregate_UndecodableLeaseIsTreatedAsUnknown(t *testing.T) {
	bad := lease("bad", 0, []string{"x"})
	bad.Annotations[TargetsAnnotation] = "!!!not base64!!!"
	_, _, truncated := Aggregate([]coordinationv1.Lease{bad}, time.Now())
	if !truncated {
		t.Fatal("an undecodable annotation is not evidence of anything; it must read as unknown")
	}
}

func TestEncode_TruncatesBeyondTheCap(t *testing.T) {
	// Deliberately incompressible names, so the cap is reached at a
	// realistic-ish count rather than never.
	many := make([]string, 400000)
	for i := range many {
		many[i] = fmt.Sprintf("%d-a1b2c3d4e5f6a7b8c9d0.very-long-group-name-%d.example.org", i, i*7919)
	}
	value, truncated := Encode(many)
	if !truncated {
		t.Fatalf("expected truncation past the %d-byte cap, got %d bytes", MaxEncodedBytes, len(value))
	}
	if value != "" {
		t.Fatal("a truncated encode must return nothing: a partial list read as complete is worse than no list")
	}
}

func TestContains(t *testing.T) {
	set := []string{"a", "m", "z"}
	for _, in := range set {
		if !Contains(set, in) {
			t.Errorf("Contains(%v, %q) = false", set, in)
		}
	}
	for _, out := range []string{"", "b", "zz"} {
		if Contains(set, out) {
			t.Errorf("Contains(%v, %q) = true", set, out)
		}
	}
}

// A renewTime in the future means the publisher's clock is ahead. A little
// is normal; a lot would keep a wedged replica looking live for as long as
// the jump lasts, and a stale report is exactly what could authorise a
// handover onto an instance that has stopped serving the target.
func TestAggregate_RejectsRenewalsTooFarInTheFuture(t *testing.T) {
	skewed := lease("skewed", -(MaxClockSkew + time.Minute), []string{"x"})
	_, reporting, _ := Aggregate([]coordinationv1.Lease{skewed}, time.Now())
	if reporting != 0 {
		t.Fatalf("reporting = %d, want 0: a renewTime %s in the future is not proof of liveness", reporting, MaxClockSkew+time.Minute)
	}

	// A small skew is normal and must still count.
	fine := lease("fine", -(MaxClockSkew / 2), []string{"x"})
	served, reporting, _ := Aggregate([]coordinationv1.Lease{fine}, time.Now())
	if reporting != 1 || !reflect.DeepEqual(served, []string{"x"}) {
		t.Fatalf("a small clock skew must still be accepted; got served=%v reporting=%d", served, reporting)
	}
}

// Encode checks the compressed size, which a highly compressible set can
// slip past. Without the uncompressed check too, Decode would hand back a
// truncated prefix that reads as a complete answer — and every target
// missing from it would look unservable, holding a handover open forever.
func TestEncodeDecode_BoundsTheDecodedSizeToo(t *testing.T) {
	// Maximally compressible: one literal name over and over. A formatted
	// index would make every name distinct, and the payload could then
	// trip MaxEncodedBytes instead — passing this test without ever
	// reaching the bound it is about.
	const name = "targets.example.org"
	many := make([]string, 0, MaxDecodedBytes/len(name)+16)
	for i := 0; i < cap(many); i++ {
		many = append(many, name)
	}

	// Stated rather than assumed: this payload is over the decoded bound
	// and comfortably under the encoded one, so truncation can only be the
	// decoded check firing.
	joined := strings.Join(many, "\n")
	if len(joined) <= MaxDecodedBytes {
		t.Fatalf("fixture is %d bytes decoded, which does not exceed the %d-byte bound it is meant to test", len(joined), MaxDecodedBytes)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(joined)); err != nil {
		t.Fatalf("compressing the fixture: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing the compressor: %v", err)
	}
	if compressed := len(base64.StdEncoding.EncodeToString(buf.Bytes())); compressed > MaxEncodedBytes {
		t.Fatalf("fixture compresses to %d bytes, over the %d-byte encoded bound — it would trip the wrong check", compressed, MaxEncodedBytes)
	}

	value, truncated := Encode(many)
	if !truncated {
		t.Fatalf("a set of %d names decoding to %d bytes, over the %d-byte bound, must be reported as truncated",
			len(many), len(joined), MaxDecodedBytes)
	}
	if value != "" {
		t.Fatal("a truncated encode must return nothing")
	}
}
