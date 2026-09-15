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
	"encoding/base64"
	"strings"
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// generatedCRD renders what Crossplane would produce for establishedXRD
// once it has picked up the conversion webhook.
func generatedCRD(name, svcName, svcNS, path string, port int32, caBundle string, reviewVersions []string) *extv1.CustomResourceDefinition {
	raw, _ := base64.StdEncoding.DecodeString(caBundle)
	return &extv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: extv1.CustomResourceDefinitionSpec{
			Group: "example.org",
			Names: extv1.CustomResourceDefinitionNames{Plural: "xfoos", Kind: "XFoo"},
			Scope: extv1.NamespaceScoped,
			Conversion: &extv1.CustomResourceConversion{
				Strategy: extv1.WebhookConverter,
				Webhook: &extv1.WebhookConversion{
					ClientConfig: &extv1.WebhookClientConfig{
						Service: &extv1.ServiceReference{
							Name: svcName, Namespace: svcNS, Path: &path, Port: &port,
						},
						CABundle: raw,
					},
					ConversionReviewVersions: reviewVersions,
				},
			},
			Versions: []extv1.CustomResourceDefinitionVersion{{Name: "v2", Served: true, Storage: true}},
		},
	}
}

// appliedCABundle is what readyServer's Secret carries, base64-encoded the
// way the operator reads it out.
func appliedCABundle() string { return base64.StdEncoding.EncodeToString([]byte("fake-ca-bundle")) }

func TestXRDReconcile_PropagationNotObservedUntilCrossplaneRenders(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	server, secret := readyServer("srv")

	c := newFakeClient(cfg, server, secret).Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}

	for i := 0; i < 2; i++ {
		if _, err := reconcileXRD(t, r, "cfg"); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}

	got := getXRDConfig(t, r, "cfg")
	// Applied must NOT wait on propagation: that would make this
	// operator's phase depend on a third party's reconcile speed.
	if got.Status.Phase != teraskyv1alpha1.PhaseApplied {
		t.Fatalf("expected Applied, got %q (%s)", got.Status.Phase, got.Status.Message)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionConversionPropagated)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected ConversionPropagated=False, got %+v", cond)
	}
	if cond.Reason != teraskyv1alpha1.ReasonGeneratedCRDNotFound {
		t.Errorf("reason = %q, want GeneratedCRDNotFound", cond.Reason)
	}
	if len(got.Status.GeneratedCRDs) != 1 || got.Status.GeneratedCRDs[0].Name != "xfoos.example.org" {
		t.Fatalf("expected one generated-CRD status entry, got %+v", got.Status.GeneratedCRDs)
	}
	if got.Status.GeneratedCRDs[0].Propagated {
		t.Error("a missing CRD is not propagated")
	}
}

func TestXRDReconcile_PropagationFlipsTrueOnceTheCRDMatches(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	server, secret := readyServer("srv")

	c := newFakeClient(cfg, server, secret).Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := reconcileXRD(t, r, "cfg"); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}

	// Now write the generated CRD by hand, exactly as Crossplane would.
	crd := generatedCRD("xfoos.example.org", cwsServiceName("srv"), "operator-ns",
		"/convert/xfoos.example.org", 443, appliedCABundle(), []string{"v1"})
	if err := c.Create(context.Background(), crd); err != nil {
		t.Fatalf("creating generated CRD: %v", err)
	}

	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("reconcile after render: %v", err)
	}
	got := getXRDConfig(t, r, "cfg")
	cond := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionConversionPropagated)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != teraskyv1alpha1.ReasonPropagated {
		t.Fatalf("expected ConversionPropagated=True/Propagated, got %+v", cond)
	}
	if len(got.Status.GeneratedCRDs) != 1 || !got.Status.GeneratedCRDs[0].Propagated {
		t.Fatalf("expected the CRD status to be propagated, got %+v", got.Status.GeneratedCRDs)
	}
	st := got.Status.GeneratedCRDs[0]
	if !strings.HasPrefix(st.ObservedCABundleHash, "sha256:") {
		t.Errorf("expected a hashed CA bundle, got %q", st.ObservedCABundleHash)
	}
	if st.ObservedAt == nil {
		t.Error("expected observedAt to be set")
	}
	// The status must never carry the bundle itself.
	if strings.Contains(st.ObservedCABundleHash, "fake-ca-bundle") {
		t.Error("status must record a hash, not the certificate")
	}
}

