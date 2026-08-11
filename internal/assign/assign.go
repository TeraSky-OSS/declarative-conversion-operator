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

// Package assign implements the single shared rule for "which
// ConversionWebhookServer instance serves this conversion config." It is
// imported identically by the operator's controllers and by every
// webhook-server replica's own local reconciler, so "who serves config X"
// is never ambiguous without a network call — every party computes the
// same answer from the same two lists of objects.
//
// The resolver is generic over ConfigLike so it works identically for
// XRDConversionConfig and CRDConversionConfig — assignment only ever
// depends on spec.webhookServerRef and each ConversionWebhookServer's
// spec.default, neither of which differs between the two config kinds.
package assign

import (
	"fmt"
	"sort"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

// ConfigLike is the minimal surface ResolveAssignment needs from a
// conversion config object. Both XRDConversionConfig and
// CRDConversionConfig satisfy it: GetName comes from the embedded
// metav1.ObjectMeta, and WebhookServerRefField is a small accessor each
// type defines itself (see api/v1alpha1).
type ConfigLike interface {
	GetName() string
	WebhookServerRefField() *teraskyv1alpha1.WebhookServerRef
}

// ResolveAssignment returns the name of the ConversionWebhookServer that
// should serve cfg's conversions: the explicitly referenced instance if
// cfg's webhookServerRef is set, otherwise whichever instance in
// allServers is marked default. Exactly one default is required — zero or
// more than one is a misconfiguration reported as an error, never silently
// resolved by picking one.
func ResolveAssignment[T ConfigLike](cfg T, allServers []teraskyv1alpha1.ConversionWebhookServer) (string, error) {
	if ref := cfg.WebhookServerRefField(); ref != nil && ref.Name != "" {
		name := ref.Name
		for _, s := range allServers {
			if s.Name == name {
				return name, nil
			}
		}
		return "", fmt.Errorf("webhookServerRef %q does not match any existing ConversionWebhookServer", name)
	}

	var defaults []string
	for _, s := range allServers {
		if s.Spec.Default {
			defaults = append(defaults, s.Name)
		}
	}
	switch len(defaults) {
	case 0:
		return "", fmt.Errorf("no ConversionWebhookServer instance is marked default and no explicit webhookServerRef is set")
	case 1:
		return defaults[0], nil
	default:
		sort.Strings(defaults)
		return "", fmt.Errorf("multiple ConversionWebhookServer instances are marked default (%v); this is a misconfiguration that must be fixed before assignment can be resolved", defaults)
	}
}

// IsAssignedTo reports whether cfg resolves (explicitly or via default
// fallback) to the ConversionWebhookServer named serverName. Resolution
// errors (e.g. no default configured) are treated as "not assigned" —
// callers deciding whether it's safe to delete a server should treat an
// unresolvable config as not currently depending on this instance, while
// the config's own controller will separately and loudly report the
// resolution error on its own status.
func IsAssignedTo[T ConfigLike](cfg T, allServers []teraskyv1alpha1.ConversionWebhookServer, serverName string) bool {
	name, err := ResolveAssignment(cfg, allServers)
	return err == nil && name == serverName
}

// ConfigsAssignedTo filters allConfigs down to those that currently
// resolve to the ConversionWebhookServer named serverName, returning
// pointers into the input slice. Used both by the ConversionWebhookServer
// controller (status.assignedConfigs, and the deletion-block check) and by
// each webhook-server replica's own local reconciler (deciding what
// belongs in its in-memory registry).
//
// allConfigs is a slice of values (matching how e.g.
// XRDConversionConfigList.Items is shaped) rather than pointers, so the
// two type parameters: V is the concrete config type, and PV is
// constrained to "*V that also satisfies ConfigLike" — Go infers PV from V
// via the constraint's core type, so callers only ever write
// ConfigsAssignedTo(list.Items, servers, name) with no explicit
// instantiation.
func ConfigsAssignedTo[V any, PV interface {
	*V
	ConfigLike
}](allConfigs []V, allServers []teraskyv1alpha1.ConversionWebhookServer, serverName string) []PV {
	var out []PV
	for i := range allConfigs {
		cfg := PV(&allConfigs[i])
		if IsAssignedTo(cfg, allServers, serverName) {
			out = append(out, cfg)
		}
	}
	return out
}
