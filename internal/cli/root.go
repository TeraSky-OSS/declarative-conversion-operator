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
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Exit codes, chosen so CI can tell "the config is broken" apart from "the
// invocation was wrong."
const (
	ExitOK          = 0
	ExitTestFailure = 1
	ExitUsageError  = 2
)

// Execute builds and runs the convctl command tree, returning the
// process exit code the caller should use.
func Execute() int {
	root := &cobra.Command{
		Use:           "convctl",
		Short:         "Offline test harness for XRDConversionConfig declarative conversions",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newValidateCmd(), newAnalyzeCmd(), newTestCmd(), newVersionCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return ExitUsageError
	}
	return exitCode
}

// exitCode lets a subcommand's RunE communicate a specific exit code back
// to Execute without cobra's own error-only return path collapsing every
// failure into the same code.
var exitCode = ExitOK

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the convctl version",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), Version)
			return err
		},
	}
}

// Version is set at build time via -ldflags; defaults to "dev" for local
// builds.
var Version = "dev"

func newValidateCmd() *cobra.Command {
	var configPath, xrdPath, output string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Run the same static checks the admission webhook performs, offline",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := RunValidate(configPath, xrdPath)
			if err != nil {
				return err
			}
			if output == "json" {
				return writeJSON(cmd, res)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "config: %s\nstructurally valid: %v\n", res.Config, res.StructurallyValid)
			if xrdPath != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "schema validated: %v\n", res.SchemaValidated)
			}
			for _, e := range res.Errors {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "ERROR: %s\n", e)
			}
			if len(res.Errors) > 0 {
				exitCode = ExitTestFailure
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to an XRDConversionConfig YAML file (required)")
	cmd.Flags().StringVarP(&xrdPath, "xrd", "x", "", "Path to an XRD YAML file (optional; enables live schema validation)")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "Output format: table|json")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

func newAnalyzeCmd() *cobra.Command {
	var xrdPath, configPath, output string
	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Schema-only lossy/coverage analysis, no samples needed",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := RunAnalyze(xrdPath, configPath)
			if err != nil {
				return err
			}
			if output == "json" {
				return writeJSON(cmd, out)
			}
			// A lossless=false result here is informational, not a failure:
			// a non-zero-error config would already have failed above, so
			// reaching this point means any lossy fields were acknowledged.
			out.WriteTable(cmd.OutOrStdout())
			return nil
		},
	}
	cmd.Flags().StringVarP(&xrdPath, "xrd", "x", "", "Path to an XRD YAML file (required)")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to an XRDConversionConfig YAML file (required)")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "Output format: table|json")
	_ = cmd.MarkFlagRequired("xrd")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

func newTestCmd() *cobra.Command {
	var (
		xrdPath, configPath, samplesDir, output, failOn string
		skipIdentity, strict, live                      bool
		versionPairs                                    []string
		kubeconfig, kubeContext                         string
	)
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run every sample through every conversion path and report timing, loss, and rule coverage",
		Long: `Run every sample through every conversion path and report timing, loss, and rule coverage.

Samples come from one of two places, chosen with --samples or --live:

  --samples <dir>  a directory of hand-written sample object YAML files (offline, the default workflow)
  --live           every existing instance of the XRD's generated type, fetched live from a cluster at
                   its hub/storage version — a pre-upgrade check: does a config-to-be-applied hold up
                   against every object that already exists, not just fixtures?

--live resolves the target cluster the same way kubectl does: --kubeconfig (falling back to $KUBECONFIG,
then ~/.kube/config) and --context (falling back to the kubeconfig's current-context). It only needs
get/list on the XRD's generated resource type — no write access, and no access to this operator's own
CRDs or webhook server.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rep, err := RunTest(TestOptions{
				XRDPath: xrdPath, ConfigPath: configPath, SamplesDir: samplesDir,
				SkipIdentity: skipIdentity, RestrictVersionPairs: versionPairs,
				Live: live, Kubeconfig: kubeconfig, KubeContext: kubeContext,
			})
			if err != nil {
				return err
			}
			if output == "json" {
				if err := writeJSON(cmd, rep); err != nil {
					return err
				}
			} else {
				rep.WriteTable(cmd.OutOrStdout())
			}
			exitCode = decideExitCode(rep, failOn, strict)
			return nil
		},
	}
	cmd.Flags().StringVarP(&xrdPath, "xrd", "x", "", "Path to an XRD YAML file (required)")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to an XRDConversionConfig YAML file (required)")
	cmd.Flags().StringVarP(&samplesDir, "samples", "s", "", "Path to a directory of sample objects")
	cmd.Flags().BoolVar(&live, "live", false, "Fetch samples from a live cluster instead of --samples (a pre-upgrade check against real objects)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig file (default: $KUBECONFIG, then ~/.kube/config); only used with --live")
	cmd.Flags().StringVar(&kubeContext, "context", "", "Kubeconfig context to use (default: the kubeconfig's current-context); only used with --live")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "Output format: table|json")
	cmd.Flags().BoolVar(&skipIdentity, "skip-identity", false, "Skip trivial same-version passthrough checks")
	cmd.Flags().BoolVar(&strict, "strict", false, "Escalate warnings (e.g. rule-coverage gaps) to failures")
	cmd.Flags().StringVar(&failOn, "fail-on", "loss", "Exit-code threshold: none|warn|loss")
	cmd.Flags().StringSliceVar(&versionPairs, "version-pair", nil, "Restrict testing to these version(s), repeatable")
	_ = cmd.MarkFlagRequired("xrd")
	_ = cmd.MarkFlagRequired("config")
	cmd.MarkFlagsOneRequired("samples", "live")
	cmd.MarkFlagsMutuallyExclusive("samples", "live")
	return cmd
}

func decideExitCode(rep *Report, failOn string, strict bool) int {
	if failOn == "none" {
		return ExitOK
	}
	if rep.Summary.Errors > 0 || rep.Summary.UnacknowledgedLoss > 0 {
		return ExitTestFailure
	}
	if failOn == "warn" || strict {
		for _, rc := range rep.RuleCoverage {
			if rc.MatchedSamples == 0 {
				return ExitTestFailure
			}
		}
	}
	return ExitOK
}

func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