func TestXRDReconcile_PropagationCABundleRotationAndRecovery(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	server, secret := readyServer("srv")

	c := newFakeClient(cfg, server, secret).Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}
	if err := c.Create(context.Background(), xrd); err != nil {
		t.Fatalf("creating XRD fixture: %v", err)
	}
	// A CRD rendered with somebody else's (or an older) CA bundle.
	stale := generatedCRD("xfoos.example.org", cwsServiceName("srv"), "operator-ns",
		"/convert/xfoos.example.org", 443, base64.StdEncoding.EncodeToString([]byte("rotated-away")), []string{"v1"})
	if err := c.Create(context.Background(), stale); err != nil {
		t.Fatalf("creating generated CRD: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := reconcileXRD(t, r, "cfg"); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}

	got := getXRDConfig(t, r, "cfg")
	cond := meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionConversionPropagated)
	if cond == nil || cond.Reason != teraskyv1alpha1.ReasonCABundleStale {
		t.Fatalf("expected reason CABundleStale, got %+v", cond)
	}
	if !strings.Contains(cond.Message, "TLS") {
		t.Errorf("a stale bundle fails TLS verification; the message should say so: %q", cond.Message)
	}

	// Crossplane catches up. The config must recover on its own, with no
	// operator restart and no manual intervention.
	live := &extv1.CustomResourceDefinition{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "xfoos.example.org"}, live); err != nil {
		t.Fatalf("getting CRD: %v", err)
	}
	live.Spec.Conversion.Webhook.ClientConfig.CABundle = []byte("fake-ca-bundle")
	if err := c.Update(context.Background(), live); err != nil {
		t.Fatalf("updating CRD: %v", err)
	}
	if _, err := reconcileXRD(t, r, "cfg"); err != nil {
		t.Fatalf("reconcile after rotation: %v", err)
	}
	got = getXRDConfig(t, r, "cfg")
	cond = meta.FindStatusCondition(got.Status.Conditions, teraskyv1alpha1.ConditionConversionPropagated)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected recovery to Propagated=True, got %+v", cond)
	}
}

func TestCompareGeneratedConversion(t *testing.T) {
	want := expectedConversion{
		ServiceName: "srv-conversion", ServiceNamespace: "operator-ns",
		Path: "/convert/xfoos.example.org", Port: 443,
		CABundle: appliedCABundle(), ReviewVersions: []string{"v1"},
	}
	match := func() *extv1.CustomResourceDefinition {
		return generatedCRD("xfoos.example.org", want.ServiceName, want.ServiceNamespace, want.Path, want.Port, want.CABundle, []string{"v1"})
	}

	t.Run("matching", func(t *testing.T) {
		if reason, detail := compareGeneratedConversion(match(), want); reason != "" {
			t.Fatalf("expected a match, got %q: %s", reason, detail)
		}
	})

	t.Run("strategy None is the dangerous state", func(t *testing.T) {
		crd := match()
		crd.Spec.Conversion = &extv1.CustomResourceConversion{Strategy: extv1.NoneConverter}
		reason, detail := compareGeneratedConversion(crd, want)
		if reason != teraskyv1alpha1.ReasonNotPropagated {
			t.Fatalf("reason = %q", reason)
		}
		// The message has to explain why "None" is worse than an outage:
		// the apiserver relabels and returns wrong data with a 200.
		if !strings.Contains(detail, "relabels") {
			t.Errorf("detail should explain the silent-wrong-data failure mode: %q", detail)
		}
	})

	t.Run("no conversion block at all", func(t *testing.T) {
		crd := match()
		crd.Spec.Conversion = nil
		if reason, _ := compareGeneratedConversion(crd, want); reason != teraskyv1alpha1.ReasonNotPropagated {
			t.Fatalf("reason = %q", reason)
		}
	})

	t.Run("wrong service", func(t *testing.T) {
		crd := generatedCRD("x", "other-svc", "other-ns", want.Path, want.Port, want.CABundle, []string{"v1"})
		reason, detail := compareGeneratedConversion(crd, want)
		if reason != teraskyv1alpha1.ReasonNotPropagated || !strings.Contains(detail, "other-ns/other-svc") {
			t.Fatalf("reason=%q detail=%q", reason, detail)
		}
	})

	t.Run("wrong path", func(t *testing.T) {
		crd := generatedCRD("x", want.ServiceName, want.ServiceNamespace, "/convert/somethingelse", want.Port, want.CABundle, []string{"v1"})
		if reason, _ := compareGeneratedConversion(crd, want); reason != teraskyv1alpha1.ReasonNotPropagated {
			t.Fatalf("reason = %q", reason)
		}
	})

	t.Run("wrong port", func(t *testing.T) {
		crd := generatedCRD("x", want.ServiceName, want.ServiceNamespace, want.Path, 9443, want.CABundle, []string{"v1"})
		if reason, _ := compareGeneratedConversion(crd, want); reason != teraskyv1alpha1.ReasonNotPropagated {
			t.Fatalf("reason = %q", reason)
		}
	})

	t.Run("review version ordering is not a mismatch", func(t *testing.T) {
		w := want
		w.ReviewVersions = []string{"v1", "v1beta1"}
		crd := generatedCRD("x", w.ServiceName, w.ServiceNamespace, w.Path, w.Port, w.CABundle, []string{"v1beta1", "v1"})
		if reason, detail := compareGeneratedConversion(crd, w); reason != "" {
			t.Fatalf("ordering carries no meaning; expected a match, got %q: %s", reason, detail)
		}
	})

	t.Run("stale CA bundle is its own reason", func(t *testing.T) {
		crd := generatedCRD("x", want.ServiceName, want.ServiceNamespace, want.Path, want.Port,
			base64.StdEncoding.EncodeToString([]byte("other")), []string{"v1"})
		if reason, _ := compareGeneratedConversion(crd, want); reason != teraskyv1alpha1.ReasonCABundleStale {
			t.Fatalf("reason = %q, want CABundleStale", reason)
		}
	})
}

