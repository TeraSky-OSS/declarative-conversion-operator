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

package assign

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	teraskyv1alpha1 "github.com/vrabbi/declarative-conversion-operator/api/v1alpha1"
)

func server(name string, isDefault bool) teraskyv1alpha1.ConversionWebhookServer {
	return teraskyv1alpha1.ConversionWebhookServer{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       teraskyv1alpha1.ConversionWebhookServerSpec{Default: isDefault},
	}
}

func config(name string, ref *string) teraskyv1alpha1.XRDConversionConfig {
	c := teraskyv1alpha1.XRDConversionConfig{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if ref != nil {
		c.Spec.WebhookServerRef = &teraskyv1alpha1.WebhookServerRef{Name: *ref}
	}
	return c
}

func strp(s string) *string { return &s }

func TestResolveAssignment_ExplicitRef(t *testing.T) {
	servers := []teraskyv1alpha1.ConversionWebhookServer{server("a", true), server("b", false)}
	cfg := config("cfg", strp("b"))
	got, err := ResolveAssignment(&cfg, servers)
	if err != nil || got != "b" {
		t.Fatalf("expected b, got %q err=%v", got, err)
	}
}

func TestResolveAssignment_ExplicitRefMissing(t *testing.T) {
	servers := []teraskyv1alpha1.ConversionWebhookServer{server("a", true)}
	cfg := config("cfg", strp("missing"))
	if _, err := ResolveAssignment(&cfg, servers); err == nil {
		t.Fatalf("expected an error for a missing explicit ref")
	}
}

func TestResolveAssignment_DefaultFallback(t *testing.T) {
	servers := []teraskyv1alpha1.ConversionWebhookServer{server("a", false), server("b", true)}
	cfg := config("cfg", nil)
	got, err := ResolveAssignment(&cfg, servers)
	if err != nil || got != "b" {
		t.Fatalf("expected b, got %q err=%v", got, err)
	}
}

func TestResolveAssignment_NoDefault(t *testing.T) {
	servers := []teraskyv1alpha1.ConversionWebhookServer{server("a", false)}
	cfg := config("cfg", nil)
	if _, err := ResolveAssignment(&cfg, servers); err == nil {
		t.Fatalf("expected an error when no default exists")
	}
}

func TestResolveAssignment_MultipleDefaults(t *testing.T) {
	servers := []teraskyv1alpha1.ConversionWebhookServer{server("a", true), server("b", true)}
	cfg := config("cfg", nil)
	if _, err := ResolveAssignment(&cfg, servers); err == nil {
		t.Fatalf("expected an error when multiple defaults exist")
	}
}

func TestConfigsAssignedTo(t *testing.T) {
	servers := []teraskyv1alpha1.ConversionWebhookServer{server("a", true), server("b", false)}
	cfgs := []teraskyv1alpha1.XRDConversionConfig{
		config("cfg1", nil),       // -> default "a"
		config("cfg2", strp("b")), // -> explicit "b"
		config("cfg3", strp("a")), // -> explicit "a"
	}
	assignedToA := ConfigsAssignedTo(cfgs, servers, "a")
	if len(assignedToA) != 2 {
		t.Fatalf("expected 2 configs assigned to a, got %d", len(assignedToA))
	}
	assignedToB := ConfigsAssignedTo(cfgs, servers, "b")
	if len(assignedToB) != 1 || assignedToB[0].Name != "cfg2" {
		t.Fatalf("expected exactly cfg2 assigned to b, got %+v", assignedToB)
	}
}
