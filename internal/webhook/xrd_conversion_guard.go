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

package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/conversionpatch"
)

// GuardPath is the mutating webhook's HTTP path. It is not a
// kubebuilder-conventional /mutate-<group>-<version>-<kind> path because
// the object is Crossplane's, not ours.
const GuardPath = "/guard-xrd-conversion"

// TargetXRDNameIndexName duplicates internal/controller's index key rather
// than importing it: internal/controller imports this package for nothing
// today, and a dependency in that direction purely for a string constant
// would be a worse trade than the two-line comment saying they must match.
//
// Must equal controller.TargetXRDNameIndex.
const TargetXRDNameIndexName = "spec.targetXRD.name"

// XRDConversionGuard re-injects spec.conversion — and the operator's two
// annotations — into any write to a CompositeResourceDefinition that would
// drop what the operator has applied.
//
// Why this exists at all: Crossplane's package establisher ends in a full
// client.Update from the package contents
// (internal/controller/pkg/revision/establisher.go, under an upstream
// comment reading "This should be a server side apply?"). Its merge step
// preserves nothing for XRDs. A client.Update is a replace — not
// merge-aware, and it ignores Server-Side Apply field ownership — so
// spec.conversion goes away outright. Establish runs on *every* revision
// reconcile with no diff check and no early return for a healthy revision,
// and the manager's SyncPeriod is Crossplane's --sync flag, which defaults
// to one hour. Any Lock change or Crossplane restart does the same.
//
// The operator re-applies within seconds, so it is a race it usually wins.
// But it repeats, per package-managed XRD, and what leaks through is not an
// outage: with spec.conversion gone the generated CRD falls back to
// strategy: None, and the apiserver then serves a stored object at another
// version by relabelling apiVersion and returning the original field
// layout. Clients get wrong data with HTTP 200 and no error, and writes
// during the window persist the wrong shape.
//
// Patching the generated CRD instead does not help: Crossplane's definition
// controller applies the rendered CRD through APIUpdatingApplicator, whose
// Apply is a Get followed by a full client.Update, on a controller that
// reconciles on every XRD change rather than hourly. No object in the chain
// preserves field ownership. Admission is the only place a non-SSA Update
// can be corrected without the writer's cooperation.
type XRDConversionGuard struct {
	Client  client.Client
	decoder admission.Decoder
}

// InjectDecoder is called by controller-runtime's webhook builder.
func (g *XRDConversionGuard) InjectDecoder(d admission.Decoder) { g.decoder = d }

// Handle implements admission.Handler.
//
// Every branch that is not "restore exactly what the controller would
// apply" returns Allowed with an empty patch. That is deliberate: this
// runs on every XRD write on the cluster, and a guard that can get an XRD
// write wrong is worse than the hourly race it exists to close.
func (g *XRDConversionGuard) Handle(ctx context.Context, req admission.Request) admission.Response {
	logger := log.FromContext(ctx).WithName("xrd-conversion-guard")

	xrd := &unstructured.Unstructured{}
	if err := g.decoder.Decode(req, xrd); err != nil {
		// Fail open. A decode failure on somebody else's object must never
		// block their write.
		logger.V(1).Info("could not decode the incoming XRD; allowing unchanged", "error", err)
		return admission.Allowed("")
	}

	cfg, ok := g.lookupConfig(ctx, req.Name)
	if !ok {
		// The common case by a wide margin: an XRD nobody has configured
		// conversion for. An in-memory index hit, no API call, no patch.
		return admission.Allowed("")
	}

	if reason, ok := shouldRestoreConversion(xrd, cfg); !ok {
		logger.V(1).Info("not restoring conversion", "xrd", req.Name, "config", cfg.Name, "reason", reason)
		return admission.Allowed("")
	}

	// The response patches the object as submitted, at whatever XRD API
	// version the write arrived at — so unlike the controller's own SSA
	// apply (see xrdadapter.WriteGroupVersion), there is no version to
	// choose here. A claim-offering XRD arrives at v1 and is patched at v1.
	patched := xrd.DeepCopy()
	if err := applyConversionToXRD(patched, cfg); err != nil {
		logger.Error(err, "could not build the conversion stanza; allowing unchanged", "xrd", req.Name)
		return admission.Allowed("")
	}

	marshalled, err := json.Marshal(patched.Object)
	if err != nil {
		logger.Error(err, "could not marshal the patched XRD; allowing unchanged", "xrd", req.Name)
		return admission.Allowed("")
	}
	logger.Info("restored the conversion stanza stripped from a CompositeResourceDefinition write",
		"xrd", req.Name, "config", cfg.Name, "operation", req.Operation)
	return admission.PatchResponseFromRaw(req.Object.Raw, marshalled)
}