func TestVerifyPropagation_ClaimOfferingXRDHasTwoCRDsToCheck(t *testing.T) {
	xrd := establishedXRD("xfoos.example.org")
	_ = unstructured.SetNestedField(xrd.Object, "LegacyCluster", "spec", "scope")
	_ = unstructured.SetNestedField(xrd.Object, "example.org", "spec", "group")
	_ = unstructured.SetNestedMap(xrd.Object, map[string]any{"kind": "XFoo", "plural": "xfoos"}, "spec", "names")
	_ = unstructured.SetNestedMap(xrd.Object, map[string]any{"kind": "Foo", "plural": "foos"}, "spec", "claimNames")

	want := expectedConversion{
		ServiceName: cwsServiceName("srv"), ServiceNamespace: "operator-ns",
		Path: "/convert/xfoos.example.org", Port: 443,
		CABundle: appliedCABundle(), ReviewVersions: []string{"v1"},
	}
	// Only the composite CRD has been rendered so far.
	composite := generatedCRD("xfoos.example.org", want.ServiceName, want.ServiceNamespace, want.Path, want.Port, want.CABundle, []string{"v1"})

	c := newFakeClient(composite).Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")

	r.verifyPropagation(context.Background(), cfg, xrd, want)

	if len(cfg.Status.GeneratedCRDs) != 2 {
		t.Fatalf("a claim-offering XRD has two CRDs to verify, got %+v", cfg.Status.GeneratedCRDs)
	}
	cond := meta.FindStatusCondition(cfg.Status.Conditions, teraskyv1alpha1.ConditionConversionPropagated)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		// Good: the claim CRD is missing, so it must NOT report propagated.
		if cond == nil || cond.Reason != teraskyv1alpha1.ReasonGeneratedCRDNotFound {
			t.Fatalf("expected GeneratedCRDNotFound for the unrendered claim CRD, got %+v", cond)
		}
	} else {
		t.Fatalf("must not report Propagated while the claim CRD is missing: %+v", cfg.Status.GeneratedCRDs)
	}
	if !strings.Contains(cond.Message, "foos.example.org") {
		t.Errorf("the message should name the missing claim CRD: %q", cond.Message)
	}

	// Render the claim CRD too, and it flips.
	claim := generatedCRD("foos.example.org", want.ServiceName, want.ServiceNamespace, want.Path, want.Port, want.CABundle, []string{"v1"})
	if err := c.Create(context.Background(), claim); err != nil {
		t.Fatalf("creating claim CRD: %v", err)
	}
	r.verifyPropagation(context.Background(), cfg, xrd, want)
	cond = meta.FindStatusCondition(cfg.Status.Conditions, teraskyv1alpha1.ConditionConversionPropagated)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected Propagated=True once both CRDs carry it, got %+v", cond)
	}
}

