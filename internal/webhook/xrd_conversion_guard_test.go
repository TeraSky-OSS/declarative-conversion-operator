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

package webhook

import (
	"context"
	"encoding/json"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8sjson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/controller"
	"github.com/terasky-oss/declarative-conversion-operator/internal/conversionpatch"
)

const (
	guardXRDName = "xfoos.example.org"
	guardCfgName = "cfg"
	guardSvcName = "srv-conversion"
	guardSvcNS   = "operator-ns"
	guardPath    = "/convert/xfoos.example.org"
	guardPort    = int32(8443)
)

// appliedConfig is a config in exactly the state that earns an apply:
// validated, applied, with resolved webhook coordinates.
func appliedConfig() *teraskyv1alpha1.XRDConversionConfig {
	return &teraskyv1alpha1.XRDConversionConfig{
		ObjectMeta: metav1.ObjectMeta{Name: guardCfgName},
		Spec: teraskyv1alpha1.XRDConversionConfigSpec{
			TargetXRD:  teraskyv1alpha1.TargetXRDRef{Name: guardXRDName},
			HubVersion: "v2",
			Spokes:     []teraskyv1alpha1.SpokeVersionRules{{Version: "v1"}},
		},
		Status: teraskyv1alpha1.XRDConversionConfigStatus{
			Phase:                 teraskyv1alpha1.PhaseApplied,
			AssignedWebhookServer: "srv",
			WebhookPath:           guardPath,
			WebhookURL:            "https://" + guardSvcName + "." + guardSvcNS + ".svc" + guardPath,
			WebhookPort:           guardPort,
			LastAppliedPlanHash:   "hash-abc123",
			Conditions: []metav1.Condition{{
				Type: teraskyv1alpha1.ConditionApplied, Status: metav1.ConditionTrue,
				Reason: "Applied", LastTransitionTime: metav1.Now(),
			}},
		},
	}
}

// strippedXRD is what the establisher's full client.Update leaves behind:
// the package's own XRD, with no spec.conversion and no annotations.
func strippedXRD() *unstructured.Unstructured { return establishedXRD(guardXRDName) }

// ourConversionXRD is the healthy steady state.
func ourConversionXRD() *unstructured.Unstructured {
	xrd := establishedXRD(guardXRDName)
	xrd.SetAnnotations(map[string]string{
		conversionpatch.ManagedByAnnotation: guardCfgName,
		conversionpatch.PlanHashAnnotation:  "hash-abc123",
	})
	_ = unstructured.SetNestedMap(xrd.Object, map[string]any{
		"strategy": "Webhook",
		"webhook": map[string]any{
			"clientConfig": map[string]any{
				"service": map[string]any{
					"name": guardSvcName, "namespace": guardSvcNS, "path": guardPath, "port": int64(guardPort),
				},
				"caBundle": "Zm9v",
			},
			"conversionReviewVersions": []any{"v1"},
		},
	}, "spec", "conversion")
	return xrd
}

func newGuard(t *testing.T, objs ...runtime.Object) *XRDConversionGuard {
	t.Helper()
	c := newFakeClient(objs...).
		WithIndex(&teraskyv1alpha1.XRDConversionConfig{}, TargetXRDNameIndexName, func(obj client.Object) []string {
			cfg := obj.(*teraskyv1alpha1.XRDConversionConfig)
			if cfg.Spec.TargetXRD.Name == "" {
				return nil
			}
			return []string{cfg.Spec.TargetXRD.Name}
		}).Build()
	g := &XRDConversionGuard{Client: c}
	g.InjectDecoder(admission.NewDecoder(newScheme()))
	return g
}

func guardRequest(t *testing.T, xrd *unstructured.Unstructured, op admissionv1.Operation) admission.Request {
	t.Helper()
	raw, err := json.Marshal(xrd.Object)
	if err != nil {
		t.Fatalf("marshalling XRD: %v", err)
	}
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Name:      xrd.GetName(),
		Operation: op,
		Object:    runtime.RawExtension{Raw: raw},
	}}
}

// applyPatches replays the JSON Patch the guard returned onto the incoming
// object, so assertions are about the resulting XRD rather than about patch
// operation shapes.
func applyPatches(t *testing.T, in *unstructured.Unstructured, resp admission.Response) *unstructured.Unstructured {
	t.Helper()
	if len(resp.Patches) == 0 {
		return in
	}
	patchJSON, err := json.Marshal(resp.Patches)
	if err != nil {
		t.Fatalf("marshalling patches: %v", err)
	}
	original, err := json.Marshal(in.Object)
	if err != nil {
		t.Fatalf("marshalling original: %v", err)
	}
	patch, err := jsonpatch.DecodePatch(patchJSON)
	if err != nil {
		t.Fatalf("decoding patch: %v", err)
	}
	patched, err := patch.Apply(original)
	if err != nil {
		t.Fatalf("applying patch: %v", err)
	}
	// Decode with apimachinery's JSON, not encoding/json: the apiserver
	// decodes an unstructured object with UseNumber semantics, so whole
	// numbers land as int64. encoding/json would make them float64 and
	// every NestedInt64 assertion below would fail for a reason that does
	// not exist in production.
	out := &unstructured.Unstructured{}
	if err := k8sjson.Unmarshal(patched, &out.Object); err != nil {
		t.Fatalf("unmarshalling patched: %v", err)
	}
	return out
}

