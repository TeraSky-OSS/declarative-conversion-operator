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
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/crdadapter"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// RehubOptions configures RunRehub.
type RehubOptions struct {
	XRDPath      string
	CRDPath      string
	ConfigPath   string
	To           string
	AllowInvalid bool
	Output       string // yaml|json
}

// RunRehub rewrites a conversion config around a new hub version and returns
// the drafted same-kind object. It never applies anything to a cluster.
// --to must already be a spoke in the input config.
func RunRehub(opts RehubOptions) (any, error) {
	if opts.To == "" {
		return nil, fmt.Errorf("--to is required: name the version that becomes the new hub")
	}
	if opts.XRDPath != "" && opts.CRDPath != "" {
		return nil, fmt.Errorf("--xrd and --crd are mutually exclusive")
	}
	kind, err := PeekConfigKind(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "CRDConversionConfig":
		if opts.CRDPath == "" {
			return nil, fmt.Errorf("%s is a CRDConversionConfig; pass its target schema with --crd, not --xrd", opts.ConfigPath)
		}
		if opts.XRDPath != "" {
			return nil, fmt.Errorf("%s is a CRDConversionConfig; use --crd, not --xrd", opts.ConfigPath)
		}
		return runRehubCRD(opts)
	default:
		if opts.XRDPath == "" {
			return nil, fmt.Errorf("%s is an XRDConversionConfig; pass its target schema with --xrd, not --crd", opts.ConfigPath)
		}
		if opts.CRDPath != "" {
			return nil, fmt.Errorf("%s is an XRDConversionConfig; use --xrd, not --crd", opts.ConfigPath)
		}
		return runRehubXRD(opts)
	}
}

func runRehubXRD(opts RehubOptions) (*teraskyv1alpha1.XRDConversionConfig, error) {
	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	xrd, err := LoadXRD(opts.XRDPath)
	if err != nil {
		return nil, err
	}
	leaves, err := versionLeafSetsXRD(xrd)
	if err != nil {
		return nil, err
	}
	newHub, spokes, err := teraskyv1alpha1.RehubSpokes(cfg.Spec.HubVersion, opts.To, cfg.Spec.Spokes, leaves)
	if err != nil {
		return nil, err
	}
	out := cfg.DeepCopy()
	out.Spec.HubVersion = newHub
	out.Spec.Spokes = spokes
	out.Status = teraskyv1alpha1.XRDConversionConfigStatus{}

	report, err := analyzeRehubDraft(xrdadapter.New(xrd), out.Spec.HubVersion, out)
	if err != nil {
		return nil, err
	}
	if report.HasErrors() && !opts.AllowInvalid {
		return nil, fmt.Errorf("rehub draft is Invalid against the XRD schema (pass --allow-invalid to print anyway):%s", summarizeSpokeErrors(report))
	}
	return out, nil
}

func runRehubCRD(opts RehubOptions) (*teraskyv1alpha1.CRDConversionConfig, error) {
	cfg, err := LoadCRDConfig(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	crd, err := LoadCRD(opts.CRDPath)
	if err != nil {
		return nil, err
	}
	leaves, err := versionLeafSetsCRD(crd)
	if err != nil {
		return nil, err
	}
	newHub, spokes, err := teraskyv1alpha1.RehubSpokes(cfg.Spec.HubVersion, opts.To, cfg.Spec.Spokes, leaves)
	if err != nil {
		return nil, err
	}
	out := cfg.DeepCopy()
	out.Spec.HubVersion = newHub
	out.Spec.Spokes = spokes
	out.Status = teraskyv1alpha1.CRDConversionConfigStatus{}

	report, err := analyzeRehubDraft(crdadapter.New(crd), out.Spec.HubVersion, out)
	if err != nil {
		return nil, err
	}
	if report.HasErrors() && !opts.AllowInvalid {
		return nil, fmt.Errorf("rehub draft is Invalid against the CRD schema (pass --allow-invalid to print anyway):%s", summarizeSpokeErrors(report))
	}
	return out, nil
}

// ruleSetProvider is satisfied by both conversion config kinds via ToRuleSets.
type ruleSetProvider interface {
	ToRuleSets() ([]engine.RuleSet, error)
}

// analyzeRehubDraft runs engine.Analyze on a draft whose hubVersion may not
// yet match the schema's storage/referenceable flag (the documented promote
// sequence flips those independently). We force the draft hub to look like
// storage so Analyze still grades rule coverage and lossiness.
func analyzeRehubDraft(source engine.SchemaSource, hub string, cfg ruleSetProvider) (engine.AnalyzeReport, error) {
	ruleSets, err := cfg.ToRuleSets()
	if err != nil {
		return engine.AnalyzeReport{}, fmt.Errorf("invalid rule configuration: %w", err)
	}
	report, err := engine.Analyze(engine.AnalyzeInput{
		Source:     forceHubStorage{inner: source, hub: hub},
		HubVersion: hub,
		Spokes:     ruleSets,
	})
	if err != nil {
		return engine.AnalyzeReport{}, fmt.Errorf("analysis failed: %w", err)
	}
	return report, nil
}

// forceHubStorage wraps a SchemaSource so Analyze treats hub as the storage
// version without mutating the caller's on-disk XRD/CRD.
type forceHubStorage struct {
	inner engine.SchemaSource
	hub   string
}

func (f forceHubStorage) Versions() ([]engine.VersionSchema, error) {
	vs, err := f.inner.Versions()
	if err != nil {
		return nil, err
	}
	out := make([]engine.VersionSchema, len(vs))
	copy(out, vs)
	for i := range out {
		out[i].Storage = out[i].Name == f.hub
	}
	return out, nil
}

func (f forceHubStorage) Describe() engine.ResourceDescriptor {
	return f.inner.Describe()
}

// stripStatus remarshals a typed config to a map and drops status so draft
// YAML does not emit an empty status: {} block.
func stripStatus(obj any) (map[string]any, error) {
	data, err := sigsyaml.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("marshaling draft: %w", err)
	}
	var m map[string]any
	if err := sigsyaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("preparing draft output: %w", err)
	}
	delete(m, "status")
	return m, nil
}

