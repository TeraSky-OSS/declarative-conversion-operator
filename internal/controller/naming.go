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

// Naming conventions for the child resources a ConversionWebhookServer
// reconciles, shared with the XRDConversionConfig reconciler so it can
// locate a server's Service/Certificate Secret without a second CRD
// round-trip.

func cwsDeploymentName(server string) string        { return server + "-webhook-server" }
func cwsServiceName(server string) string           { return server + "-webhook-server" }
func cwsCertificateName(server string) string       { return server + "-webhook-server-cert" }
func cwsCertificateSecretName(server string) string { return server + "-webhook-server-tls" }
func cwsPDBName(server string) string               { return server + "-webhook-server" }
func cwsHPAName(server string) string               { return server + "-webhook-server" }

// FieldOwner is the SSA field manager name used for every patch this
// operator applies to resources it doesn't fully own (XRDs, most notably),
// so its ownership is scoped to exactly the fields it sets and never fights
// other owners of the same object.
const FieldOwner = "declarative-conversion-operator"