func TestXRDConversionGuard_RestoresAStrippedConversion(t *testing.T) {
	// The case the guard exists for: Crossplane's establisher replaced the
	// XRD with the package's copy, and spec.conversion went with it.
	g := newGuard(t, appliedConfig())
	in := strippedXRD()

	resp := g.Handle(context.Background(), guardRequest(t, in, admissionv1.Update))
	if !resp.Allowed {
		t.Fatalf("the guard must never deny: %+v", resp.Result)
	}
	if len(resp.Patches) == 0 {
		t.Fatal("expected the conversion stanza to be restored")
	}

	out := applyPatches(t, in, resp)
	strategy, _, _ := unstructured.NestedString(out.Object, "spec", "conversion", "strategy")
	if strategy != "Webhook" {
		t.Fatalf("strategy = %q, want Webhook", strategy)
	}
	name, _, _ := unstructured.NestedString(out.Object, "spec", "conversion", "webhook", "clientConfig", "service", "name")
	ns, _, _ := unstructured.NestedString(out.Object, "spec", "conversion", "webhook", "clientConfig", "service", "namespace")
	path, _, _ := unstructured.NestedString(out.Object, "spec", "conversion", "webhook", "clientConfig", "service", "path")
	if name != guardSvcName || ns != guardSvcNS || path != guardPath {
		t.Errorf("restored coordinates = %s/%s%s, want %s/%s%s", ns, name, path, guardSvcNS, guardSvcName, guardPath)
	}
	ann := out.GetAnnotations()
	if ann[conversionpatch.ManagedByAnnotation] != guardCfgName {
		t.Errorf("managed-by annotation = %q", ann[conversionpatch.ManagedByAnnotation])
	}
	if ann[conversionpatch.PlanHashAnnotation] != "hash-abc123" {
		t.Errorf("plan-hash annotation = %q", ann[conversionpatch.PlanHashAnnotation])
	}
	// Only ever add: the package's own fields must go through untouched.
	if _, found, _ := unstructured.NestedSlice(out.Object, "spec", "versions"); !found {
		t.Error("the guard must not disturb anything but spec.conversion and the two annotations")
	}
}

func TestXRDConversionGuard_RestoresAnExplicitStrategyNone(t *testing.T) {
	// The establisher can equally write the package's own `strategy: None`
	// rather than omitting the block. Same outcome, same fix.
	g := newGuard(t, appliedConfig())
	in := strippedXRD()
	_ = unstructured.SetNestedField(in.Object, "None", "spec", "conversion", "strategy")

	resp := g.Handle(context.Background(), guardRequest(t, in, admissionv1.Update))
	if len(resp.Patches) == 0 {
		t.Fatal("expected strategy: None to be corrected")
	}
	out := applyPatches(t, in, resp)
	strategy, _, _ := unstructured.NestedString(out.Object, "spec", "conversion", "strategy")
	if strategy != "Webhook" {
		t.Fatalf("strategy = %q, want Webhook", strategy)
	}
}

func TestXRDConversionGuard_IndexMissIsANoOp(t *testing.T) {
	// The overwhelmingly common case: an XRD nobody configured conversion
	// for. It must cost an in-memory lookup and produce no patch.
	g := newGuard(t) // no configs at all
	in := strippedXRD()

	resp := g.Handle(context.Background(), guardRequest(t, in, admissionv1.Update))
	if !resp.Allowed || len(resp.Patches) != 0 {
		t.Fatalf("expected an empty allow, got allowed=%v patches=%+v", resp.Allowed, resp.Patches)
	}
}

func TestXRDConversionGuard_LeavesAThirdPartyWebhookAlone(t *testing.T) {
	// An XRD deliberately wired to a hand-written conversion webhook must
	// not be hijacked. The managed-by annotation is what distinguishes it.
	g := newGuard(t, appliedConfig())
	in := establishedXRD(guardXRDName)
	_ = unstructured.SetNestedMap(in.Object, map[string]any{
		"strategy": "Webhook",
		"webhook": map[string]any{"clientConfig": map[string]any{"service": map[string]any{
			"name": "someone-elses", "namespace": "their-ns", "path": "/convert", "port": int64(443),
		}}},
	}, "spec", "conversion")

	resp := g.Handle(context.Background(), guardRequest(t, in, admissionv1.Update))
	if !resp.Allowed {
		t.Fatalf("must not deny: %+v", resp.Result)
	}
	if len(resp.Patches) != 0 {
		t.Fatalf("must not touch a third-party webhook, got patches %+v", resp.Patches)
	}
}

