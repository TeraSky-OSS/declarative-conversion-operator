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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
		Use:   "convctl",
		Short: "CLI for TeraSky's declarative-conversion-operator",
		Long: `convctl is the CLI for TeraSky's declarative-conversion-operator.

It exercises the same conversion engine the operator and webhook server use, so you
can validate and test declarative conversion configs before they ever touch a
cluster. Every command works against either resource type:

  XRDConversionConfig   Crossplane CompositeResourceDefinition version conversions
  CRDConversionConfig   native CustomResourceDefinition version conversions`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newValidateCmd(), newAnalyzeCmd(), newTestCmd(), newDiffCmd(), newConvertCmd(), newSuggestCmd(), newVersionCmd())

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
	var configPath, xrdPath, crdPath, output string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate a conversion config the same way the admission webhook does",
		Long: `Validate an XRDConversionConfig or CRDConversionConfig offline, using the same
static checks the operator's admission webhook runs at apply time.

Without --xrd/--crd, only structural checks on the config itself run. Supply the
matching schema file to also compile every rule against the real hub and spoke
schemas.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := RunValidate(configPath, xrdPath, crdPath)
			if err != nil {
				return err
			}
			if output == "json" {
				return writeJSON(cmd, res)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "config: %s\nstructurally valid: %v\n", res.Config, res.StructurallyValid)
			if xrdPath != "" || crdPath != "" {
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
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to an XRDConversionConfig or CRDConversionConfig YAML file (required)")
	cmd.Flags().StringVarP(&xrdPath, "xrd", "x", "", "Path to an XRD YAML file (optional; enables live schema validation against an XRDConversionConfig)")
	cmd.Flags().StringVar(&crdPath, "crd", "", "Path to a CRD YAML file (optional; enables live schema validation against a CRDConversionConfig)")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "Output format: table|json")
	_ = cmd.MarkFlagRequired("config")
	cmd.MarkFlagsMutuallyExclusive("xrd", "crd")
	return cmd
}

func newAnalyzeCmd() *cobra.Command {
	var xrdPath, crdPath, configPath, output string
	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Report lossiness and rule coverage from schemas alone",
		Long: `Schema-only analysis of an XRDConversionConfig or CRDConversionConfig — no sample
objects required.

Answers whether the config would validate against the target XRD/CRD, which rules
are lossy in which direction, and whether every schema field is covered.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := RunAnalyze(xrdPath, crdPath, configPath)
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
	cmd.Flags().StringVarP(&xrdPath, "xrd", "x", "", "Path to an XRD YAML file (required for an XRDConversionConfig)")
	cmd.Flags().StringVar(&crdPath, "crd", "", "Path to a CRD YAML file (required for a CRDConversionConfig)")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to an XRDConversionConfig or CRDConversionConfig YAML file (required)")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "Output format: table|json")
	_ = cmd.MarkFlagRequired("config")
	cmd.MarkFlagsOneRequired("xrd", "crd")
	cmd.MarkFlagsMutuallyExclusive("xrd", "crd")
	return cmd
}

