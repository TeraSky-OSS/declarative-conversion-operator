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

	"sigs.k8s.io/controller-runtime/pkg/client"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

// validateRegistryKeyAvailable rejects a config whose target resource name
// would collide with another config in the webhook-server registry.
//
// Registry keys are the bare target name (XRD or CRD), shared across both
// config kinds. Same-kind duplicates are rejected here as well as
// cross-kind collisions (e.g. an XRDConversionConfig and a
// CRDConversionConfig both targeting "xfoos.example.org").
//
// skipName/skipKind identify the object being validated so updates of an
// existing config do not collide with themselves. kind must be "xrd" or "crd".
func validateRegistryKeyAvailable(ctx context.Context, c client.Client, targetName, skipName, skipKind string) error {
	if targetName == "" {
		return nil
	}

	var xrds teraskyv1alpha1.XRDConversionConfigList
	if err := c.List(ctx, &xrds); err != nil {
		return fmt.Errorf("listing existing XRDConversionConfigs: %w", err)
	}
	for _, other := range xrds.Items {
		if skipKind == "xrd" && other.Name == skipName {
			continue
		}
		if other.Spec.TargetXRD.Name == targetName {
			return fmt.Errorf("XRDConversionConfig %q already targets %q; registry keys are the bare target name shared by XRD and CRD configs, so only one config per target name is supported", other.Name, targetName)
		}
	}

	var crds teraskyv1alpha1.CRDConversionConfigList
	if err := c.List(ctx, &crds); err != nil {
		return fmt.Errorf("listing existing CRDConversionConfigs: %w", err)
	}
	for _, other := range crds.Items {
		if skipKind == "crd" && other.Name == skipName {
			continue
		}
		if other.Spec.TargetCRD.Name == targetName {
			return fmt.Errorf("CRDConversionConfig %q already targets %q; registry keys are the bare target name shared by XRD and CRD configs, so only one config per target name is supported", other.Name, targetName)
		}
	}
	return nil
}
