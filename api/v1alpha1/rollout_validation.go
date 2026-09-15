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
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/intstr"
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
	// Strictly greater, not >=. At equality the drain deadline and the
	// kubelet's SIGKILL deadline expire at the same instant, so whether an
	// in-flight response makes it out is a race rather than a guarantee.
	if need >= have {
		return fmt.Errorf(
			"rollout.terminationGracePeriodSeconds (%ds) is too short: a terminating replica sleeps %ds in its preStop hook and then drains for up to %s, so it needs strictly more than %s. "+
				"As written, the kubelet sends SIGKILL while a ConversionReview is still being answered, and the apiserver reports that as a failed write. "+
				"Raise terminationGracePeriodSeconds, or lower rollout.preStopSleepSeconds or --shutdown-timeout",
			grace, preStop, shutdown, need)
	}

	return validateRolloutStrategy(rollout)
}

// validateRolloutStrategy checks the two IntOrString fields the CRD cannot.
//
// The schema accepts any integer or string for an IntOrString, so a direct
// client can persist -1 or "banana". Both reach
// Deployment.spec.strategy.rollingUpdate verbatim, where the apiserver
// rejects them — and the failure surfaces as a Deployment that will not
// apply, attributed to the controller rather than to the value that caused
// it. maxUnavailable and maxSurge both zero is the same class of problem:
// individually legal, together a strategy Kubernetes refuses and a rollout
// that can never make progress.
func validateRolloutStrategy(rollout *RolloutSpec) error {
	if rollout == nil {
		return nil
	}
	if err := validateIntOrPercent(rollout.MaxUnavailable, "rollout.maxUnavailable"); err != nil {
		return err
	}
	if err := validateIntOrPercent(rollout.MaxSurge, "rollout.maxSurge"); err != nil {
		return err
	}

	// Defaults are 0 and 1, so only an explicit pair can be zero/zero.
	unavailableZero := rollout.MaxUnavailable == nil || isZeroIntOrPercent(*rollout.MaxUnavailable)
	surgeZero := rollout.MaxSurge != nil && isZeroIntOrPercent(*rollout.MaxSurge)
	if unavailableZero && surgeZero {
		return fmt.Errorf(
			"rollout.maxUnavailable and rollout.maxSurge are both zero: Kubernetes rejects that RollingUpdate strategy, "+
				"because it permits neither taking a replica out of service nor adding one, so the rollout can never progress. "+
				"maxUnavailable defaults to 0, so leave maxSurge at its default of 1 or raise one of them (got maxUnavailable=%s, maxSurge=%s)",
			intOrPercentString(rollout.MaxUnavailable, "0"), intOrPercentString(rollout.MaxSurge, "1"))
	}
	return nil
}

func validateIntOrPercent(v *intstr.IntOrString, field string) error {
	if v == nil {
		return nil
	}
	switch v.Type {
	case intstr.Int:
		if v.IntValue() < 0 {
			return fmt.Errorf("%s must not be negative, got %d", field, v.IntValue())
		}
	case intstr.String:
		raw := v.StrVal
		if !strings.HasSuffix(raw, "%") {
			return fmt.Errorf("%s must be an integer or a percentage such as \"25%%\", got %q", field, raw)
		}
		n, err := strconv.Atoi(strings.TrimSuffix(raw, "%"))
		if err != nil {
			return fmt.Errorf("%s is not a valid percentage: %q", field, raw)
		}
		if n < 0 {
			return fmt.Errorf("%s must not be a negative percentage, got %q", field, raw)
		}
	}
	return nil
}

func isZeroIntOrPercent(v intstr.IntOrString) bool {
	if v.Type == intstr.Int {
		return v.IntValue() == 0
	}
	n, err := strconv.Atoi(strings.TrimSuffix(v.StrVal, "%"))
	return err == nil && n == 0
}

func intOrPercentString(v *intstr.IntOrString, whenUnset string) string {
	if v == nil {
		return whenUnset + " (default)"
	}
	return v.String()
}

// shutdownTimeoutFromArgs finds the EFFECTIVE --shutdown-timeout in
// extraArgs, in either the `--flag=value` or `--flag value` form.
//
// Effective means the last occurrence, not the first: Go's flag package
// processes every occurrence and keeps the last, which is the same reason
// ValidateWebhookServerExtraArgs rejects duplicates of managed flags. A
// validator that stopped at the first would approve
// `--shutdown-timeout=10s --shutdown-timeout=120s` against the 10s the server
// is not going to use.
func shutdownTimeoutFromArgs(args []string) (time.Duration, error) {
	effective := DefaultWebhookServerShutdownTimeout
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
		effective = d
		if !strings.Contains(args[i], "=") && i+1 < len(args) {
			i++
		}
	}
	return effective, nil
}
