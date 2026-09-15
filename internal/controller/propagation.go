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

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

// expectedConversion is what the operator applied to the XRD, in the terms
// the generated CRD expresses it.
type expectedConversion struct {
	ServiceName      string
	ServiceNamespace string
	Path             string
	Port             int32
	// CABundle is base64-encoded, matching how the operator reads it out
	// of the cert-manager Secret.
	CABundle       string
	ReviewVersions []string
}

// caBundleHash is what the status records instead of the bundle itself: a
// status field is the wrong place to duplicate a certificate, and a hash is
// all a comparison needs.
func caBundleHash(b64 string) string {
	if b64 == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		// Hash whatever we were handed rather than returning nothing: a
		// mismatch is still detectable, which is the point.
		raw = []byte(b64)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// verifyPropagation reads every CRD Crossplane generates from the target
// XRD and compares its spec.conversion.webhook against what the operator
// applied.
//
// This exists because patching the XRD is not the same as conversion
// working. Nothing converts anything until Crossplane re-renders
// {plural}.{group} with that webhook block, and between those two moments —
// or indefinitely, if Crossplane is wedged, paused, an old version, or has
// lost RBAC — the config reported healthy while every conversion request
// either failed or, worse, silently returned unconverted objects.
//
// It never repairs anything. Reporting is the whole job: a broken
// Crossplane is not this operator's to fix, but it is very much this
// operator's to stop claiming is fine.
func (r *XRDConversionConfigReconciler) verifyPropagation(ctx context.Context, cfg *teraskyv1alpha1.XRDConversionConfig, xrd *unstructured.Unstructured, want expectedConversion) {
	generated, err := xrdadapter.GeneratedCRDNames(xrd)
	if err != nil {
		setPropagationCondition(cfg, metav1.ConditionFalse, teraskyv1alpha1.ReasonNotPropagated,
			fmt.Sprintf("cannot determine which CRDs the XRD generates: %v", err))
		GetManagerMetrics().ConversionPropagated.WithLabelValues(cfg.Spec.TargetXRD.Name).Set(0)
		return
	}

	now := metav1.NewTime(time.Now())
	statuses := make([]teraskyv1alpha1.GeneratedCRDStatus, 0, len(generated))
	var (
		notFound []string
		stale    []string
		mismatch []string
	)

	for _, g := range generated {
		st := teraskyv1alpha1.GeneratedCRDStatus{Name: g.Name, Role: string(g.Role), ObservedAt: &now}

		var crd extv1.CustomResourceDefinition
		if err := r.Get(ctx, types.NamespacedName{Name: g.Name}, &crd); err != nil {
			if apierrors.IsNotFound(err) {
				st.Message = "Crossplane has not created this CRD yet"
				notFound = append(notFound, g.Name)
			} else {
				st.Message = fmt.Sprintf("reading the generated CRD failed: %v", err)
				mismatch = append(mismatch, g.Name)
			}
			statuses = append(statuses, st)
			continue
		}

		// ClientConfig is nil-checked separately: the apiserver requires it
		// when the strategy is Webhook, but this runs in a reconcile loop
		// and a panic here would take the controller down over a shape it
		// merely did not expect.
		observed := ""
		if c := crd.Spec.Conversion; c != nil && c.Webhook != nil && c.Webhook.ClientConfig != nil {
			observed = base64.StdEncoding.EncodeToString(c.Webhook.ClientConfig.CABundle)
		}
		st.ObservedCABundleHash = caBundleHash(observed)

		reason, detail := compareGeneratedConversion(&crd, want)
		switch reason {
		case "":
			st.Propagated = true
		case teraskyv1alpha1.ReasonCABundleStale:
			st.Message = detail
			stale = append(stale, g.Name)
		default:
			st.Message = detail
			mismatch = append(mismatch, g.Name)
		}
		statuses = append(statuses, st)
	}

	cfg.Status.GeneratedCRDs = statuses

	// Publish the current verdict for this target before branching, so the
	// gauge always reflects the state the condition below is about.
	defer func() {
		propagated := 0.0
		if meta.IsStatusConditionTrue(cfg.Status.Conditions, teraskyv1alpha1.ConditionConversionPropagated) {
			propagated = 1
		}
		GetManagerMetrics().ConversionPropagated.WithLabelValues(cfg.Spec.TargetXRD.Name).Set(propagated)
	}()

	switch {
	case len(notFound) > 0:
		setPropagationCondition(cfg, metav1.ConditionFalse, teraskyv1alpha1.ReasonGeneratedCRDNotFound,
			fmt.Sprintf("Crossplane has not created %s yet; conversion cannot work until it does", strings.Join(notFound, ", ")))
	case len(mismatch) > 0:
		setPropagationCondition(cfg, metav1.ConditionFalse, teraskyv1alpha1.ReasonNotPropagated,
			fmt.Sprintf("%s does not carry the conversion webhook applied to the XRD: %s", strings.Join(mismatch, ", "), firstMessage(statuses, mismatch)))
	case len(stale) > 0:
		// Called out separately from a generic mismatch because the
		// remedy is different: everything else is "Crossplane has not
		// caught up", this one is "a certificate rotated and the CRD is
		// still serving the old bundle", which breaks conversion with a
		// TLS error rather than a missing webhook.
		setPropagationCondition(cfg, metav1.ConditionFalse, teraskyv1alpha1.ReasonCABundleStale,
			fmt.Sprintf("%s carries a stale caBundle; conversion requests will fail TLS verification until Crossplane re-renders it", strings.Join(stale, ", ")))
	default:
		r.observePropagationLag(cfg)
		setPropagationCondition(cfg, metav1.ConditionTrue, teraskyv1alpha1.ReasonPropagated,
			fmt.Sprintf("every CRD generated from the XRD (%s) carries the applied conversion webhook", strings.Join(crdNames(statuses), ", ")))
	}
}

// compareGeneratedConversion returns "" when the CRD matches, or a
// condition reason plus a human-readable detail when it does not.
func compareGeneratedConversion(crd *extv1.CustomResourceDefinition, want expectedConversion) (reason, detail string) {
	if crd.Spec.Conversion == nil || crd.Spec.Conversion.Strategy != extv1.WebhookConverter {
		strategy := "None"
		if crd.Spec.Conversion != nil {
			strategy = string(crd.Spec.Conversion.Strategy)
		}
		// This is the dangerous state, not a harmless one: with strategy
		// None the apiserver serves a stored object at another version by
		// relabelling apiVersion and returning the original field layout,
		// so clients get wrong data with a 200.
		return teraskyv1alpha1.ReasonNotPropagated,
			fmt.Sprintf("spec.conversion.strategy is %q, so the apiserver relabels stored objects without converting them", strategy)
	}
	wh := crd.Spec.Conversion.Webhook
	if wh == nil || wh.ClientConfig == nil || wh.ClientConfig.Service == nil {
		return teraskyv1alpha1.ReasonNotPropagated, "spec.conversion.webhook has no service client config"
	}
	svc := wh.ClientConfig.Service

	port := int32(443)
	if svc.Port != nil {
		port = *svc.Port
	}
	path := ""
	if svc.Path != nil {
		path = *svc.Path
	}
	switch {
	case svc.Name != want.ServiceName || svc.Namespace != want.ServiceNamespace:
		return teraskyv1alpha1.ReasonNotPropagated,
			fmt.Sprintf("points at service %s/%s, expected %s/%s", svc.Namespace, svc.Name, want.ServiceNamespace, want.ServiceName)
	case path != want.Path:
		return teraskyv1alpha1.ReasonNotPropagated,
			fmt.Sprintf("webhook path is %q, expected %q", path, want.Path)
	case port != want.Port:
		return teraskyv1alpha1.ReasonNotPropagated,
			fmt.Sprintf("webhook port is %d, expected %d", port, want.Port)
	case !sameStrings(wh.ConversionReviewVersions, want.ReviewVersions):
		return teraskyv1alpha1.ReasonNotPropagated,
			fmt.Sprintf("conversionReviewVersions are %v, expected %v", wh.ConversionReviewVersions, want.ReviewVersions)
	}

	observed := base64.StdEncoding.EncodeToString(wh.ClientConfig.CABundle)
	if caBundleHash(observed) != caBundleHash(want.CABundle) {
		return teraskyv1alpha1.ReasonCABundleStale,
			fmt.Sprintf("caBundle is %s, expected %s", caBundleHash(observed), caBundleHash(want.CABundle))
	}
	return "", ""
}

// observePropagationLag records apply -> observed-propagated, but only on
// the transition into Propagated: observing it on every steady-state
// reconcile would bury the real distribution under a pile of samples that
// measure nothing but the resync interval.
func (r *XRDConversionConfigReconciler) observePropagationLag(cfg *teraskyv1alpha1.XRDConversionConfig) {
	if meta.IsStatusConditionTrue(cfg.Status.Conditions, teraskyv1alpha1.ConditionConversionPropagated) {
		return
	}
	applied := meta.FindStatusCondition(cfg.Status.Conditions, teraskyv1alpha1.ConditionApplied)
	if applied == nil || applied.Status != metav1.ConditionTrue || applied.LastTransitionTime.IsZero() {
		return
	}
	lag := time.Since(applied.LastTransitionTime.Time).Seconds()
	if lag < 0 {
		return
	}
	GetManagerMetrics().PropagationLag.WithLabelValues(cfg.Spec.TargetXRD.Name).Observe(lag)
}

func setPropagationCondition(cfg *teraskyv1alpha1.XRDConversionConfig, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: teraskyv1alpha1.ConditionConversionPropagated, Status: status, Reason: reason, Message: message,
	})
}

func crdNames(statuses []teraskyv1alpha1.GeneratedCRDStatus) []string {
	out := make([]string, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, s.Name)
	}
	return out
}

func firstMessage(statuses []teraskyv1alpha1.GeneratedCRDStatus, names []string) string {
	for _, s := range statuses {
		for _, n := range names {
			if s.Name == n && s.Message != "" {
				return s.Message
			}
		}
	}
	return "see status.generatedCRDs"
}

// sameStrings compares two lists as sets, since conversionReviewVersions
// order carries no meaning and an ordering difference is not a mismatch
// worth reporting to a user.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
