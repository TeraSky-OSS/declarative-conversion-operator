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

// Package v1alpha1 contains the terasky.com/v1alpha1 API: XRDConversionConfig
// and ConversionWebhookServer.
//
// +kubebuilder:object:generate=true
// +groupName=terasky.com
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "terasky.com", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	//
	// This uses apimachinery's runtime.SchemeBuilder directly rather than
	// controller-runtime's pkg/scheme.Builder helper (deprecated as of
	// controller-runtime v0.24): api packages should keep their dependency
	// footprint to the standard library, k8s.io/apimachinery, and other api
	// packages, not controller-runtime.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&XRDConversionConfig{}, &XRDConversionConfigList{},
		&ConversionWebhookServer{}, &ConversionWebhookServerList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
