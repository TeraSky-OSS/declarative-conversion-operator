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

	internalwebhook "github.com/terasky-oss/declarative-conversion-operator/internal/webhook"
)

// ValidateResult is the outcome of the `validate` subcommand: the same
// static checks the admission webhook runs, runnable offline in CI before
// a config is ever applied to a cluster.
type ValidateResult struct {
	Config            string   `json:"config"`
	StructurallyValid bool     `json:"structurallyValid"`
	SchemaValidated   bool     `json:"schemaValidated"`
	Errors            []string `json:"errors,omitempty"`
}

// RunValidate loads a config (and, if provided, its target XRD or CRD) and
// runs exactly the checks the admission webhook performs at apply time.
// Which of xrdPath/crdPath applies is determined by the config's own kind,
// not by which flag the caller happened to pass — a config validated
// against the wrong resource type is worse than not validated at all.
func RunValidate(configPath, xrdPath, crdPath string) (*ValidateResult, error) {
	if xrdPath != "" && crdPath != "" {
		return nil, fmt.Errorf("--xrd and --crd are mutually exclusive")
	}
	kind, err := PeekConfigKind(configPath)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "CRDConversionConfig":
		if xrdPath != "" {
			return nil, fmt.Errorf("%s is a CRDConversionConfig; use --crd, not --xrd, to validate it against a schema", configPath)
		}
		return runValidateCRD(configPath, crdPath)
	default: // "XRDConversionConfig"
		if crdPath != "" {
			return nil, fmt.Errorf("%s is an XRDConversionConfig; use --xrd, not --crd, to validate it against a schema", configPath)
		}
		return runValidateXRD(configPath, xrdPath)
	}
}

func runValidateXRD(configPath, xrdPath string) (*ValidateResult, error) {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	res := &ValidateResult{Config: cfg.Name}

	if err := internalwebhook.ValidateStructure(cfg); err != nil {
		res.Errors = append(res.Errors, err.Error())
		return res, nil
	}
	res.StructurallyValid = true

	if xrdPath == "" {
		return res, nil
	}
	xrd, err := LoadXRD(xrdPath)
	if err != nil {
		return nil, err
	}
	report, _, err := runAnalyze(xrd, cfg)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		return res, nil
	}
	if report.HasErrors() {
		res.Errors = append(res.Errors, fmt.Sprintf("configuration is invalid against the XRD schema:%s", summarizeSpokeErrors(report)))
		return res, nil
	}
	res.SchemaValidated = true
	return res, nil
}

func runValidateCRD(configPath, crdPath string) (*ValidateResult, error) {
	cfg, err := LoadCRDConfig(configPath)
	if err != nil {
		return nil, err
	}
	res := &ValidateResult{Config: cfg.Name}

	if err := internalwebhook.ValidateCRDStructure(cfg); err != nil {
		res.Errors = append(res.Errors, err.Error())
		return res, nil
	}
	res.StructurallyValid = true

	if crdPath == "" {
		return res, nil
	}
	crd, err := LoadCRD(crdPath)
	if err != nil {
		return nil, err
	}
	report, _, err := runAnalyzeCRD(crd, cfg)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		return res, nil
	}
	if report.HasErrors() {
		res.Errors = append(res.Errors, fmt.Sprintf("configuration is invalid against the CRD schema:%s", summarizeSpokeErrors(report)))
		return res, nil
	}
	res.SchemaValidated = true
	return res, nil
}
