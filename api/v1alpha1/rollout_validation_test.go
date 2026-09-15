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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/intstr"
)

func i32(v int32) *int32                           { return &v }
func i64(v int64) *int64                           { return &v }
func ios(v intstr.IntOrString) *intstr.IntOrString { return &v }

func TestValidateWebhookServerRollout(t *testing.T) {
	cases := []struct {
		name      string
		rollout   *RolloutSpec
		extraArgs []string
		wantErr   string
	}{
		{
			name: "defaults fit",
			// 5s preStop + 30s drain = 35s, inside the 45s default grace.
			rollout: nil,
		},
		{
			// The realistic way this breaks: somebody raises the preStop
			// sleep to be safer and makes the pod strictly less safe.
			name:    "a longer preStop overruns the grace period",
			rollout: &RolloutSpec{PreStopSleepSeconds: i32(20)},
			wantErr: "terminationGracePeriodSeconds (45s) is too short",
		},
		{
			name:    "a longer preStop with a matching grace period is fine",
			rollout: &RolloutSpec{PreStopSleepSeconds: i32(20), TerminationGracePeriodSeconds: i64(60)},
		},
		{
			name:    "shortening the grace period alone",
			rollout: &RolloutSpec{TerminationGracePeriodSeconds: i64(20)},
			wantErr: "needs strictly more than 35s",
		},
		{
			// The validator has to read the flag, not assume the default,
			// or it approves exactly the configuration it exists to reject.
			name:      "a longer drain via extraArgs",
			rollout:   nil,
			extraArgs: []string{"--shutdown-timeout=120s"},
			wantErr:   "needs strictly more than 2m5s",
		},
		{
			name:      "a longer drain with a matching grace period",
			rollout:   &RolloutSpec{TerminationGracePeriodSeconds: i64(200)},
			extraArgs: []string{"--shutdown-timeout", "120s"},
		},
		{
			name:      "an unparseable shutdown timeout",
			extraArgs: []string{"--shutdown-timeout=soon"},
			wantErr:   "is not a duration",
		},
		{
			name:      "a non-positive shutdown timeout",
			extraArgs: []string{"--shutdown-timeout=0s"},
			wantErr:   "must be positive",
		},
		{
			// A flag that merely takes a value must not be mistaken for
			// the one being looked for.
			name:      "an unrelated flag whose value looks like a flag name",
			extraArgs: []string{"--cert-reload-interval", "1m", "--zap-devel=true"},
		},
		{
			name:    "preStop disabled leaves only the drain",
			rollout: &RolloutSpec{PreStopSleepSeconds: i32(0), TerminationGracePeriodSeconds: i64(31)},
		},
		{
			// At equality the drain deadline and the kubelet's SIGKILL
			// deadline expire together, so delivery of the final response
			// is a race. Rejected on purpose.
			name:    "exactly equal is not enough",
			rollout: &RolloutSpec{PreStopSleepSeconds: i32(0), TerminationGracePeriodSeconds: i64(30)},
			wantErr: "strictly more than 30s",
		},
		{
			// Go's flag package keeps the LAST occurrence, so validating
			// the first approves a value the server will not use.
			name:      "the last --shutdown-timeout wins",
			extraArgs: []string{"--shutdown-timeout=10s", "--shutdown-timeout=120s"},
			wantErr:   "strictly more than 2m5s",
		},
		{
			name:      "the last --shutdown-timeout wins, space-separated",
			rollout:   &RolloutSpec{TerminationGracePeriodSeconds: i64(200)},
			extraArgs: []string{"--shutdown-timeout", "120s", "--shutdown-timeout", "20s"},
		},
		{
			name:    "maxUnavailable and maxSurge both zero cannot progress",
			rollout: &RolloutSpec{MaxSurge: ios(intstr.FromInt32(0))},
			wantErr: "both zero",
		},
		{
			name:    "an explicit zero/zero pair",
			rollout: &RolloutSpec{MaxUnavailable: ios(intstr.FromInt32(0)), MaxSurge: ios(intstr.FromInt32(0))},
			wantErr: "both zero",
		},
		{
			name:    "zero percent counts as zero",
			rollout: &RolloutSpec{MaxSurge: ios(intstr.FromString("0%"))},
			wantErr: "both zero",
		},
		{
			name:    "the shipped defaults are not zero/zero",
			rollout: &RolloutSpec{},
		},
		{
			name:    "a negative maxSurge",
			rollout: &RolloutSpec{MaxSurge: ios(intstr.FromInt32(-1))},
			wantErr: "must not be negative",
		},
		{
			name:    "a maxUnavailable that is neither an integer nor a percentage",
			rollout: &RolloutSpec{MaxUnavailable: ios(intstr.FromString("banana"))},
			wantErr: "integer or a percentage",
		},
		{
			name:    "a negative percentage",
			rollout: &RolloutSpec{MaxSurge: ios(intstr.FromString("-10%"))},
			wantErr: "negative percentage",
		},
		{
			name:    "a valid percentage pair",
			rollout: &RolloutSpec{MaxUnavailable: ios(intstr.FromString("25%")), MaxSurge: ios(intstr.FromString("50%"))},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWebhookServerRollout(tc.rollout, tc.extraArgs)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// The duplicated constant is load-bearing: api/v1alpha1 cannot import
// internal/webhookserver without a cycle, so a drift between the two is
// only caught if something says so out loud.
func TestRolloutDefaultsAreInternallyConsistent(t *testing.T) {
	need := int64(DefaultPreStopSleepSeconds) + int64(DefaultWebhookServerShutdownTimeout.Seconds())
	if need > DefaultGracePeriodSeconds {
		t.Fatalf("the shipped defaults do not satisfy their own rule: %ds needed, %ds granted", need, DefaultGracePeriodSeconds)
	}
}
