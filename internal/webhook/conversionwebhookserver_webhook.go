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
	"fmt"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/assign"
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

var _ admission.Validator[*teraskyv1alpha1.ConversionWebhookServer] = &ConversionWebhookServerValidator{}

func (v *ConversionWebhookServerValidator) ValidateCreate(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer) (admission.Warnings, error) {
	if err := teraskyv1alpha1.ValidateWebhookServerExtraArgs(server.Spec.ExtraArgs); err != nil {
		return nil, err
	}
	if err := teraskyv1alpha1.ValidateWebhookServerRollout(server.Spec.Rollout, server.Spec.ExtraArgs); err != nil {
		return nil, err
	}
	if err := v.checkDefault(ctx, server); err != nil {
		return nil, err
	}
	return nil, v.checkShardPool(ctx, server)
}

func (v *ConversionWebhookServerValidator) ValidateUpdate(ctx context.Context, _, newServer *teraskyv1alpha1.ConversionWebhookServer) (admission.Warnings, error) {
	if err := teraskyv1alpha1.ValidateWebhookServerExtraArgs(newServer.Spec.ExtraArgs); err != nil {
		return nil, err
	}
	if err := teraskyv1alpha1.ValidateWebhookServerRollout(newServer.Spec.Rollout, newServer.Spec.ExtraArgs); err != nil {
		return nil, err
	}
	if err := v.checkDefault(ctx, newServer); err != nil {
		return nil, err
	}
	return nil, v.checkShardPool(ctx, newServer)
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

// checkShardPool enforces the one invariant automatic assignment needs:
// while any instance opts into sharding, the instance marked default must
// be one of them.
//
// Without it, enabling sharding on a single non-default instance would
// move every unpinned config onto that one instance in a single admission
// — a fleet-wide reassignment triggered by what looks like a local change.
// The pool takes precedence over spec.default precisely so that a
// half-configured pool cannot leave unpinned configs unserved, and this
// check is what makes that precedence safe to have.
//
// There is always a valid ordering: enable sharding on the default
// instance first, then on the others. Doing it that way moves nothing on
// the first step, and a bounded share on each one after.
func (v *ConversionWebhookServerValidator) checkShardPool(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer) error {
	var list teraskyv1alpha1.ConversionWebhookServerList
	if err := v.Client.List(ctx, &list); err != nil {
		return fmt.Errorf("listing existing ConversionWebhookServers: %w", err)
	}

	// The incoming object replaces its stored copy, so the check is made
	// against the fleet as it will be, not as it is.
	fleet := make([]teraskyv1alpha1.ConversionWebhookServer, 0, len(list.Items)+1)
	found := false
	for _, other := range list.Items {
		if other.Name == server.Name {
			fleet = append(fleet, *server)
			found = true
			continue
		}
		fleet = append(fleet, other)
	}
	if !found {
		fleet = append(fleet, *server)
	}

	pool := assign.ShardPool(fleet)
	if len(pool) == 0 {
		return nil
	}
	var defaultName string
	for _, s := range fleet {
		if s.Spec.Default {
			defaultName = s.Name
			break
		}
	}
	// No default at all is legal once a pool exists: the pool is what
	// answers for unpinned configs, so nothing is left unresolved.
	if defaultName == "" {
		return nil
	}
	for _, s := range pool {
		if s.Name == defaultName {
			return nil
		}
	}
	names := make([]string, 0, len(pool))
	for _, s := range pool {
		names = append(names, s.Name)
	}
	return fmt.Errorf("ConversionWebhookServer %q is marked default but is not in the sharding pool %v; "+
		"while a pool exists it, not spec.default, serves configs with no explicit webhookServerRef, so this would move every unpinned config off %q at once. "+
		"Set spec.sharding.enabled on %q as well (do that first when building a pool), or unset spec.default",
		defaultName, names, defaultName, defaultName)
}

func (v *ConversionWebhookServerValidator) ValidateDelete(ctx context.Context, server *teraskyv1alpha1.ConversionWebhookServer) (admission.Warnings, error) {
	if server.Annotations[teraskyv1alpha1.AllowForceDeleteAnnotation] == "true" {
		return nil, nil
	}
	var xrdConfigs teraskyv1alpha1.XRDConversionConfigList
	if err := v.Client.List(ctx, &xrdConfigs); err != nil {
		return nil, fmt.Errorf("listing XRDConversionConfigs: %w", err)
	}
	var crdConfigs teraskyv1alpha1.CRDConversionConfigList
	if err := v.Client.List(ctx, &crdConfigs); err != nil {
		return nil, fmt.Errorf("listing CRDConversionConfigs: %w", err)
	}
	var allServers teraskyv1alpha1.ConversionWebhookServerList
	if err := v.Client.List(ctx, &allServers); err != nil {
		return nil, fmt.Errorf("listing ConversionWebhookServers: %w", err)
	}
	// ServedBy rather than IsAssignedTo: mid-handover a config resolves to
	// its destination while its target still points here, and this
	// instance is still the one answering for it. The controller's
	// finalizer check uses the same rule, so admission and reconcile
	// cannot disagree about whether a delete is safe.
	dependentXRD := assign.ConfigsServedBy(xrdConfigs.Items, allServers.Items, server.Name)
	dependentCRD := assign.ConfigsServedBy(crdConfigs.Items, allServers.Items, server.Name)
	total := len(dependentXRD) + len(dependentCRD)
	if total == 0 {
		return nil, nil
	}
	names := make([]string, 0, total)
	for _, c := range dependentXRD {
		names = append(names, c.Name)
	}
	for _, c := range dependentCRD {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	suffix := ""
	if server.Spec.Default {
		suffix = " (this is the DEFAULT instance — configs with no explicit webhookServerRef depend on it too)"
	}
	return nil, fmt.Errorf("%d config(s) still resolve to this instance, or still have their target pointed at it%s: %v; reassign them first, or add annotation %q=\"true\" to force", total, suffix, names, teraskyv1alpha1.AllowForceDeleteAnnotation)
}
