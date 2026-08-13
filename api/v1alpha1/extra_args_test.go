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

import "testing"

func TestValidateWebhookServerExtraArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "nil", args: nil},
		{name: "optional only", args: []string{"--cert-reload-interval=1m", "--zap-devel=true"}},
		{name: "equals form managed", args: []string{"--webhook-server-name=evil"}, wantErr: true},
		{name: "two-token managed", args: []string{"--tls-cert-dir", "/evil"}, wantErr: true},
		{name: "bool managed bare", args: []string{"--enable-xrd-support=false"}, wantErr: true},
		{name: "cache selector managed", args: []string{"--cache-label-selector=tenant=a"}, wantErr: true},
		{name: "value looks like flag name", args: []string{"--cert-reload-interval", "webhook-server-name"}},
		{name: "mixed optional then managed", args: []string{"--zap-devel=true", "--conversion-bind-address=:1"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateWebhookServerExtraArgs(tc.args)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %v", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %v: %v", tc.args, err)
			}
		})
	}
}