// lookupConfig resolves the XRDConversionConfig targeting this XRD through
// the field index the admission webhook's uniqueness check already
// maintains. Scoping by the index rather than by an objectSelector on a
// label is not a stylistic choice: the label would be wiped by the very
// Update being guarded against, so a selector on it would switch the guard
// off in exactly the case it exists for.
func (g *XRDConversionGuard) lookupConfig(ctx context.Context, xrdName string) (*teraskyv1alpha1.XRDConversionConfig, bool) {
	if xrdName == "" {
		return nil, false
	}
	var list teraskyv1alpha1.XRDConversionConfigList
	if err := g.Client.List(ctx, &list, client.MatchingFields{TargetXRDNameIndexName: xrdName}); err != nil {
		log.FromContext(ctx).V(1).Info("index lookup failed; allowing unchanged", "xrd", xrdName, "error", err)
		return nil, false
	}
	if len(list.Items) != 1 {
		// Zero is the normal miss. More than one violates the
		// one-config-per-XRD invariant the validating webhook enforces, and
		// guessing which one wins is not this code's call to make.
		return nil, false
	}
	return &list.Items[0], true
}

// shouldRestoreConversion decides whether this write is one the guard
// should correct, returning a reason when it is not.
func shouldRestoreConversion(xrd *unstructured.Unstructured, cfg *teraskyv1alpha1.XRDConversionConfig) (string, bool) {
	// Only ever restore what the operator's own controller has already
	// successfully applied. A config that has not itself passed its gates
	// — validated, XRD healthy, assigned server ready — must never have
	// conversion injected on its behalf: that would route live traffic at
	// a webhook server the operator has not confirmed is serving it.
	if cfg.Status.LastAppliedPlanHash == "" {
		return "the config has never successfully applied", false
	}
	if !meta.IsStatusConditionTrue(cfg.Status.Conditions, teraskyv1alpha1.ConditionApplied) {
		return "the config's Applied condition is not True", false
	}
	if cfg.Status.AssignedWebhookServer == "" || cfg.Status.WebhookPath == "" {
		return "the config has no resolved webhook coordinates", false
	}
	if !cfg.DeletionTimestamp.IsZero() {
		// Mid-delete the controller is unpatching on purpose. Restoring
		// here would fight it.
		return "the config is being deleted", false
	}

	strategy, _, _ := unstructured.NestedString(xrd.Object, "spec", "conversion", "strategy")
	if strategy == "" {
		// The case this was built for: the establisher's Update dropped
		// spec.conversion entirely.
		return "", true
	}
	if strategy != "Webhook" {
		// An explicit strategy: None. Restore it — that is still the
		// establisher's replace, just written out in full.
		return "", true
	}

	// There IS a webhook here. Only touch it if it is ours. An XRD
	// deliberately wired to a hand-written conversion webhook must not be
	// hijacked, and the annotation is what distinguishes the two.
	if xrd.GetAnnotations()[conversionpatch.ManagedByAnnotation] != cfg.Name {
		return "spec.conversion points at a webhook this operator does not manage", false
	}

	// Ours, but possibly with the wrong coordinates or a stale annotation.
	want, err := guardWebhookService(cfg)
	if err != nil {
		return err.Error(), false
	}
	svcName, _, _ := unstructured.NestedString(xrd.Object, "spec", "conversion", "webhook", "clientConfig", "service", "name")
	svcNS, _, _ := unstructured.NestedString(xrd.Object, "spec", "conversion", "webhook", "clientConfig", "service", "namespace")
	path, _, _ := unstructured.NestedString(xrd.Object, "spec", "conversion", "webhook", "clientConfig", "service", "path")
	port, _, _ := unstructured.NestedInt64(xrd.Object, "spec", "conversion", "webhook", "clientConfig", "service", "port")
	if svcName == want.name && svcNS == want.namespace && path == cfg.Status.WebhookPath && port == int64(want.port) &&
		xrd.GetAnnotations()[conversionpatch.PlanHashAnnotation] == cfg.Status.LastAppliedPlanHash {
		return "the incoming XRD already carries the applied conversion stanza", false
	}
	return "", true
}