func TestMapGeneratedCRDToConfigs_FindsTheClaimCRDViaItsOwnerReference(t *testing.T) {
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	c := newFakeClient(cfg).
		WithIndex(&teraskyv1alpha1.XRDConversionConfig{}, TargetXRDNameIndex, func(obj client.Object) []string {
			return []string{obj.(*teraskyv1alpha1.XRDConversionConfig).Spec.TargetXRD.Name}
		}).Build()
	r := &XRDConversionConfigReconciler{Client: c}

	t.Run("composite CRD resolves by name", func(t *testing.T) {
		crd := &extv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "xfoos.example.org"}}
		if got := r.mapGeneratedCRDToConfigs(context.Background(), crd); len(got) != 1 || got[0].Name != "cfg" {
			t.Fatalf("expected cfg, got %+v", got)
		}
	})

	t.Run("claim CRD resolves via its owner reference", func(t *testing.T) {
		// The claim CRD's name has no relationship to the XRD's, so the
		// owner reference is the only way to find it.
		crd := &extv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{
			Name: "foos.example.org",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: xrdadapter.GroupVersionKind.GroupVersion().String(),
				Kind:       xrdadapter.GroupVersionKind.Kind,
				Name:       "xfoos.example.org",
			}},
		}}
		if got := r.mapGeneratedCRDToConfigs(context.Background(), crd); len(got) != 1 || got[0].Name != "cfg" {
			t.Fatalf("expected cfg, got %+v", got)
		}
	})

	t.Run("an unrelated CRD enqueues nothing", func(t *testing.T) {
		crd := &extv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "deployments.apps"}}
		if got := r.mapGeneratedCRDToConfigs(context.Background(), crd); len(got) != 0 {
			t.Fatalf("expected no requests, got %+v", got)
		}
	})

	t.Run("the composite is not enqueued twice", func(t *testing.T) {
		// Name and owner name are the same for a composite; the map must
		// deduplicate rather than reconcile the config twice per event.
		crd := &extv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{
			Name: "xfoos.example.org",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: xrdadapter.GroupVersionKind.GroupVersion().String(),
				Kind:       xrdadapter.GroupVersionKind.Kind,
				Name:       "xfoos.example.org",
			}},
		}}
		if got := r.mapGeneratedCRDToConfigs(context.Background(), crd); len(got) != 1 {
			t.Fatalf("expected exactly one request, got %+v", got)
		}
	})
}

// TestVerifyPropagation_WebhookWithNoClientConfig guards a shape the
// apiserver should never produce but which must not take the controller
// down if it ever does: strategy Webhook with a nil clientConfig.
func TestVerifyPropagation_WebhookWithNoClientConfig(t *testing.T) {
	crd := &extv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "xfoos.example.org"},
		Spec: extv1.CustomResourceDefinitionSpec{
			Group: "example.org",
			Names: extv1.CustomResourceDefinitionNames{Plural: "xfoos", Kind: "XFoo"},
			Scope: extv1.NamespaceScoped,
			Conversion: &extv1.CustomResourceConversion{
				Strategy: extv1.WebhookConverter,
				Webhook:  &extv1.WebhookConversion{},
			},
			Versions: []extv1.CustomResourceDefinitionVersion{{Name: "v2", Served: true, Storage: true}},
		},
	}
	c := newFakeClient(crd).Build()
	r := &XRDConversionConfigReconciler{Client: c, DefaultServerNamespace: "operator-ns"}
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")

	// Must not panic, and must report the CRD as not propagated.
	r.verifyPropagation(context.Background(), cfg, establishedXRD("xfoos.example.org"), expectedConversion{
		ServiceName: cwsServiceName("srv"), ServiceNamespace: "operator-ns",
		Path: "/convert/xfoos.example.org", Port: 443,
		CABundle: appliedCABundle(), ReviewVersions: []string{"v1"},
	})

	cond := meta.FindStatusCondition(cfg.Status.Conditions, teraskyv1alpha1.ConditionConversionPropagated)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected ConversionPropagated=False, got %+v", cond)
	}
	if len(cfg.Status.GeneratedCRDs) != 1 || cfg.Status.GeneratedCRDs[0].Propagated {
		t.Fatalf("expected the CRD to be reported not propagated, got %+v", cfg.Status.GeneratedCRDs)
	}
}
