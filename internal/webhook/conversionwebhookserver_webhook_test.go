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
	"strings"
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

func sharded(name string, isDefault bool) *teraskyv1alpha1.ConversionWebhookServer {
	s := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: name}}
	s.Spec.Default = isDefault
	s.Spec.Sharding = &teraskyv1alpha1.ShardingSpec{}
	return s
}

// Enabling sharding on a non-default instance while the default stays out
// of the pool would move every unpinned config onto the pool in one
// admission — a fleet-wide reassignment produced by what reads as a local
// change to one object.
func TestShardPool_RejectsADefaultOutsideThePool(t *testing.T) {
	existingDefault := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	existingDefault.Spec.Default = true

	c := newFakeClient(existingDefault).Build()
	v := &ConversionWebhookServerValidator{Client: c}

	_, err := v.ValidateCreate(context.Background(), sharded("shard-b", false))
	if err == nil {
		t.Fatal("expected a pool that excludes the default instance to be rejected")
	}
	for _, want := range []string{"default", "sharding pool", "spec.sharding.enabled"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// The valid ordering: enable it on the default first, which moves nothing
// because the pool is then that one instance, then add the others.
func TestShardPool_AcceptsTheDefaultAsAPoolMember(t *testing.T) {
	c := newFakeClient().Build()
	v := &ConversionWebhookServerValidator{Client: c}
	if _, err := v.ValidateCreate(context.Background(), sharded("default", true)); err != nil {
		t.Fatalf("enabling sharding on the default instance must be accepted: %v", err)
	}

	c = newFakeClient(sharded("default", true)).Build()
	v = &ConversionWebhookServerValidator{Client: c}
	if _, err := v.ValidateCreate(context.Background(), sharded("shard-b", false)); err != nil {
		t.Fatalf("adding a second pool member must be accepted: %v", err)
	}
}

// A fleet with no default at all is legal once a pool exists — the pool is
// a complete answer for an unpinned config.
func TestShardPool_AcceptsAPoolWithNoDefault(t *testing.T) {
	c := newFakeClient(sharded("shard-a", false)).Build()
	v := &ConversionWebhookServerValidator{Client: c}
	if _, err := v.ValidateCreate(context.Background(), sharded("shard-b", false)); err != nil {
		t.Fatalf("a pool with no default instance must be accepted: %v", err)
	}
}

// The check runs against the fleet as it WILL be: turning sharding off on
// the last pool member, or on the default, has to be allowed even though
// the stored copy still says otherwise.
func TestShardPool_JudgesThePostUpdateFleet(t *testing.T) {
	stored := sharded("default", true)
	c := newFakeClient(stored).Build()
	v := &ConversionWebhookServerValidator{Client: c}

	off := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	off.Spec.Default = true
	disabled := false
	off.Spec.Sharding = &teraskyv1alpha1.ShardingSpec{Enabled: &disabled}

	if _, err := v.ValidateUpdate(context.Background(), stored, off); err != nil {
		t.Fatalf("turning sharding off on the last pool member must be accepted: %v", err)
	}
}

func TestShardPool_NoPoolIsAlwaysFine(t *testing.T) {
	existingDefault := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	existingDefault.Spec.Default = true
	c := newFakeClient(existingDefault).Build()
	v := &ConversionWebhookServerValidator{Client: c}
	plain := &teraskyv1alpha1.ConversionWebhookServer{ObjectMeta: metav1.ObjectMeta{Name: "tenant-a"}}
	if _, err := v.ValidateCreate(context.Background(), plain); err != nil {
		t.Fatalf("a fleet with no sharding at all must be unaffected: %v", err)
	}
}
