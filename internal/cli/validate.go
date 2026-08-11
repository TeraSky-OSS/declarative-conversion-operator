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

// RunValidate loads a config (and, if provided, an XRD) and runs exactly
// the checks the admission webhook performs at apply time.
func RunValidate(configPath, xrdPath string) (*ValidateResult, error) {
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
