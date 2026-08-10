/*
Copyright 2026 The xrd-conversion-operator Authors.

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
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	teraskyv1alpha1 "github.com/vrabbi/xrd-conversion-operator/api/v1alpha1"
	"github.com/vrabbi/xrd-conversion-operator/internal/assign"
)

// ConversionWebhookServerValidator validates ConversionWebhookServer
// objects at admission time: at most one instance may be marked default at
// any moment (rather than allowing a second one and relying on the
// controller to merely flag the conflict later), and deletion is blocked
// synchronously — mirroring the controller's finalizer check, but giving
// immediate feedback on `kubectl delete` instead of a silent hang — unless
// the explicit force-delete annotation is present on the object being
// deleted.
type ConversionWebhookServerValidator struct {
	Client client.Client
}

var _ admission.CustomValidator = &ConversionWebhookServerValidator{}

func (v *ConversionWebhookServerValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	server, ok := obj.(*teraskyv1alpha1.ConversionWebhookServer)
	if !ok {
		return nil, fmt.Errorf("expected a ConversionWebhookServer, got %T", obj)
	}
	return nil, v.checkDefault(ctx, server)
}

func (v *ConversionWebhookServerValidator) ValidateUpdate(ctx context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	server, ok := newObj.(*teraskyv1alpha1.ConversionWebhookServer)
	if !ok {
		return nil, fmt.Errorf("expected a ConversionWebhookServer, got %T", newObj)
	}
	return nil, v.checkDefault(ctx, server)
}

func (v *ConversionWebhookServerValidator) checkDefault(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer) error {
	if !server.Spec.Default {
		return nil
	}
	var list teraskyv1alpha1.ConversionWebhookServerList
	if err := v.Client.List(ctx, &list); err != nil {
		return fmt.Errorf("listing existing ConversionWebhookServers: %w", err)
	}
	for _, other := range list.Items {
		if other.Name == server.Name {
			continue
		}
		if other.Spec.Default {
			return fmt.Errorf("ConversionWebhookServer %q is already marked default; only one instance may be default at a time (unset it first, or set spec.default=false here)", other.Name)
		}
	}
	return nil
}

func (v *ConversionWebhookServerValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	server, ok := obj.(*teraskyv1alpha1.ConversionWebhookServer)
	if !ok {
		return nil, fmt.Errorf("expected a ConversionWebhookServer, got %T", obj)
	}
	if server.Annotations[teraskyv1alpha1.AllowForceDeleteAnnotation] == "true" {
		return nil, nil
	}
	var configs teraskyv1alpha1.XRDConversionConfigList
	if err := v.Client.List(ctx, &configs); err != nil {
		return nil, fmt.Errorf("listing XRDConversionConfigs: %w", err)
	}
	var allServers teraskyv1alpha1.ConversionWebhookServerList
	if err := v.Client.List(ctx, &allServers); err != nil {
		return nil, fmt.Errorf("listing ConversionWebhookServers: %w", err)
	}
	dependents := assign.ConfigsAssignedTo(configs.Items, allServers.Items, server.Name)
	if len(dependents) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(dependents))
	for _, c := range dependents {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	suffix := ""
	if server.Spec.Default {
		suffix = " (this is the DEFAULT instance — configs with no explicit webhookServerRef depend on it too)"
	}
	return nil, fmt.Errorf("%d XRDConversionConfig(s) still resolve to this instance%s: %v; reassign them first, or add annotation %q=\"true\" to force", len(dependents), suffix, names, teraskyv1alpha1.AllowForceDeleteAnnotation)
}
