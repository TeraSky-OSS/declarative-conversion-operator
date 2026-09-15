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
	"context"
	"fmt"
	"io"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// PropagationCRDStatus is one generated CRD's observed conversion wiring.
type PropagationCRDStatus struct {
	CRD  string `json:"crd"`
	Role string `json:"role"`
	// Strategy is the CRD's spec.conversion.strategy as the apiserver
	// currently has it. "None" is the dangerous value: the apiserver then
	// serves a stored object at another version by relabelling apiVersion
	// and returning the original field layout — wrong data, HTTP 200.
	Strategy string `json:"strategy"`
	// Service is the webhook the CRD points at, "namespace/name", empty
	// when there is no webhook.
	Service string `json:"service,omitempty"`
	Path    string `json:"path,omitempty"`
	// MatchesXRD reports whether the CRD's webhook coordinates equal the
	// XRD's, which is what "Crossplane has re-rendered this" means.
	MatchesXRD bool   `json:"matchesXRD"`
	Message    string `json:"message,omitempty"`
}

// PropagationReport is what --verify-propagation adds to a live run.
type PropagationReport struct {
	XRD string `json:"xrd"`
	// XRDStrategy is the XRD's own spec.conversion.strategy.
	XRDStrategy string                 `json:"xrdStrategy"`
	CRDs        []PropagationCRDStatus `json:"crds"`
	// Propagated is true only when every generated CRD matches.
	Propagated bool `json:"propagated"`
}

// VerifyPropagation reads the XRD's spec.conversion and compares it against
// every CRD Crossplane generates from it.
//
// It answers the question a `test --live` run cannot: the samples may all
// convert perfectly through the engine and still not convert *in the
// cluster*, because nothing converts anything until Crossplane re-renders
// the generated CRD with the webhook block. Between the operator patching
// the XRD and Crossplane acting on it — or indefinitely, if Crossplane is
// wedged, paused, or has lost RBAC — reads come back relabelled and
// unconverted with no error at all.
func VerifyPropagation(ctx context.Context, dyn dynamic.Interface, xrdName string) (*PropagationReport, error) {
	xrd, err := FetchLiveXRD(ctx, dyn, xrdName)
	if err != nil {
		return nil, err
	}
	generated, err := xrdadapter.GeneratedCRDNames(xrd)
	if err != nil {
		return nil, err
	}

	rep := &PropagationReport{XRD: xrdName, Propagated: true}
	rep.XRDStrategy, _, _ = unstructured.NestedString(xrd.Object, "spec", "conversion", "strategy")
	if rep.XRDStrategy == "" {
		rep.XRDStrategy = "None"
	}
	wantSvc, wantPath := webhookCoords(xrd.Object)

	for _, g := range generated {
		st := PropagationCRDStatus{CRD: g.Name, Role: string(g.Role)}
		obj, err := dyn.Resource(crdGVR).Get(ctx, g.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				st.Message = "Crossplane has not created this CRD yet"
			} else {
				st.Message = fmt.Sprintf("reading the CRD failed: %v", err)
			}
			rep.CRDs = append(rep.CRDs, st)
			rep.Propagated = false
			continue
		}

		st.Strategy, _, _ = unstructured.NestedString(obj.Object, "spec", "conversion", "strategy")
		if st.Strategy == "" {
			st.Strategy = "None"
		}
		st.Service, st.Path = webhookCoords(obj.Object)

		switch {
		case rep.XRDStrategy != "Webhook":
			st.Message = "the XRD itself has no conversion webhook, so there is nothing to propagate — is an XRDConversionConfig applied and Applied?"
		case st.Strategy != "Webhook":
			st.Message = fmt.Sprintf("spec.conversion.strategy is %q: the apiserver relabels stored objects without converting them, returning wrong data with HTTP 200", st.Strategy)
		case st.Service != wantSvc || st.Path != wantPath:
			st.Message = fmt.Sprintf("points at %s%s, but the XRD says %s%s", st.Service, st.Path, wantSvc, wantPath)
		default:
			st.MatchesXRD = true
		}
		if !st.MatchesXRD {
			rep.Propagated = false
		}
		rep.CRDs = append(rep.CRDs, st)
	}
	return rep, nil
}

func webhookCoords(obj map[string]any) (service, path string) {
	name, _, _ := unstructured.NestedString(obj, "spec", "conversion", "webhook", "clientConfig", "service", "name")
	ns, _, _ := unstructured.NestedString(obj, "spec", "conversion", "webhook", "clientConfig", "service", "namespace")
	path, _, _ = unstructured.NestedString(obj, "spec", "conversion", "webhook", "clientConfig", "service", "path")
	if name == "" && ns == "" {
		return "", path
	}
	return ns + "/" + name, path
}

// WriteTable renders the propagation check for a terminal.
func (r *PropagationReport) WriteTable(w io.Writer) {
	verdict := "PROPAGATED"
	if !r.Propagated {
		verdict = "NOT PROPAGATED"
	}
	_, _ = fmt.Fprintf(w, "\nConversion propagation: %s\n", verdict)
	_, _ = fmt.Fprintf(w, "XRD %s spec.conversion.strategy: %s\n", r.XRD, r.XRDStrategy)
	for _, c := range r.CRDs {
		status := "ok"
		if !c.MatchesXRD {
			status = "MISMATCH"
		}
		_, _ = fmt.Fprintf(w, "  %s (%s): strategy=%s %s\n", c.CRD, c.Role, c.Strategy, status)
		if c.Message != "" {
			_, _ = fmt.Fprintf(w, "      %s\n", c.Message)
		}
	}
	if !r.Propagated {
		_, _ = fmt.Fprintln(w, strings.TrimSpace(`
  Until every generated CRD carries the webhook, reads at a non-storage
  version return stored objects relabelled but UNCONVERTED, with HTTP 200
  and no error. Check the XRDConversionConfig's ConversionPropagated
  condition, and whether Crossplane is running and not paused on this XRD.`))
	}
}
