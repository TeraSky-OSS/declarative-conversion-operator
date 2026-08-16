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

package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newGenerateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Emit helper manifests from an XRD (never applied)",
		Long: `Print generated helper manifests to stdout. convctl never applies them.

Use this when a GitOps repo should own the YAML — review the draft, commit it,
and let the cluster's controllers act on it.`,
	}
	cmd.AddCommand(newGenerateKyvernoCmd())
	return cmd
}

func newGenerateKyvernoCmd() *cobra.Command {
	var (
		xrdPath, to, from, labelKey  string
		compositionName, migrateName string
		output                       string
	)
	cmd := &cobra.Command{
		Use:   "kyverno",
		Short: "Draft Kyverno MutatingPolicies that retarget XRs onto a new hub Composition",
		Long: `Print two policies.kyverno.io/v1 MutatingPolicies for an XRD that is evolving
its API. Nothing is applied.

  1. A per-XRD Composition labeler. Admission writes xrd-api-version from
     the version element of spec.compositeTypeRef (example.org/v2 → v2).
     That label is never in git. XRD targeting (kind + group) lives in the
     mutation CEL — Kyverno 1.18 silently ignores matchConditions that
     read object.spec. XRDs that never change their API do not need this
     policy — do not generate it for them.
  2. A standing XR migrate policy (one per XRD). Admission (and
     mutateExisting) strips compositionRef and compositionRevisionRef and
     sets compositionSelector.matchLabels to the --to version so Crossplane
     re-selects. Re-generate with the new --from/--to on a hub flip and
     apply the same metadata.name. Admission is required: Kyverno 1.18.1
     never runs MutatingPolicy mutateExisting in the background.

Crossplane pins compositionRef at create time and ignores the selector until
that pin is removed. compositionUpdatePolicy: Automatic only walks revisions
of the already-pinned Composition. Do not use XRD enforcedCompositionRef to
chase hub versions — that field is immutable.

--to must name a version on the XRD. --from, if set, limits the migrate
policy to XRs whose selector is missing or still equals that version
(a canary). Without --from, anything not already labeled --to is migrated.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch output {
			case "yaml", "json":
			default:
				return fmt.Errorf("invalid --output value %q (want yaml or json)", output)
			}
			docs, err := RunGenerateKyverno(GenerateKyvernoOptions{
				XRDPath:               xrdPath,
				To:                    to,
				From:                  from,
				LabelKey:              labelKey,
				CompositionPolicyName: compositionName,
				MigratePolicyName:     migrateName,
			})
			if err != nil {
				return err
			}
			if output == "json" {
				return writeJSON(cmd, docs)
			}
			data, err := encodeKyvernoYAML(docs)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	cmd.Flags().StringVarP(&xrdPath, "xrd", "x", "", "Path to an XRD YAML file (required)")
	cmd.Flags().StringVar(&to, "to", "", "Target xrd-api-version label; must be a version on the XRD (required)")
	cmd.Flags().StringVar(&from, "from", "", "Only migrate XRs whose selector is missing or equals this version (optional canary)")
	cmd.Flags().StringVar(&labelKey, "label-key", defaultXRDAPIVersionLabel, "Label key written on Compositions and XR compositionSelector.matchLabels")
	cmd.Flags().StringVar(&compositionName, "composition-policy-name", "", "Name of the Composition labeler (default label-compositions-<plural>)")
	cmd.Flags().StringVar(&migrateName, "migrate-policy-name", "", "Name of the XR migrate policy (default set-composition-version-selector-<plural>)")
	cmd.Flags().StringVarP(&output, "output", "o", "yaml", "Output format: yaml|json")
	_ = cmd.MarkFlagRequired("xrd")
	_ = cmd.MarkFlagRequired("to")
	registerYAMLFileCompletions(cmd, "xrd")
	registerOutputCompletions(cmd, "yaml", "json")
	return cmd
}