type guardService struct {
	name, namespace string
	port            int32
}

// guardWebhookService derives the service coordinates from the config's own
// published status rather than re-resolving the assignment. The controller
// wrote status.webhookURL when it applied, in the form
// "https://<service>.<namespace>.svc/<path>"; re-deriving the coordinates
// here would be a second implementation of the operator's naming
// convention, free to drift from the first.
func guardWebhookService(cfg *teraskyv1alpha1.XRDConversionConfig) (guardService, error) {
	u, err := url.Parse(cfg.Status.WebhookURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return guardService{}, fmt.Errorf("the config's status.webhookURL %q is not a usable https URL", cfg.Status.WebhookURL)
	}
	parts := strings.Split(u.Hostname(), ".")
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" {
		return guardService{}, fmt.Errorf("the config's status.webhookURL host %q is not <service>.<namespace>.svc", u.Hostname())
	}
	// The port is not in the URL, so it is carried separately. An empty
	// value means the config was last applied by an operator predating
	// status.webhookPort — 443 is what that version always used, and the
	// controller will fill the field in on its next reconcile.
	port := cfg.Status.WebhookPort
	if port == 0 {
		port = 443
	}
	return guardService{name: parts[0], namespace: parts[1], port: port}, nil
}

// applyConversionToXRD writes the conversion stanza and both annotations
// onto the object in place. It only ever adds: nothing else in the incoming
// XRD is read, compared, or removed, so a package's own changes to any
// other field go through exactly as written.
func applyConversionToXRD(xrd *unstructured.Unstructured, cfg *teraskyv1alpha1.XRDConversionConfig) error {
	svc, err := guardWebhookService(cfg)
	if err != nil {
		return err
	}
	reviewVersions := cfg.Spec.ConversionReviewVersions
	if len(reviewVersions) == 0 {
		reviewVersions = []string{"v1"}
	}
	versions := make([]any, 0, len(reviewVersions))
	for _, v := range reviewVersions {
		versions = append(versions, v)
	}

	// The CA bundle is deliberately NOT restored here. cert-manager's
	// ca-injector owns it on the generated objects, the controller
	// re-applies it on its next reconcile, and an admission handler
	// carrying a certificate in memory would be one more thing to rotate.
	// Restoring the service coordinates is what stops the generated CRD
	// falling back to strategy: None, which is the failure that matters.
	conversion := map[string]any{
		"strategy": "Webhook",
		"webhook": map[string]any{
			"clientConfig": map[string]any{
				"service": map[string]any{
					"name":      svc.name,
					"namespace": svc.namespace,
					"path":      cfg.Status.WebhookPath,
					"port":      int64(svc.port),
				},
			},
			"conversionReviewVersions": versions,
		},
	}
	// Preserve whatever caBundle the incoming object still carries; only
	// the one this operator would have applied is knowable here, and the
	// controller reconciles it either way.
	if existing, found, _ := unstructured.NestedString(xrd.Object, "spec", "conversion", "webhook", "clientConfig", "caBundle"); found && existing != "" {
		// conversion is built a few lines above by this function, so the
		// shape is known — but this is an admission handler, and a panic
		// here fails open on every XRD write in the cluster. Navigate it
		// defensively and skip the preservation rather than crash.
		if webhookCfg, ok := conversion["webhook"].(map[string]any); ok {
			if clientConfig, ok := webhookCfg["clientConfig"].(map[string]any); ok {
				clientConfig["caBundle"] = existing
			}
		}
	}
	if err := unstructured.SetNestedMap(xrd.Object, conversion, "spec", "conversion"); err != nil {
		return fmt.Errorf("setting spec.conversion: %w", err)
	}

	annotations := xrd.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[conversionpatch.ManagedByAnnotation] = cfg.Name
	annotations[conversionpatch.PlanHashAnnotation] = cfg.Status.LastAppliedPlanHash
	xrd.SetAnnotations(annotations)
	return nil
}
