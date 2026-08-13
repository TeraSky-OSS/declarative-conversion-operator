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

package v1alpha1

import (
	"fmt"
	"strings"
)

// operatorManagedWebhookServerFlagNames are flags the ConversionWebhookServer
// controller always injects. ExtraArgs must not override them — Go's flag
// package keeps the last value, so a later duplicate would silently break
// identity, TLS, bind addresses, or feature wiring.
var operatorManagedWebhookServerFlagNames = map[string]struct{}{
	"webhook-server-name":     {},
	"tls-cert-dir":            {},
	"conversion-bind-address": {},
	"metrics-bind-address":    {},
	"enable-xrd-support":      {},
	"enable-crd-support":      {},
	"cache-label-selector":    {},
}

// ValidateWebhookServerExtraArgs rejects ExtraArgs entries that name an
// operator-managed webhook-server flag. Both `--flag=value` and `--flag value`
// forms are detected; other optional flags pass through unchanged.
func ValidateWebhookServerExtraArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		name, ok := longFlagName(args[i])
		if !ok {
			continue
		}
		if _, managed := operatorManagedWebhookServerFlagNames[name]; managed {
			return fmt.Errorf("extraArgs must not override operator-managed flag %q", name)
		}
		// `--flag value` (no '='): skip the following value token so it is
		// not misread as another flag name.
		if !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
		}
	}
	return nil
}

func longFlagName(arg string) (string, bool) {
	if !strings.HasPrefix(arg, "--") {
		return "", false
	}
	name := strings.TrimPrefix(arg, "--")
	if name == "" {
		return "", false
	}
	if eq := strings.IndexByte(name, '='); eq >= 0 {
		name = name[:eq]
	}
	return name, name != ""
}
