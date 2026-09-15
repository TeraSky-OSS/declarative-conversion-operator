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
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// ownedByConfigurationRevision stamps the owner reference Crossplane's
// package establisher sets on every object it establishes.
func ownedByConfigurationRevision(xrd *unstructured.Unstructured, revision string) *unstructured.Unstructured {
	xrd.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "pkg.crossplane.io/v1",
		Kind:       "ConfigurationRevision",
		Name:       revision,
		UID:        types.UID("abc-123"),
	}})
	return xrd
}

func revertCount(t *testing.T, target string) float64 {
	t.Helper()
	return testutil.ToFloat64(GetManagerMetrics().ConversionReverts.WithLabelValues("xrd", target))
}

func TestXRDReconcile_PackageManagedCondition(t *testing.T) {
	cases := []struct {
		name       string
		xrd        *unstructured.Unstructured
		wantStatus metav1.ConditionStatus
		wantReason string
		msgHas     string
	}{
		{
			name:       "owned by a ConfigurationRevision",
			xrd:        ownedByConfigurationRevision(establishedXRD("xfoos.example.org"), "platform-abc123"),
			wantStatus: metav1.ConditionTrue,
			wantReason: "OwnedByPackageRevision",
			// The message has to name the revision: "you are affected" is
			// not actionable without "by this".
			msgHas: "platform-abc123",
		},
		{
			name:       "applied by hand",
			xrd:        establishedXRD("xfoos.example.org"),
			wantStatus: metav1.ConditionFalse,
			wantReason: "NotPackageManaged",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
			server, secret := readyServer("srv")
			c := newFakeClient(cfg, server, secret).Build()
			r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}
			if err := c.Create(context.Background(), tc.xrd); err != nil {
				t.Fatalf("creating XRD fixture: %v", err)
			}

			for i := 0; i < 2; i++ {
				if _, err := reconcileXRD(t, r, "cfg"); err != nil {
					t.Fatalf("reconcile %d: %v", i, err)
				}
			}

			got := getXRDConfig(t, r, "cfg")
			found := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionPackageManaged)
			if found == nil {
				// Errorf plus an explicit return rather than Fatalf: inside
				// a subtest closure the early exit is clearer stated than
				// inferred.
				t.Errorf("expected a PackageManaged condition, got %+v", got.Status.Conditions)
				return
			}
			cond := *found
			if cond.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q (message: %s)", cond.Status, tc.wantStatus, cond.Message)
			}
			if cond.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", cond.Reason, tc.wantReason)
			}
			if tc.msgHas != "" && !strings.Contains(cond.Message, tc.msgHas) {
				t.Errorf("message %q does not name %q", cond.Message, tc.msgHas)
			}
		})
	}
}

func TestXRDReconcile_ConversionRevertCounter(t *testing.T) {
	const target = "xreverts.example.org"
	xrd := establishedXRD(target)
	cfg := renameRuleXRDConfig("cfg", target)
	server, secret := readyServer("srv")

	c := newFakeClient(cfg, server, secret).Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}

	before := revertCount(t, target)

	// Reconcile to Applied. The first apply must NOT move the counter:
	// there was no previous state to revert from.
	for i := 0; i < 2; i++ {
		if _, err := reconcileXRD(t, r, "cfg"); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	if got := getXRDConfig(t, r, "cfg"); got.Status.Phase != teraskyv1alpha1.PhaseApplied {
		t.Fatalf("expected Applied, got %q (%s)", got.Status.Phase, got.Status.Message)
	}
	if got := revertCount(t, target); got != before {
		t.Fatalf("the first apply must not count as a revert: %v -> %v", before, got)
	}

	// A steady-state reconcile with the conversion still in place must not
	// move it either.
	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("steady-state reconcile: %v", err)
	}
	if got := revertCount(t, target); got != before {
		t.Fatalf("a no-op reconcile must not count as a revert: %v -> %v", before, got)
	}

	// Now strip spec.conversion and the annotations out of band — exactly
	// what Crossplane's establisher does with its full client.Update.
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(xrdadapter.GroupVersionKind)
	if err := r.Get(context.Background(), types.NamespacedName{Name: target}, live); err != nil {
		t.Fatalf("getting live XRD: %v", err)
	}
	unstructured.RemoveNestedField(live.Object, "spec", "conversion")
	live.SetAnnotations(nil)
	if err := r.Update(context.Background(), live); err != nil {
		t.Fatalf("stripping conversion: %v", err)
	}

	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("reconcile after revert: %v", err)
	}
	if got := revertCount(t, target); got != before+1 {
		t.Fatalf("expected the counter to move by exactly one, got %v -> %v", before, got)
	}

	// And the operator re-applied, so the next reconcile is quiet again.
	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("reconcile after re-apply: %v", err)
	}
	if got := revertCount(t, target); got != before+1 {
		t.Fatalf("a re-applied conversion must not keep counting: %v", got)
	}
}

func TestConversionIsOurs(t *testing.T) {
	withConversion := func(strategy, managedBy string) *unstructured.Unstructured {
		xrd := establishedXRD("x.example.org")
		if strategy != "" {
			_ = unstructured.SetNestedField(xrd.Object, strategy, "spec", "conversion", "strategy")
		}
		if managedBy != "" {
			xrd.SetAnnotations(map[string]string{"conversion.terasky.com/managed-by": managedBy})
		}
		return xrd
	}

	cases := []struct {
		name string
		xrd  *unstructured.Unstructured
		want bool
	}{
		{"ours", withConversion("Webhook", "cfg"), true},
		{"stripped entirely", withConversion("", ""), false},
		{"reset to None", withConversion("None", "cfg"), false},
		// Both halves are load-bearing: a hand-written webhook would
		// otherwise look like ours, and an annotation surviving a replaced
		// conversion block would hide a real overwrite.
		{"somebody else's webhook", withConversion("Webhook", ""), false},
		{"another config's webhook", withConversion("Webhook", "other-cfg"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := conversionIsOurs(tc.xrd, "cfg"); got != tc.want {
				t.Errorf("conversionIsOurs = %v, want %v", got, tc.want)
			}
		})
	}
}
