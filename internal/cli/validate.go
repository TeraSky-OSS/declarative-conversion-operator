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
	"errors"
	"fmt"

	internalwebhook "github.com/terasky-oss/declarative-conversion-operator/internal/webhook"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/engine"
)

// ValidateResult is the outcome of the `validate` subcommand: the same
// static checks the admission webhook runs, runnable offline in CI before
// a config is ever applied to a cluster.
type ValidateResult struct {
	Config            string   `json:"config"`
	StructurallyValid bool     `json:"structurallyValid"`
	SchemaValidated   bool     `json:"schemaValidated"`
	Errors            []string `json:"errors,omitempty"`
	// Analysis is the underlying report, kept so the CI output formats can
	// attribute each diagnostic to the rule and line that produced it. Not
	// serialized: the JSON shape of this result is a stable contract, and
	// `analyze -o json` is where the full report already lives.
	Analysis *engine.AnalyzeReport `json:"-"`
}

// RunValidate loads a config (and, if provided, its target XRD or CRD) and
// runs exactly the checks the admission webhook performs at apply time.
// Which of xrdPath/crdPath applies is determined by the config's own kind,
// not by which flag the caller happened to pass — a config validated
// against the wrong resource type is worse than not validated at all.
func RunValidate(configPath, xrdPath, crdPath string) (*ValidateResult, error) {
	return RunValidateFrom(configPath, xrdPath, crdPath, "", "")
}

// RunValidateFrom is RunValidate with a package as an alternative schema
// source. See XRDFromSource.
func RunValidateFrom(configPath, xrdPath, crdPath, packageRef, target string) (*ValidateResult, error) {
	if xrdPath != "" && crdPath != "" {
		return nil, errors.New("--xrd and --crd are mutually exclusive")
	}
	if packageRef != "" && crdPath != "" {
		return nil, errors.New("--package and --crd are mutually exclusive: a package ships XRDs")
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
		return runValidateXRD(configPath, xrdPath, packageRef, target)
	}
}

func runValidateXRD(configPath, xrdPath, packageRef, target string) (*ValidateResult, error) {
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

	if xrdPath == "" && packageRef == "" {
		return res, nil
	}
	xrd, err := XRDFromSource(xrdPath, packageRef, target)
	if err != nil {
		return nil, err
	}
	report, _, err := runAnalyze(xrd, cfg)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		return res, nil
	}
	res.Analysis = &report
	if report.HasErrors() {
		res.Errors = append(res.Errors, "configuration is invalid against the XRD schema:"+summarizeSpokeErrors(report))
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
	res.Analysis = &report
	if report.HasErrors() {
		res.Errors = append(res.Errors, "configuration is invalid against the CRD schema:"+summarizeSpokeErrors(report))
		return res, nil
	}
	res.SchemaValidated = true
	return res, nil
}