func TestXRDConversionGuard_AlreadyCorrectIsANoOp(t *testing.T) {
	// The steady state. Patching here would rewrite every XRD on every
	// write for no reason.
	g := newGuard(t, appliedConfig())
	in := ourConversionXRD()

	resp := g.Handle(context.Background(), guardRequest(t, in, admissionv1.Update))
	if len(resp.Patches) != 0 {
		t.Fatalf("an already-correct XRD needs no patch, got %+v", resp.Patches)
	}
}

func TestXRDConversionGuard_PreservesAnExistingCABundle(t *testing.T) {
	// cert-manager's ca-injector and the controller both own the bundle;
	// the guard must not blank one that survived the write.
	g := newGuard(t, appliedConfig())
	in := ourConversionXRD()
	// Same bundle, but wrong coordinates — so a patch is warranted.
	_ = unstructured.SetNestedField(in.Object, "wrong-svc", "spec", "conversion", "webhook", "clientConfig", "service", "name")

	resp := g.Handle(context.Background(), guardRequest(t, in, admissionv1.Update))
	if len(resp.Patches) == 0 {
		t.Fatal("expected wrong coordinates to be corrected")
	}
	out := applyPatches(t, in, resp)
	ca, _, _ := unstructured.NestedString(out.Object, "spec", "conversion", "webhook", "clientConfig", "caBundle")
	if ca != "Zm9v" {
		t.Errorf("caBundle = %q, want the incoming bundle preserved", ca)
	}
}

func TestXRDConversionGuard_NeverInjectsForAConfigThatHasNotEarnedIt(t *testing.T) {
	// Injecting conversion for a config that has not passed its own gates
	// would route live admission traffic at a webhook server the operator
	// has never confirmed is serving it.
	cases := []struct {
		name   string
		mutate func(*teraskyv1alpha1.XRDConversionConfig)
	}{
		{"never applied", func(c *teraskyv1alpha1.XRDConversionConfig) {
			c.Status.LastAppliedPlanHash = ""
			c.Status.Phase = teraskyv1alpha1.PhasePending
		}},
		{"Applied condition is False", func(c *teraskyv1alpha1.XRDConversionConfig) {
			c.Status.Conditions[0].Status = metav1.ConditionFalse
		}},
		{"Applied condition absent", func(c *teraskyv1alpha1.XRDConversionConfig) {
			c.Status.Conditions = nil
		}},
		{"invalid", func(c *teraskyv1alpha1.XRDConversionConfig) {
			c.Status.Phase = teraskyv1alpha1.PhaseInvalid
			c.Status.Conditions[0].Status = metav1.ConditionFalse
		}},
		{"no resolved coordinates", func(c *teraskyv1alpha1.XRDConversionConfig) {
			c.Status.WebhookPath = ""
		}},
		{"no assigned server", func(c *teraskyv1alpha1.XRDConversionConfig) {
			c.Status.AssignedWebhookServer = ""
		}},
		{"unparseable webhook URL", func(c *teraskyv1alpha1.XRDConversionConfig) {
			c.Status.WebhookURL = "not-a-url"
		}},
		{"being deleted", func(c *teraskyv1alpha1.XRDConversionConfig) {
			now := metav1.Now()
			c.DeletionTimestamp = &now
			c.Finalizers = []string{teraskyv1alpha1.XRDConversionConfigFinalizer}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := appliedConfig()
			tc.mutate(cfg)
			g := newGuard(t, cfg)
			in := strippedXRD()

			resp := g.Handle(context.Background(), guardRequest(t, in, admissionv1.Update))
			if !resp.Allowed {
				t.Fatalf("must never deny: %+v", resp.Result)
			}
			if len(resp.Patches) != 0 {
				t.Fatalf("must not inject for this config state, got %+v", resp.Patches)
			}
		})
	}
}

func TestXRDConversionGuard_HandlesCreateAsWellAsUpdate(t *testing.T) {
	// A package install is a CREATE, and it drops conversion just as a
	// resync UPDATE does.
	g := newGuard(t, appliedConfig())
	in := strippedXRD()

	resp := g.Handle(context.Background(), guardRequest(t, in, admissionv1.Create))
	if len(resp.Patches) == 0 {
		t.Fatal("expected CREATE to be guarded too")
	}
}

