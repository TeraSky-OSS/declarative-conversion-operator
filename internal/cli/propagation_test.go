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

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// xrdWithWebhook is the shape the operator leaves behind after a
// successful apply.
func xrdWithWebhook() *unstructured.Unstructured {
	xrd := claimXRD()
	_ = unstructured.SetNestedMap(xrd.Object, map[string]any{
		"strategy": "Webhook",
		"webhook": map[string]any{
			"clientConfig": map[string]any{
				"service": map[string]any{
					"name":      "default-conversion",
					"namespace": "declarative-conversion-system",
					"path":      "/convert/xwidgets.e2e.example.org",
					"port":      int64(443),
				},
			},
		},
	}, "spec", "conversion")
	return xrd
}

// renderedCRD is what Crossplane produces once it has picked the webhook
// up. strategy "None" is the pre-render (and the dangerous) state.
func renderedCRD(name, strategy, svcNS, svcName, path string) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"group": "e2e.example.org"},
	}
	conv := map[string]any{"strategy": strategy}
	if strategy == "Webhook" {
		conv["webhook"] = map[string]any{"clientConfig": map[string]any{"service": map[string]any{
			"name": svcName, "namespace": svcNS, "path": path, "port": int64(443),
		}}}
	}
	obj["spec"].(map[string]any)["conversion"] = conv
	return &unstructured.Unstructured{Object: obj}
}

func TestVerifyPropagation_BothCRDsCarryTheWebhook(t *testing.T) {
	const (
		ns   = "declarative-conversion-system"
		svc  = "default-conversion"
		path = "/convert/xwidgets.e2e.example.org"
	)
	dyn := newClaimFake(
		xrdWithWebhook(),
		renderedCRD("xwidgets.e2e.example.org", "Webhook", ns, svc, path),
		renderedCRD("widgets.e2e.example.org", "Webhook", ns, svc, path),
	)

	rep, err := VerifyPropagation(context.Background(), dyn, "xwidgets.e2e.example.org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.Propagated {
		t.Fatalf("expected propagated, got %+v", rep.CRDs)
	}
	if len(rep.CRDs) != 2 {
		t.Fatalf("a claim-offering XRD has two CRDs to check, got %+v", rep.CRDs)
	}
	for _, c := range rep.CRDs {
		if !c.MatchesXRD {
			t.Errorf("%s does not match: %s", c.CRD, c.Message)
		}
	}
}

func TestVerifyPropagation_ClaimCRDLagsBehind(t *testing.T) {
	// The exact half-propagated state that makes claims silently return
	// wrong data while composites convert fine.
	const (
		ns   = "declarative-conversion-system"
		svc  = "default-conversion"
		path = "/convert/xwidgets.e2e.example.org"
	)
	dyn := newClaimFake(
		xrdWithWebhook(),
		renderedCRD("xwidgets.e2e.example.org", "Webhook", ns, svc, path),
		renderedCRD("widgets.e2e.example.org", "None", "", "", ""),
	)

	rep, err := VerifyPropagation(context.Background(), dyn, "xwidgets.e2e.example.org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Propagated {
		t.Fatal("must not report propagated while the claim CRD is still strategy: None")
	}
	// A value plus a found flag rather than a pointer: there is nothing to
	// dereference, so there is nothing for a reader (or staticcheck) to
	// wonder about.
	var claim PropagationCRDStatus
	found := false
	for _, c := range rep.CRDs {
		if c.Role == "claim" {
			claim, found = c, true
		}
	}
	if !found {
		t.Fatalf("no claim entry: %+v", rep.CRDs)
	}
	// The message has to explain why "None" is worse than an outage.
	if !strings.Contains(claim.Message, "HTTP 200") {
		t.Errorf("message should name the silent-wrong-data failure mode: %q", claim.Message)
	}

	var buf bytes.Buffer
	rep.WriteTable(&buf)
	out := buf.String()
	if !strings.Contains(out, "NOT PROPAGATED") || !strings.Contains(out, "widgets.e2e.example.org") {
		t.Errorf("table should be loud and name the lagging CRD:\n%s", out)
	}
}

func TestVerifyPropagation_CRDPointsAtADifferentWebhook(t *testing.T) {
	dyn := newClaimFake(
		xrdWithWebhook(),
		renderedCRD("xwidgets.e2e.example.org", "Webhook", "other-ns", "other-svc", "/convert/elsewhere"),
		renderedCRD("widgets.e2e.example.org", "Webhook", "declarative-conversion-system", "default-conversion", "/convert/xwidgets.e2e.example.org"),
	)

	rep, err := VerifyPropagation(context.Background(), dyn, "xwidgets.e2e.example.org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Propagated {
		t.Fatal("a CRD pointing somewhere else is not propagated")
	}
	if !strings.Contains(rep.CRDs[0].Message, "other-ns/other-svc") {
		t.Errorf("message should name where it actually points: %q", rep.CRDs[0].Message)
	}
}

func TestVerifyPropagation_XRDHasNoWebhookAtAll(t *testing.T) {
	// Nothing to propagate yet — the report must say that rather than
	// blaming Crossplane for not rendering something nobody asked for.
	dyn := newClaimFake(
		claimXRD(),
		renderedCRD("xwidgets.e2e.example.org", "None", "", "", ""),
		renderedCRD("widgets.e2e.example.org", "None", "", "", ""),
	)

	rep, err := VerifyPropagation(context.Background(), dyn, "xwidgets.e2e.example.org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.XRDStrategy != "None" {
		t.Errorf("XRDStrategy = %q", rep.XRDStrategy)
	}
	if rep.Propagated {
		t.Fatal("no webhook on the XRD is not a propagated state")
	}
	if !strings.Contains(rep.CRDs[0].Message, "XRDConversionConfig") {
		t.Errorf("message should point at the missing config, not at Crossplane: %q", rep.CRDs[0].Message)
	}
}

func TestVerifyPropagation_GeneratedCRDDoesNotExistYet(t *testing.T) {
	dyn := newClaimFake(xrdWithWebhook())

	rep, err := VerifyPropagation(context.Background(), dyn, "xwidgets.e2e.example.org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Propagated {
		t.Fatal("a missing CRD is not propagated")
	}
	if !strings.Contains(rep.CRDs[0].Message, "has not created this CRD yet") {
		t.Errorf("unexpected message: %q", rep.CRDs[0].Message)
	}
}

func TestRunTest_VerifyPropagationConstraints(t *testing.T) {
	// TestOptions is exported, so the cobra command's own check is not the
	// only way in. Silently skipping a check the caller asked for is
	// exactly the failure mode the check exists to prevent.
	t.Run("without --live", func(t *testing.T) {
		_, err := RunTest(TestOptions{
			XRDPath: "testdata/xrd.yaml", ConfigPath: "testdata/config.yaml",
			SamplesDir: "testdata/samples", VerifyPropagation: true,
		})
		if err == nil || !strings.Contains(err.Error(), "requires --live") {
			t.Fatalf("expected a refusal, got %v", err)
		}
	})

	t.Run("against a CRDConversionConfig", func(t *testing.T) {
		// A CRDConversionConfig's target IS the CRD, so there is no
		// generated CRD for anything to propagate into.
		_, err := RunTest(TestOptions{
			CRDPath: "testdata/crd.yaml", ConfigPath: "testdata/crdconfig.yaml",
			Live: true, VerifyPropagation: true,
		})
		if err == nil || !strings.Contains(err.Error(), "applies only to an XRDConversionConfig") {
			t.Fatalf("expected a refusal, got %v", err)
		}
	})
}