func versionLeafSetsXRD(xrd *unstructured.Unstructured) (map[string]map[string]bool, error) {
	versions, err := xrdadapter.New(xrd).Versions()
	if err != nil {
		return nil, fmt.Errorf("reading XRD versions: %w", err)
	}
	return leafSetsFromVersions(versions), nil
}

func versionLeafSetsCRD(crd *extv1.CustomResourceDefinition) (map[string]map[string]bool, error) {
	versions, err := crdadapter.New(crd).Versions()
	if err != nil {
		return nil, fmt.Errorf("reading CRD versions: %w", err)
	}
	return leafSetsFromVersions(versions), nil
}

func leafSetsFromVersions(versions []engine.VersionSchema) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(versions))
	for _, v := range versions {
		leaves := map[string]bool{}
		for _, l := range engine.FlattenSchema(v.Schema) {
			leaves[l.Path.String()] = true
		}
		out[v.Name] = leaves
	}
	return out
}

func newRehubCmd() *cobra.Command {
	var (
		configPath, xrdPath, crdPath, to, output string
		allowInvalid                             bool
	)
	cmd := &cobra.Command{
		Use:   "rehub",
		Short: "Draft a conversion config rewritten around a new hub version",
		Long: `Rewrite an XRDConversionConfig or CRDConversionConfig so --to becomes the hub.

--to must already be a spoke in the input (add it as a spoke first, then promote).
The command prints a same-kind YAML/JSON draft to stdout and never applies it.

What it does:
  1. Sets hubVersion to --to
  2. Drops the --to spoke entry
  3. Adds the old hub as a spoke whose rules are the invert of the old --to rules
  4. Composes every other spoke through the old-hub → new-hub path mapping

Hub promotion is not a mechanical path swap — remaining spokes are re-expressed
(often composed) against the new hub. Fail-closed: if a rule cannot be rewritten
safely, rehub errors instead of emitting a silently wrong config.

The draft is run through engine.Analyze; Invalid drafts are refused unless
--allow-invalid is set. Always review the output, then convctl validate / test
before applying.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch output {
			case "yaml", "json":
			default:
				return fmt.Errorf("invalid --output value %q (want yaml or json)", output)
			}
			out, err := RunRehub(RehubOptions{
				XRDPath: xrdPath, CRDPath: crdPath, ConfigPath: configPath,
				To: to, AllowInvalid: allowInvalid, Output: output,
			})
			if err != nil {
				return err
			}
			draft, err := stripStatus(out)
			if err != nil {
				return err
			}
			if output == "json" {
				return writeJSON(cmd, draft)
			}
			data, err := sigsyaml.Marshal(draft)
			if err != nil {
				return fmt.Errorf("marshaling draft: %w", err)
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to an XRDConversionConfig or CRDConversionConfig YAML file (required)")
	cmd.Flags().StringVarP(&xrdPath, "xrd", "x", "", "Path to an XRD YAML file (required for an XRDConversionConfig)")
	cmd.Flags().StringVar(&crdPath, "crd", "", "Path to a CRD YAML file (required for a CRDConversionConfig)")
	cmd.Flags().StringVar(&to, "to", "", "Version that becomes the new hub (must already be a spoke)")
	cmd.Flags().StringVarP(&output, "output", "o", "yaml", "Output format: yaml|json")
	cmd.Flags().BoolVar(&allowInvalid, "allow-invalid", false, "Print the draft even if Analyze reports it Invalid")
	_ = cmd.MarkFlagRequired("config")
	_ = cmd.MarkFlagRequired("to")
	cmd.MarkFlagsOneRequired("xrd", "crd")
	cmd.MarkFlagsMutuallyExclusive("xrd", "crd")
	registerOfflineFlagCompletions(cmd)
	registerOutputCompletions(cmd, "yaml", "json")
	return cmd
}