func TestXRDConversionGuard_UndecodableObjectFailsOpen(t *testing.T) {
	g := newGuard(t, appliedConfig())
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Name:      guardXRDName,
		Operation: admissionv1.Update,
		Object:    runtime.RawExtension{Raw: []byte("{not json")},
	}}

	resp := g.Handle(context.Background(), req)
	if !resp.Allowed {
		t.Fatalf("a decode failure must never block somebody else's write: %+v", resp.Result)
	}
	if len(resp.Patches) != 0 {
		t.Fatalf("expected no patches, got %+v", resp.Patches)
	}
}

func TestGuardWebhookService(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		wantSvc   string
		wantNS    string
		wantError bool
	}{
		{name: "standard", url: "https://srv-conversion.operator-ns.svc/convert/xfoos.example.org", wantSvc: "srv-conversion", wantNS: "operator-ns"},
		{name: "cluster.local suffix", url: "https://srv-conversion.operator-ns.svc.cluster.local/convert/x", wantSvc: "srv-conversion", wantNS: "operator-ns"},
		{name: "empty", url: "", wantError: true},
		{name: "not https", url: "http://srv.ns.svc/x", wantError: true},
		{name: "host is not service.namespace.svc", url: "https://srv/x", wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := appliedConfig()
			cfg.Status.WebhookURL = tc.url
			got, err := guardWebhookService(cfg)
			if tc.wantError {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.name != tc.wantSvc || got.namespace != tc.wantNS {
				t.Errorf("got %s/%s, want %s/%s", got.namespace, got.name, tc.wantNS, tc.wantSvc)
			}
		})
	}
}

// TestTargetXRDNameIndexNameMatchesTheController pins the one string this
// package duplicates. The guard's whole scoping story is an in-memory
// lookup against the controller's field index, so a silent divergence here
// would turn every guard invocation into a miss — the failure mode being
// exactly the hourly conversion strip it exists to prevent, with no error
// anywhere to say the guard had stopped working.
func TestTargetXRDNameIndexNameMatchesTheController(t *testing.T) {
	if TargetXRDNameIndexName != controller.TargetXRDNameIndex {
		t.Fatalf("index key drifted: webhook has %q, controller has %q", TargetXRDNameIndexName, controller.TargetXRDNameIndex)
	}
}

// TestXRDConversionGuard_RestoresTheConfiguredPort pins a detail that is
// invisible until somebody sets ConversionWebhookServer.spec.service.port
// to something other than 443: status.webhookURL does not carry the port,
// so the guard has to read it from status.webhookPort. Restoring 443 onto a
// server listening on 8443 would point the generated CRD at a closed port
// until the controller's next reconcile — a window the guard exists to
// eliminate, not to create.
func TestXRDConversionGuard_RestoresTheConfiguredPort(t *testing.T) {
	g := newGuard(t, appliedConfig())
	in := strippedXRD()

	resp := g.Handle(context.Background(), guardRequest(t, in, admissionv1.Update))
	if len(resp.Patches) == 0 {
		t.Fatal("expected the conversion stanza to be restored")
	}
	out := applyPatches(t, in, resp)
	port, found, _ := unstructured.NestedInt64(out.Object, "spec", "conversion", "webhook", "clientConfig", "service", "port")
	if !found || port != int64(guardPort) {
		t.Fatalf("restored port = %d (found=%v), want %d", port, found, guardPort)
	}
}

func TestXRDConversionGuard_WrongPortIsCorrected(t *testing.T) {
	// Everything else matches, so only the port distinguishes "already
	// correct" from "needs restoring".
	g := newGuard(t, appliedConfig())
	in := ourConversionXRD()
	_ = unstructured.SetNestedField(in.Object, int64(443), "spec", "conversion", "webhook", "clientConfig", "service", "port")

	resp := g.Handle(context.Background(), guardRequest(t, in, admissionv1.Update))
	if len(resp.Patches) == 0 {
		t.Fatal("a wrong port must be corrected")
	}
	out := applyPatches(t, in, resp)
	port, _, _ := unstructured.NestedInt64(out.Object, "spec", "conversion", "webhook", "clientConfig", "service", "port")
	if port != int64(guardPort) {
		t.Fatalf("port = %d, want %d", port, guardPort)
	}
}

func TestGuardWebhookService_PortDefaultsWhenUnset(t *testing.T) {
	// A config last applied by an operator predating status.webhookPort.
	// 443 is what that version always used, and the controller fills the
	// field in on its next reconcile.
	cfg := appliedConfig()
	cfg.Status.WebhookPort = 0
	got, err := guardWebhookService(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.port != 443 {
		t.Errorf("port = %d, want the 443 default", got.port)
	}
}
