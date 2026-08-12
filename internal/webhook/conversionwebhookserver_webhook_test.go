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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func TestConversionWebhookServerValidator_CheckDefault_NoConflictWhenNotDefault(t *testing.T) {
	c := newFakeClient().Build()
	v := &ConversionWebhookServerValidator{Client: c}
	server := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "srv"}}
	if _, err := v.ValidateCreate(context.Background(), server); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConversionWebhookServerValidator_ExtraArgs_RejectsManagedFlag(t *testing.T) {
	c := newFakeClient().Build()
	v := &ConversionWebhookServerValidator{Client: c}
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			ExtraArgs: []string{"--webhook-server-name=evil"},
		},
	}
	if _, err := v.ValidateCreate(context.Background(), server); err == nil {
		t.Fatal("expected ExtraArgs naming a managed flag to be rejected")
	}
	if _, err := v.ValidateUpdate(context.Background(), server, server); err == nil {
		t.Fatal("expected ExtraArgs naming a managed flag to be rejected on update")
	}
}

func TestConversionWebhookServerValidator_ExtraArgs_AllowsOptional(t *testing.T) {
	c := newFakeClient().Build()
	v := &ConversionWebhookServerValidator{Client: c}
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec: teraskyv1alpha1.ConversionWebhookServerSpec{
			ExtraArgs: []string{"--cert-reload-interval=1m", "--zap-devel", "true"},
		},
	}
	if _, err := v.ValidateCreate(context.Background(), server); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConversionWebhookServerValidator_CheckDefault_FirstDefaultAllowed(t *testing.T) {
	c := newFakeClient().Build()
	v := &ConversionWebhookServerValidator{Client: c}
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: true},
	}
	if _, err := v.ValidateCreate(context.Background(), server); err != nil {
		t.Fatalf("unexpected error for the first default instance: %v", err)
	}
}

func TestConversionWebhookServerValidator_CheckDefault_RejectsSecondDefault(t *testing.T) {
	existing := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "existing"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: true},
	}
	c := newFakeClient(existing).Build()
	v := &ConversionWebhookServerValidator{Client: c}
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: true},
	}
	if _, err := v.ValidateCreate(context.Background(), server); err == nil {
		t.Fatalf("expected an error when a second instance is marked default")
	}
	if _, err := v.ValidateUpdate(context.Background(), server, server); err == nil {
		t.Fatalf("expected ValidateUpdate to enforce the same rule")
	}
}

func TestConversionWebhookServerValidator_CheckDefault_UpdatingSelfIsNotAConflict(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: true},
	}
	c := newFakeClient(server).Build()
	v := &ConversionWebhookServerValidator{Client: c}
	if _, err := v.ValidateUpdate(context.Background(), server, server); err != nil {
		t.Fatalf("unexpected error: a server must not conflict with itself: %v", err)
	}
}

func TestConversionWebhookServerValidator_ValidateDelete_NoDependents(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "srv"}}
	c := newFakeClient(server).Build()
	v := &ConversionWebhookServerValidator{Client: c}
	if _, err := v.ValidateDelete(context.Background(), server); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConversionWebhookServerValidator_ValidateDelete_BlockedByXRDConfig(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "srv"}}
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv"}
	c := newFakeClient(server, cfg).Build()
	v := &ConversionWebhookServerValidator{Client: c}
	if _, err := v.ValidateDelete(context.Background(), server); err == nil {
		t.Fatalf("expected deletion to be blocked by a dependent XRDConversionConfig")
	}
}

func TestConversionWebhookServerValidator_ValidateDelete_BlockedByCRDConfig(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "srv"}}
	cfg := renameRuleCRDConfig("cfg", "foos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv"}
	c := newFakeClient(server, cfg).Build()
	v := &ConversionWebhookServerValidator{Client: c}
	if _, err := v.ValidateDelete(context.Background(), server); err == nil {
		t.Fatalf("expected deletion to be blocked by a dependent CRDConversionConfig")
	}
}

func TestConversionWebhookServerValidator_ValidateDelete_DefaultInstanceBlockedByImplicitDependents(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv"},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: true},
	}
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org") // no explicit webhookServerRef: falls back to default
	c := newFakeClient(server, cfg).Build()
	v := &ConversionWebhookServerValidator{Client: c}
	_, err := v.ValidateDelete(context.Background(), server)
	if err == nil {
		t.Fatalf("expected deletion of the default instance to be blocked by an implicitly-assigned config")
	}
}

func TestConversionWebhookServerValidator_ValidateDelete_ForceAnnotationBypasses(t *testing.T) {
	server := &teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv", Annotations: map[string]string{teraskyv1alpha1.AllowForceDeleteAnnotation: "true"}},
	}
	cfg := renameRuleXRDConfig("cfg", "xfoos.example.org")
	cfg.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: "srv"}
	c := newFakeClient(server, cfg).Build()
	v := &ConversionWebhookServerValidator{Client: c}
	if _, err := v.ValidateDelete(context.Background(), server); err != nil {
		t.Fatalf("expected the force-delete annotation to bypass the block, got: %v", err)
	}
}