func newTestCmd() *cobra.Command {
	var (
		xrdPath, crdPath, configPath, samplesDir, output, failOn, outputFile string
		skipIdentity, strict, live                                           bool
		versionPairs                                                         []string
		kubeconfig, kubeContext                                              string
	)
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Round-trip samples through every conversion path",
		Long: `Run every sample through every served-version conversion path and report pass/loss,
timing, and rule coverage.

Works against an XRDConversionConfig (--xrd) or a CRDConversionConfig (--crd). The
config's own kind decides which schema flag is required.

Samples come from exactly one of:

  --samples <dir>  offline fixtures — one YAML file (or multi-doc) per sample object
  --live           every existing instance of the target type, fetched from a cluster
                   at its hub/storage version (a pre-upgrade check against real objects)

--live resolves the cluster the same way kubectl does (--kubeconfig / --context, with
the usual $KUBECONFIG and ~/.kube/config fallbacks). It only needs get/list on the
target resource — no write access, and no access to the operator's own CRDs.

--output selects table (default), json, or junit (for CI test-result reporters).
--output-file writes the full report to a path instead of stdout; a short
pass/loss/fail/error summary still prints to stdout either way.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch output {
			case "table", "json", "junit":
			default:
				return fmt.Errorf("invalid --output value %q (want table, json, or junit)", output)
			}
			rep, err := RunTest(TestOptions{
				XRDPath: xrdPath, CRDPath: crdPath, ConfigPath: configPath, SamplesDir: samplesDir,
				SkipIdentity: skipIdentity, RestrictVersionPairs: versionPairs,
				Live: live, Kubeconfig: kubeconfig, KubeContext: kubeContext,
			})
			if err != nil {
				return err
			}

			var buf bytes.Buffer
			switch output {
			case "json":
				err = writeJSONTo(&buf, rep)
			case "junit":
				err = rep.WriteJUnit(&buf)
			default:
				rep.WriteTable(&buf)
			}
			if err != nil {
				return err
			}

			if outputFile != "" {
				if err := os.WriteFile(outputFile, buf.Bytes(), 0o644); err != nil {
					return fmt.Errorf("writing report to %s: %w", outputFile, err)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "output written to file %s\n", outputFile)
				rep.WriteSummaryLine(cmd.OutOrStdout())
			} else {
				_, _ = cmd.OutOrStdout().Write(buf.Bytes())
			}

			exitCode = decideExitCode(rep, failOn, strict)
			return nil
		},
	}
	cmd.Flags().StringVarP(&xrdPath, "xrd", "x", "", "Path to an XRD YAML file (required for an XRDConversionConfig)")
	cmd.Flags().StringVar(&crdPath, "crd", "", "Path to a CRD YAML file (required for a CRDConversionConfig)")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to an XRDConversionConfig or CRDConversionConfig YAML file (required)")
	cmd.Flags().StringVarP(&samplesDir, "samples", "s", "", "Path to a directory of sample objects")
	cmd.Flags().BoolVar(&live, "live", false, "Fetch samples from a live cluster instead of --samples (a pre-upgrade check against real objects)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig file (default: $KUBECONFIG, then ~/.kube/config); only used with --live")
	cmd.Flags().StringVar(&kubeContext, "context", "", "Kubeconfig context to use (default: the kubeconfig's current-context); only used with --live")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "Output format: table|json|junit")
	cmd.Flags().StringVar(&outputFile, "output-file", "", "Write the full report to this file instead of stdout; a short summary still prints to stdout")
	cmd.Flags().BoolVar(&skipIdentity, "skip-identity", false, "Skip trivial same-version passthrough checks")
	cmd.Flags().BoolVar(&strict, "strict", false, "Escalate warnings (e.g. rule-coverage gaps) to failures")
	cmd.Flags().StringVar(&failOn, "fail-on", "loss", "Exit-code threshold: none|warn|loss")
	cmd.Flags().StringSliceVar(&versionPairs, "version-pair", nil, "Restrict testing to these version(s), repeatable")
	_ = cmd.MarkFlagRequired("config")
	cmd.MarkFlagsOneRequired("xrd", "crd")
	cmd.MarkFlagsMutuallyExclusive("xrd", "crd")
	cmd.MarkFlagsOneRequired("samples", "live")
	cmd.MarkFlagsMutuallyExclusive("samples", "live")
	return cmd
}

func newDiffCmd() *cobra.Command {
	var (
		configPaths                                       []string
		xrdPath, crdPath, output, kubeconfig, kubeContext string
		live                                              bool
	)
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Compare coverage, rule claims, and lossiness between two conversion configs",
		Long: `Analyze two conversion configs against the same schema and report what changed
between them: which hub/spoke fields go from covered to uncovered (or back),
which rules claim which paths, which directions flip between lossless and lossy,
and which errors and warnings appear or disappear.

Pass --config twice to compare two local files. Pass it once with --live to
compare against the cluster instead: the live XRD/CRD supplies the schema, and
the ConversionConfig of the same name supplies the other side. If the cluster
has no such config yet, the comparison runs against an empty rule set for the
same spokes — "what would applying this claim?" rather than an error.

Exits 0 when the two sides are equivalent and 1 when any delta is found, so it
drops straight into a CI gate.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch output {
			case "table", "json":
			default:
				return fmt.Errorf("invalid --output value %q (want table or json)", output)
			}
			out, err := RunDiff(DiffOptions{
				ConfigPaths: configPaths, XRDPath: xrdPath, CRDPath: crdPath,
				Live: live, Kubeconfig: kubeconfig, KubeContext: kubeContext,
			})
			if err != nil {
				return err
			}
			if output == "table" {
				out.WriteTable(cmd.OutOrStdout())
			} else if err := writeJSON(cmd, out); err != nil {
				return err
			}
			if out.HasDeltas {
				exitCode = ExitTestFailure
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&configPaths, "config", "c", nil, "Path to a conversion config YAML file; pass twice to compare two files, or once with --live")
	cmd.Flags().StringVarP(&xrdPath, "xrd", "x", "", "Path to an XRD YAML file (required for two-file mode against XRDConversionConfigs)")
	cmd.Flags().StringVar(&crdPath, "crd", "", "Path to a CRD YAML file (required for two-file mode against CRDConversionConfigs)")
	cmd.Flags().BoolVar(&live, "live", false, "Compare the single --config against the cluster's live schema and applied config")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig file (default: $KUBECONFIG, then ~/.kube/config); only used with --live")
	cmd.Flags().StringVar(&kubeContext, "context", "", "Kubeconfig context to use (default: the kubeconfig's current-context); only used with --live")
	cmd.Flags().StringVarP(&output, "output", "o", "json", "Output format: json|table")
	_ = cmd.MarkFlagRequired("config")
	cmd.MarkFlagsMutuallyExclusive("xrd", "crd")
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
	return writeJSONTo(cmd.OutOrStdout(), v)
}

func writeJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
