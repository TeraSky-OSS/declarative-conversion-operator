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

// Command manager is the operator's controller-manager binary: it runs the
// XRDConversionConfig and ConversionWebhookServer reconcilers, plus this
// operator's own admission webhooks for those two CRDs. It never serves
// CRD conversion requests itself — that's cmd/webhook-server's job.
//
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations;mutatingwebhookconfigurations,verbs=get;list;watch
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/controller"
	internalwebhook "github.com/terasky-oss/declarative-conversion-operator/internal/webhook"
	"github.com/terasky-oss/declarative-conversion-operator/pkg/xrdadapter"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(teraskyv1alpha1.AddToScheme(scheme))
	utilruntime.Must(extv1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		defaultImage         string
		enableXRDSupport     bool
		enableCRDSupport     bool
		enableXRDGuard       bool
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager. "+
		"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&defaultImage, "default-webhook-server-image", "", "Default image used for ConversionWebhookServer Deployments that don't override spec.image.")
	flag.BoolVar(&enableXRDSupport, "enable-xrd-support", true, "Enable XRDConversionConfig support for Crossplane CompositeResourceDefinitions. "+
		"Requires Crossplane to be installed; disable on clusters that don't have it, since watching a GVK whose CRD doesn't exist is fatal at startup.")
	flag.BoolVar(&enableCRDSupport, "enable-crd-support", true, "Enable CRDConversionConfig support for plain native Kubernetes CustomResourceDefinitions.")
	flag.BoolVar(&enableXRDGuard, "enable-xrd-conversion-guard", true, "Register a mutating admission webhook on compositeresourcedefinitions that re-injects spec.conversion into writes that would drop it. "+
		"Exists because Crossplane's package establisher writes established objects with a full client.Update rather than a Server-Side Apply, stripping the field on every revision reconcile. "+
		"Both upstream write paths carry a TODO to move to SSA; turn this off once a Crossplane version lands that no longer needs it. Requires --enable-xrd-support.")
	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	logger := ctrl.Log.WithName("manager")

	namespace := currentNamespace()

	restConfig := ctrl.GetConfigOrDie()

	// Fail fast, and legibly, on a control plane that does not serve the
	// v2 XRD API. Without this the first symptom is controller-runtime
	// refusing to establish the CompositeResourceDefinition watch with a
	// bare "no matches for kind", which reads like a scheme bug rather
	// than "Crossplane 2.x is a prerequisite".
	if enableXRDSupport {
		if err := checkCrossplaneV2Served(restConfig); err != nil {
			logger.Error(err, "XRD support is enabled but this cluster does not serve the Crossplane 2.x XRD API. "+
				"Install Crossplane 2.x (https://docs.crossplane.io/latest/software/install/), or disable XRD support "+
				"with --enable-xrd-support=false (Helm: features.crossplane.enabled=false). Crossplane 1.x control "+
				"planes are out of scope; note that scope: LegacyCluster XRDs are fully supported on Crossplane 2.x.",
				"requiredAPI", xrdadapter.GroupVersionKind.GroupVersion().String())
			os.Exit(1)
		}
	}

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "declarative-conversion-operator.terasky.com",
		WebhookServer:          webhook.NewServer(webhook.Options{Port: 9443}),
	})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// The XRDConversionConfig controller watches Crossplane's
	// CompositeResourceDefinition GVK, which doesn't exist at all on a
	// cluster without Crossplane installed — establishing that watch is
	// fatal at manager startup in that case, so the controller (and its
	// watch) is only ever set up when XRD support is enabled. The
	// CRDConversionConfig controller watches a core Kubernetes type that's
	// always present, so it carries no equivalent startup risk, but the
	// toggle still exists for operators who simply don't want the feature
	// active.
	if enableXRDSupport {
		if err := (&controller.XRDConversionConfigReconciler{
			Client:                 mgr.GetClient(),
			Scheme:                 mgr.GetScheme(),
			DefaultServerNamespace: namespace,
		}).SetupWithManager(mgr); err != nil {
			logger.Error(err, "unable to create controller", "controller", "XRDConversionConfig")
			os.Exit(1)
		}
	} else {
		logger.Info("XRD support disabled (--enable-xrd-support=false); not watching Crossplane CompositeResourceDefinitions")
	}
	if enableCRDSupport {
		if err := (&controller.CRDConversionConfigReconciler{
			Client:                 mgr.GetClient(),
			Scheme:                 mgr.GetScheme(),
			DefaultServerNamespace: namespace,
		}).SetupWithManager(mgr); err != nil {
			logger.Error(err, "unable to create controller", "controller", "CRDConversionConfig")
			os.Exit(1)
		}
	} else {
		logger.Info("native CRD support disabled (--enable-crd-support=false)")
	}
	if err := (&controller.ConversionWebhookServerReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		DefaultNamespace: namespace,
		DefaultImage:     defaultImage,
		EnableXRDSupport: enableXRDSupport,
		EnableCRDSupport: enableCRDSupport,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create controller", "controller", "ConversionWebhookServer")
		os.Exit(1)
	}

	// Both admission webhooks are always registered, regardless of the
	// toggles above — this gives a create/update of a disabled config kind
	// an immediate, clear rejection reason (see each Validator's Enabled
	// field) instead of a silently-never-reconciled object.
	if err := ctrl.NewWebhookManagedBy(mgr, &teraskyv1alpha1.XRDConversionConfig{}).
		WithValidator(&internalwebhook.XRDConversionConfigValidator{Client: mgr.GetClient(), Enabled: enableXRDSupport}).
		Complete(); err != nil {
		logger.Error(err, "unable to create webhook", "webhook", "XRDConversionConfig")
		os.Exit(1)
	}
	if err := ctrl.NewWebhookManagedBy(mgr, &teraskyv1alpha1.CRDConversionConfig{}).
		WithValidator(&internalwebhook.CRDConversionConfigValidator{Client: mgr.GetClient(), Enabled: enableCRDSupport}).
		Complete(); err != nil {
		logger.Error(err, "unable to create webhook", "webhook", "CRDConversionConfig")
		os.Exit(1)
	}
	if err := ctrl.NewWebhookManagedBy(mgr, &teraskyv1alpha1.ConversionWebhookServer{}).
		WithValidator(&internalwebhook.ConversionWebhookServerValidator{Client: mgr.GetClient()}).
		Complete(); err != nil {
		logger.Error(err, "unable to create webhook", "webhook", "ConversionWebhookServer")
		os.Exit(1)
	}

	// The conversion guard mutates Crossplane's own XRDs, so unlike the
	// three validators above it is only registered when XRD support is on
	// — there is nothing to guard otherwise, and on a cluster with no
	// Crossplane the type does not exist at all.
	switch {
	case !enableXRDGuard:
		logger.Info("XRD conversion guard disabled (--enable-xrd-conversion-guard=false); a package-managed XRD will lose its conversion stanza on every ConfigurationRevision reconcile until the operator re-applies")
	case !enableXRDSupport:
		logger.Info("XRD conversion guard not registered: it requires --enable-xrd-support")
	default:
		guard := &internalwebhook.XRDConversionGuard{Client: mgr.GetClient()}
		guard.InjectDecoder(admission.NewDecoder(mgr.GetScheme()))
		mgr.GetWebhookServer().Register(internalwebhook.GuardPath, &webhook.Admission{Handler: guard})
		logger.Info("XRD conversion guard registered", "path", internalwebhook.GuardPath)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	logger.Info("starting manager", "namespace", namespace)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// errCrossplaneV2NotServed is returned by checkCrossplaneV2Served when the
// cluster's discovery document has no apiextensions.crossplane.io/v2 group
// version, which is what a Crossplane 1.x control plane (or a cluster with
// no Crossplane at all) looks like.
var errCrossplaneV2NotServed = errors.New("the apiextensions.crossplane.io/v2 API is not served by this cluster")

// newDiscoveryClient is a var so the startup check can be unit-tested
// against a fake discovery document without a live apiserver.
var newDiscoveryClient = func(cfg *rest.Config) (discovery.DiscoveryInterface, error) {
	return discovery.NewDiscoveryClientForConfig(cfg)
}

// checkCrossplaneV2Served reports whether this cluster serves the XRD API
// version the operator reads. A discovery error that is not "group version
// absent" is returned as-is: an unreachable or briefly-unavailable
// apiserver should not be reported to the user as a missing Crossplane.
func checkCrossplaneV2Served(cfg *rest.Config) error {
	dc, err := newDiscoveryClient(cfg)
	if err != nil {
		return fmt.Errorf("building discovery client: %w", err)
	}
	gv := xrdadapter.GroupVersionKind.GroupVersion().String()
	resources, err := dc.ServerResourcesForGroupVersion(gv)
	if err != nil {
		if apierrors.IsNotFound(err) || discovery.IsGroupDiscoveryFailedError(err) {
			return errCrossplaneV2NotServed
		}
		return fmt.Errorf("discovering %s: %w", gv, err)
	}
	for _, r := range resources.APIResources {
		if r.Kind == xrdadapter.GroupVersionKind.Kind {
			return nil
		}
	}
	return errCrossplaneV2NotServed
}

// serviceAccountNamespaceFile is a var (not a const) so tests can point it
// at a path that's guaranteed not to exist, rather than depending on
// whether the test happens to be running inside a real Kubernetes pod.
var serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// currentNamespace returns the namespace the operator is running in,
// read from the projected service account token directory Kubernetes
// mounts into every pod. Falls back to "default" for local/dev runs
// outside a cluster, where ConversionWebhookServer child resources would
// need spec.namespace set explicitly anyway.
func currentNamespace() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	if data, err := os.ReadFile(serviceAccountNamespaceFile); err == nil {
		if ns := string(data); ns != "" {
			return ns
		}
	}
	return "default"
}
