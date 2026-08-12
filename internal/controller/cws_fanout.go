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
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/assign"
)

// mapServerToAssignedXRDConfigs returns reconcile requests only for
// XRDConversionConfigs that currently resolve to the changed
// ConversionWebhookServer. Unrelated configs (assigned elsewhere, or
// unresolvable) are skipped so a CWS event does not fan out to the entire
// cluster inventory.
func mapServerToAssignedXRDConfigs(ctx context.Context, c client.Client, server client.Object) ([]reconcile.Request, error) {
	var list teraskyv1alpha1.XRDConversionConfigList
	if err := c.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("listing XRDConversionConfigs for CWS fan-out: %w", err)
	}
	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := c.List(ctx, &servers); err != nil {
		return nil, fmt.Errorf("listing ConversionWebhookServers for CWS fan-out: %w", err)
	}
	assigned := assign.ConfigsAssignedTo(list.Items, servers.Items, server.GetName())
	reqs := make([]reconcile.Request, 0, len(assigned))
	for _, cfg := range assigned {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name}})
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].Name < reqs[j].Name })
	return reqs, nil
}

// mapServerToAssignedCRDConfigs is the CRDConversionConfig counterpart of
// mapServerToAssignedXRDConfigs.
func mapServerToAssignedCRDConfigs(ctx context.Context, c client.Client, server client.Object) ([]reconcile.Request, error) {
	var list teraskyv1alpha1.CRDConversionConfigList
	if err := c.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("listing CRDConversionConfigs for CWS fan-out: %w", err)
	}
	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := c.List(ctx, &servers); err != nil {
		return nil, fmt.Errorf("listing ConversionWebhookServers for CWS fan-out: %w", err)
	}
	assigned := assign.ConfigsAssignedTo(list.Items, servers.Items, server.GetName())
	reqs := make([]reconcile.Request, 0, len(assigned))
	for _, cfg := range assigned {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name}})
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].Name < reqs[j].Name })
	return reqs, nil
}
