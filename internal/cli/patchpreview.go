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
	"encoding/base64"
	"fmt"

	"github.com/spf13/cobra"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/terasky-oss/declarative-conversion-operator/internal/conversionpatch"
)

// PatchPreviewOptions configures RunPatchPreview.
type PatchPreviewOptions struct {
	ConfigPath string
	// XRDPath and CRDPath are optional: supplying one runs the same schema
	// validation `convctl analyze` does before rendering the patch, so a
	// config that could never be applied doesn't get a preview of being
	// applied.
	XRDPath string
	CRDPath string

	ServiceName      string
	ServiceNamespace string
	// Path defaults to "/convert/<target name>", the route the operator
	// derives from the config's target.
	Path string
	Port int32
	// CABundle is accepted either base64-encoded (as it appears in a CRD's
	// YAML) or as raw PEM, which is detected and encoded.
	CABundle string
	PlanHash string
}

// RunPatchPreview renders the exact server-side-apply object the operator
// would send to the target XRD or CRD. It is strictly offline: no client is
// ever constructed, so it cannot touch a cluster even by accident.
func RunPatchPreview(opts PatchPreviewOptions) ([]byte, error) {
	if opts.ServiceName == "" || opts.ServiceNamespace == "" {
		return nil, fmt.Errorf("--service-name and --service-namespace are required: patch-preview never contacts a cluster to look them up")
	}
	if opts.CABundle == "" {
		return nil, fmt.Errorf("--ca-bundle is required: the patch the operator applies always carries one")
	}
	caBundle := normalizeCABundle(opts.CABundle)
	port := opts.Port
	if port == 0 {
		port = 443
	}

	kind, err := PeekConfigKind(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	if kind == "CRDConversionConfig" {
		cfg, err := LoadCRDConfig(opts.ConfigPath)
		if err != nil {
			return nil, err
		}
		if opts.XRDPath != "" {
			return nil, fmt.Errorf("%s is a CRDConversionConfig; pass its target schema with --crd, not --xrd", opts.ConfigPath)
		}
		if opts.CRDPath != "" {
			crd, err := LoadCRD(opts.CRDPath)
			if err != nil {
				return nil, err
			}
			report, _, err := runAnalyzeCRD(crd, cfg)
			if err != nil {
				return nil, err
			}
			if report.HasErrors() {
				return nil, fmt.Errorf("configuration is invalid against the CRD schema, the operator would never apply this patch:%s", summarizeSpokeErrors(report))
			}
		}
		params := conversionpatch.Params{
			TargetName: cfg.Spec.TargetCRD.Name, ConfigName: cfg.Name, PlanHash: opts.PlanHash,
			ServiceName: opts.ServiceName, ServiceNamespace: opts.ServiceNamespace,
			Path: orDefault(opts.Path, "/convert/"+cfg.Spec.TargetCRD.Name), Port: port,
			CABundle: caBundle, ReviewVersions: reviewVersionsOrDefault(cfg.Spec.ConversionReviewVersions),
		}
		patch, err := conversionpatch.BuildCRDConversionPatch(params)
		if err != nil {
			return nil, err
		}
		return marshalPatch(patch)
	}

	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	if opts.CRDPath != "" {
		return nil, fmt.Errorf("%s is an XRDConversionConfig; pass its target schema with --xrd, not --crd", opts.ConfigPath)
	}
	if opts.XRDPath != "" {
		xrd, err := LoadXRD(opts.XRDPath)
		if err != nil {
			return nil, err
		}
		report, _, err := runAnalyze(xrd, cfg)
		if err != nil {
			return nil, err
		}
		if report.HasErrors() {
			return nil, fmt.Errorf("configuration is invalid against the XRD schema, the operator would never apply this patch:%s", summarizeSpokeErrors(report))
		}
	}
	patch := conversionpatch.BuildXRDConversionPatch(conversionpatch.Params{
		TargetName: cfg.Spec.TargetXRD.Name, ConfigName: cfg.Name, PlanHash: opts.PlanHash,
		ServiceName: opts.ServiceName, ServiceNamespace: opts.ServiceNamespace,
		Path: orDefault(opts.Path, "/convert/"+cfg.Spec.TargetXRD.Name), Port: port,
		CABundle: caBundle, ReviewVersions: reviewVersionsOrDefault(cfg.Spec.ConversionReviewVersions),
	})
	return marshalPatch(patch.Object)
}

func marshalPatch(v any) ([]byte, error) {
	data, err := sigsyaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("rendering YAML: %w", err)
	}
	return data, nil
}

// normalizeCABundle accepts the bundle either already base64-encoded or as
// raw PEM. PEM's "-----BEGIN" armor isn't in the standard base64 alphabet,
// so a successful decode is a reliable signal the input was already
// encoded.
func normalizeCABundle(v string) string {
	if _, err := base64.StdEncoding.DecodeString(v); err == nil {
		return v
	}
	return base64.StdEncoding.EncodeToString([]byte(v))
}

func reviewVersionsOrDefault(vs []string) []string {
	if len(vs) == 0 {
		return []string{"v1"}
	}
	return vs
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func newPatchPreviewCmd() *cobra.Command {
	var opts PatchPreviewOptions
	cmd := &cobra.Command{
		Use:   "patch-preview",
		Short: "Print the server-side-apply patch the operator would send to the target",
		Long: `Render the exact object the operator server-side-applies onto the target XRD or
CRD to point its spec.conversion at a webhook server — built by the same code the
controllers call, not a reimplementation that could drift from it.

This is a fully offline dry run. No Kubernetes client is ever constructed, which
is why the service coordinates and CA bundle are flags rather than cluster
lookups. Supply --xrd or --crd to additionally validate the config against its
schema first, so a config the operator would refuse to apply doesn't get a
preview of being applied.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := RunPatchPreview(opts)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	cmd.Flags().StringVarP(&opts.ConfigPath, "config", "c", "", "Path to an XRDConversionConfig or CRDConversionConfig YAML file (required)")
	cmd.Flags().StringVarP(&opts.XRDPath, "xrd", "x", "", "Path to an XRD YAML file (optional; validates the config against its schema first)")
	cmd.Flags().StringVar(&opts.CRDPath, "crd", "", "Path to a CRD YAML file (optional; validates the config against its schema first)")
	cmd.Flags().StringVar(&opts.ServiceName, "service-name", "", "Name of the webhook server Service (required)")
	cmd.Flags().StringVar(&opts.ServiceNamespace, "service-namespace", "", "Namespace of the webhook server Service (required)")
	cmd.Flags().StringVar(&opts.Path, "path", "", "Webhook path (default: /convert/<target name>)")
	cmd.Flags().Int32Var(&opts.Port, "port", 443, "Webhook Service port")
	cmd.Flags().StringVar(&opts.CABundle, "ca-bundle", "", "CA bundle, base64-encoded or raw PEM (required)")
	cmd.Flags().StringVar(&opts.PlanHash, "plan-hash", "", "Value for the plan-hash annotation (default: empty, as it is before the first successful validation)")
	_ = cmd.MarkFlagRequired("config")
	_ = cmd.MarkFlagRequired("service-name")
	_ = cmd.MarkFlagRequired("service-namespace")
	_ = cmd.MarkFlagRequired("ca-bundle")
	cmd.MarkFlagsMutuallyExclusive("xrd", "crd")
	return cmd
}
