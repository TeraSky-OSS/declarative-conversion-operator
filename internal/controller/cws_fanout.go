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

// mapServerToAssignedXRDConfigs returns reconcile requests for
// XRDConversionConfigs currently assigned to server under the live server
// list. Used for Create / Delete / Generic events.
func mapServerToAssignedXRDConfigs(ctx context.Context, c client.Client, server client.Object) ([]reconcile.Request, error) {
	return mapXRDConfigsForServerViews(ctx, c, server.GetName(), serverAsCWS(server))
}

// mapServerTransitionToAssignedXRDConfigs returns the union of configs
// assigned under the prior server view and the current server view. Update
// events must use this so losing `spec.default` (or similar) still enqueues
// configs that previously resolved to the changed server.
func mapServerTransitionToAssignedXRDConfigs(ctx context.Context, c client.Client, oldObj, newObj client.Object) ([]reconcile.Request, error) {
	name := ""
	if newObj != nil {
		name = newObj.GetName()
	} else if oldObj != nil {
		name = oldObj.GetName()
	}
	return mapXRDConfigsForServerViews(ctx, c, name, serverAsCWS(oldObj), serverAsCWS(newObj))
}

func mapServerToAssignedCRDConfigs(ctx context.Context, c client.Client, server client.Object) ([]reconcile.Request, error) {
	return mapCRDConfigsForServerViews(ctx, c, server.GetName(), serverAsCWS(server))
}

func mapServerTransitionToAssignedCRDConfigs(ctx context.Context, c client.Client, oldObj, newObj client.Object) ([]reconcile.Request, error) {
	name := ""
	if newObj != nil {
		name = newObj.GetName()
	} else if oldObj != nil {
		name = oldObj.GetName()
	}
	return mapCRDConfigsForServerViews(ctx, c, name, serverAsCWS(oldObj), serverAsCWS(newObj))
}

func serverAsCWS(obj client.Object) *teraskyv1alpha1.ConversionWebhookServer {
	if obj == nil {
		return nil
	}
	if s, ok := obj.(*teraskyv1alpha1.ConversionWebhookServer); ok {
		return s
	}
	return nil
}

func mapXRDConfigsForServerViews(ctx context.Context, c client.Client, serverName string, views ...*teraskyv1alpha1.ConversionWebhookServer) ([]reconcile.Request, error) {
	var list teraskyv1alpha1.XRDConversionConfigList
	if err := c.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("listing XRDConversionConfigs for CWS fan-out: %w", err)
	}
	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := c.List(ctx, &servers); err != nil {
		return nil, fmt.Errorf("listing ConversionWebhookServers for CWS fan-out: %w", err)
	}

	seen := map[string]struct{}{}
	var reqs []reconcile.Request
	for _, view := range views {
		if view == nil {
			continue
		}
		for _, cfg := range assign.ConfigsAssignedTo(list.Items, serversWithView(servers.Items, view), serverName) {
			if _, ok := seen[cfg.Name]; ok {
				continue
			}
			seen[cfg.Name] = struct{}{}
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name}})
		}
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].Name < reqs[j].Name })
	return reqs, nil
}

func mapCRDConfigsForServerViews(ctx context.Context, c client.Client, serverName string, views ...*teraskyv1alpha1.ConversionWebhookServer) ([]reconcile.Request, error) {
	var list teraskyv1alpha1.CRDConversionConfigList
	if err := c.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("listing CRDConversionConfigs for CWS fan-out: %w", err)
	}
	var servers teraskyv1alpha1.ConversionWebhookServerList
	if err := c.List(ctx, &servers); err != nil {
		return nil, fmt.Errorf("listing ConversionWebhookServers for CWS fan-out: %w", err)
	}

	seen := map[string]struct{}{}
	var reqs []reconcile.Request
	for _, view := range views {
		if view == nil {
			continue
		}
		for _, cfg := range assign.ConfigsAssignedTo(list.Items, serversWithView(servers.Items, view), serverName) {
			if _, ok := seen[cfg.Name]; ok {
				continue
			}
			seen[cfg.Name] = struct{}{}
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name}})
		}
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].Name < reqs[j].Name })
	return reqs, nil
}

// serversWithView returns a copy of live where the named server is replaced
// by view (or appended if missing). If view.Spec.Default is true, other
// servers' Default flags are cleared so assignment reflects the prior
// exclusive-default world before a concurrent update made another server
// the default.
func serversWithView(live []teraskyv1alpha1.ConversionWebhookServer, view *teraskyv1alpha1.ConversionWebhookServer) []teraskyv1alpha1.ConversionWebhookServer {
	out := make([]teraskyv1alpha1.ConversionWebhookServer, 0, len(live)+1)
	found := false
	for _, s := range live {
		if s.Name == view.Name {
			out = append(out, *view)
			found = true
			continue
		}
		cp := s
		if view.Spec.Default {
			cp.Spec.Default = false
		}
		out = append(out, cp)
	}
	if !found {
		out = append(out, *view)
	}
	return out
}
