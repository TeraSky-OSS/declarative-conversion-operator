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
	"time"
)

// Rollout defaults, mirrored from the kubebuilder markers on RolloutSpec
// and from cmd/webhook-server's --shutdown-timeout. They are repeated here
// because validation has to reason about the combination an unset field
// will actually produce, not about the literal nil.
const (
	DefaultPreStopSleepSeconds int32 = 5
	DefaultGracePeriodSeconds  int64 = 45
	// DefaultWebhookServerShutdownTimeout must stay equal to
	// webhookserver.DefaultShutdownTimeout. It is duplicated rather than
	// imported because api/v1alpha1 is the leaf package every other one
	// depends on, and an import the other way would be a cycle.
	DefaultWebhookServerShutdownTimeout = 30 * time.Second
)

// ValidateWebhookServerRollout rejects a rollout configuration in which the
// kubelet would SIGKILL a replica while it is still answering a
// ConversionReview.
//
// The sequence a terminating pod goes through is:
//
//	preStop sleep  →  SIGTERM  →  graceful drain  →  (grace period ends) SIGKILL
//
// so preStopSleep + shutdownTimeout has to fit inside
// terminationGracePeriodSeconds. It is easy to satisfy and easy to break by
// raising one value in isolation, and when it is broken the symptom is not
// a config error — it is a handful of failed writes during an otherwise
// normal rolling update, attributed to whatever was being written at the
// time.
//
// The shutdown timeout is read from extraArgs when set, because that is the
// only way to change it and a validator that assumed the default would
// approve exactly the configuration it exists to reject.
func ValidateWebhookServerRollout(rollout *RolloutSpec, extraArgs []string) error {
	preStop := DefaultPreStopSleepSeconds
	grace := DefaultGracePeriodSeconds
	if rollout != nil {
		if rollout.PreStopSleepSeconds != nil {
			preStop = *rollout.PreStopSleepSeconds
		}
		if rollout.TerminationGracePeriodSeconds != nil {
			grace = *rollout.TerminationGracePeriodSeconds
		}
	}

	shutdown, err := shutdownTimeoutFromArgs(extraArgs)
	if err != nil {
		return err
	}

	need := time.Duration(preStop)*time.Second + shutdown
	have := time.Duration(grace) * time.Second
	if need > have {
		return fmt.Errorf(
			"rollout.terminationGracePeriodSeconds (%ds) is too short: a terminating replica sleeps %ds in its preStop hook and then drains for up to %s, so it needs at least %s. "+
				"As written, the kubelet sends SIGKILL while a ConversionReview is still being answered, and the apiserver reports that as a failed write. "+
				"Raise terminationGracePeriodSeconds, or lower rollout.preStopSleepSeconds or --shutdown-timeout",
			grace, preStop, shutdown, need)
	}
	return nil
}

// shutdownTimeoutFromArgs finds --shutdown-timeout in extraArgs, in either
// the `--flag=value` or `--flag value` form.
func shutdownTimeoutFromArgs(args []string) (time.Duration, error) {
	for i := 0; i < len(args); i++ {
		name, ok := longFlagName(args[i])
		if !ok {
			continue
		}
		if name != "shutdown-timeout" {
			if !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
			continue
		}
		raw := ""
		if eq := strings.IndexByte(args[i], '='); eq >= 0 {
			raw = args[i][eq+1:]
		} else if i+1 < len(args) {
			raw = args[i+1]
		}
		d, err := time.ParseDuration(raw)
		if err != nil {
			return 0, fmt.Errorf("extraArgs: --shutdown-timeout %q is not a duration: %w", raw, err)
		}
		if d <= 0 {
			return 0, fmt.Errorf("extraArgs: --shutdown-timeout must be positive, got %q", raw)
		}
		return d, nil
	}
	return DefaultWebhookServerShutdownTimeout, nil
}
