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
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// ConvertOptions configures RunConvert.
type ConvertOptions struct {
	XRDPath    string
	CRDPath    string
	ConfigPath string
	// SamplePath is a single-document YAML/JSON file holding the object to
	// convert.
	SamplePath string
	// To is the version to convert into.
	To string
	// From overrides the version inferred from the sample's own
	// apiVersion, for objects that don't carry one (or carry a wrong one).
	From string
}

// RunConvert converts one object between two versions through the exact
// same compiled plan the webhook server would use, and returns the
// converted object. Which of XRDPath/CRDPath applies is determined by the
// config's own kind.
func RunConvert(opts ConvertOptions) (map[string]any, error) {
	if opts.To == "" {
		return nil, fmt.Errorf("--to is required: name the version to convert into")
	}
	sample, err := loadSingleSample(opts.SamplePath)
	if err != nil {
		return nil, err
	}
	from := opts.From
	if from == "" {
		from = sample.Version
	}
	if from == "" {
		return nil, fmt.Errorf("%s has no apiVersion to infer the source version from; pass --from", opts.SamplePath)
	}

	kind, err := PeekConfigKind(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	if kind == "CRDConversionConfig" {
		if opts.CRDPath == "" {
			return nil, fmt.Errorf("%s is a CRDConversionConfig; pass its target schema with --crd, not --xrd", opts.ConfigPath)
		}
		crd, err := LoadCRD(opts.CRDPath)
		if err != nil {
			return nil, err
		}
		cfg, err := LoadCRDConfig(opts.ConfigPath)
		if err != nil {
			return nil, err
		}
		report, _, err := runAnalyzeCRD(crd, cfg)
		if err != nil {
			return nil, err
		}
		if report.HasErrors() {
			return nil, fmt.Errorf("configuration is invalid against the CRD schema, cannot convert:%s", summarizeSpokeErrors(report))
		}
		router, err := buildRouterCRD(cfg, report)
		if err != nil {
			return nil, err
		}
		return convertOne(router, sample.Object, from, opts.To, crd.Spec.Group)
	}

	if opts.XRDPath == "" {
		return nil, fmt.Errorf("%s is an XRDConversionConfig; pass its target schema with --xrd, not --crd", opts.ConfigPath)
	}
	xrd, err := LoadXRD(opts.XRDPath)
	if err != nil {
		return nil, err
	}
	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	report, _, err := runAnalyze(xrd, cfg)
	if err != nil {
		return nil, err
	}
	if report.HasErrors() {
		return nil, fmt.Errorf("configuration is invalid against the XRD schema, cannot convert:%s", summarizeSpokeErrors(report))
	}
	router, err := buildRouter(cfg, report)
	if err != nil {
		return nil, err
	}
	group, _, err := xrdResourceInfo(xrd)
	if err != nil {
		return nil, err
	}
	return convertOne(router, sample.Object, from, opts.To, group)
}

// convertOne runs the conversion and stamps the output's apiVersion, which
// the engine deliberately never sets itself: a Plan knows the version names
// it maps between, but only the schema knows the API group they live under.
func convertOne(router *engine.Router, obj map[string]any, from, to, group string) (map[string]any, error) {
	out, err := router.Convert(obj, from, to)
	if err != nil {
		return nil, err
	}
	// Router.Convert returns the input untouched for a same-version
	// request, so copy before stamping rather than mutating the caller's
	// object.
	converted := make(map[string]any, len(out)+1)
	for k, v := range out {
		converted[k] = v
	}
	if group == "" {
		return nil, fmt.Errorf("cannot determine the API group to stamp on the converted object")
	}
	converted["apiVersion"] = group + "/" + to
	return converted, nil
}

// loadSingleSample reads exactly one object from path. A multi-document
// file is rejected rather than silently converting only its first
// document.
func loadSingleSample(path string) (Sample, error) {
	if path == "" {
		return Sample{}, fmt.Errorf("--sample is required: pass the object to convert")
	}
	docs, err := decodeAllDocuments(path)
	if err != nil {
		return Sample{}, fmt.Errorf("reading %s: %w", path, err)
	}
	switch len(docs) {
	case 0:
		return Sample{}, fmt.Errorf("%s contains no objects", path)
	case 1:
	default:
		return Sample{}, fmt.Errorf("%s contains %d documents; convert takes exactly one object", path, len(docs))
	}
	apiVersion, _ := docs[0]["apiVersion"].(string)
	return Sample{File: path, Object: docs[0], Version: versionFromAPIVersion(apiVersion)}, nil
}

func newConvertCmd() *cobra.Command {
	var xrdPath, crdPath, configPath, samplePath, to, from, output string
	cmd := &cobra.Command{
		Use:   "convert",
		Short: "Convert a single object between two versions",
		Long: `Run one object through the conversion config and print the result — the same
compiled plan the webhook server would execute, minus the cluster.

The source version is inferred from the object's own apiVersion (override it with
--from), and the converted output's apiVersion is rewritten to the target version
under the group declared by the XRD or CRD. Spoke-to-spoke conversions route
through the hub exactly as they do in production.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch output {
			case "yaml", "json":
			default:
				return fmt.Errorf("invalid --output value %q (want yaml or json)", output)
			}
			obj, err := RunConvert(ConvertOptions{
				XRDPath: xrdPath, CRDPath: crdPath, ConfigPath: configPath,
				SamplePath: samplePath, To: to, From: from,
			})
			if err != nil {
				return err
			}
			if output == "json" {
				return writeJSON(cmd, obj)
			}
			data, err := sigsyaml.Marshal(obj)
			if err != nil {
				return fmt.Errorf("rendering YAML: %w", err)
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	cmd.Flags().StringVarP(&xrdPath, "xrd", "x", "", "Path to an XRD YAML file (required for an XRDConversionConfig)")
	cmd.Flags().StringVar(&crdPath, "crd", "", "Path to a CRD YAML file (required for a CRDConversionConfig)")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to an XRDConversionConfig or CRDConversionConfig YAML file (required)")
	cmd.Flags().StringVar(&samplePath, "sample", "", "Path to a single-document YAML file holding the object to convert (required)")
	cmd.Flags().StringVar(&to, "to", "", "Version to convert into (required)")
	cmd.Flags().StringVar(&from, "from", "", "Source version (default: inferred from the sample's own apiVersion)")
	cmd.Flags().StringVarP(&output, "output", "o", "yaml", "Output format: yaml|json")
	_ = cmd.MarkFlagRequired("config")
	_ = cmd.MarkFlagRequired("sample")
	_ = cmd.MarkFlagRequired("to")
	cmd.MarkFlagsOneRequired("xrd", "crd")
	cmd.MarkFlagsMutuallyExclusive("xrd", "crd")
	return cmd
}
