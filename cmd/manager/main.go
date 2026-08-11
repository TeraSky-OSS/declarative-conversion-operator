/*
Copyright 2026 The xrd-conversion-operator Authors.

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
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations,verbs=get;list;watch
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	teraskyv1alpha1 "github.com/vrabbi/xrd-conversion-operator/api/v1alpha1"
	"github.com/vrabbi/xrd-conversion-operator/internal/controller"
	internalwebhook "github.com/vrabbi/xrd-conversion-operator/internal/webhook"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(teraskyv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		defaultImage         string
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager. "+
		"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&defaultImage, "default-webhook-server-image", "", "Default image used for ConversionWebhookServer Deployments that don't override spec.image.")
	flag.Parse()

	logger := ctrl.Log.WithName("manager")
	ctrl.SetLogger(logger)

	namespace := currentNamespace()

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "xrd-conversion-operator.terasky.com",
		WebhookServer:          webhook.NewServer(webhook.Options{Port: 9443}),
	})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&controller.XRDConversionConfigReconciler{
		Client:                 mgr.GetClient(),
		Scheme:                 mgr.GetScheme(),
		DefaultServerNamespace: namespace,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create controller", "controller", "XRDConversionConfig")
		os.Exit(1)
	}
	if err := (&controller.ConversionWebhookServerReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		DefaultNamespace: namespace,
		DefaultImage:     defaultImage,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create controller", "controller", "ConversionWebhookServer")
		os.Exit(1)
	}

	if err := ctrl.NewWebhookManagedBy(mgr, &teraskyv1alpha1.XRDConversionConfig{}).
		WithValidator(&internalwebhook.XRDConversionConfigValidator{Client: mgr.GetClient()}).
		Complete(); err != nil {
		logger.Error(err, "unable to create webhook", "webhook", "XRDConversionConfig")
		os.Exit(1)
	}
	if err := ctrl.NewWebhookManagedBy(mgr, &teraskyv1alpha1.ConversionWebhookServer{}).
		WithValidator(&internalwebhook.ConversionWebhookServerValidator{Client: mgr.GetClient()}).
		Complete(); err != nil {
		logger.Error(err, "unable to create webhook", "webhook", "ConversionWebhookServer")
		os.Exit(1)
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

// currentNamespace returns the namespace the operator is running in,
// read from the projected service account token directory Kubernetes
// mounts into every pod. Falls back to "default" for local/dev runs
// outside a cluster, where ConversionWebhookServer child resources would
// need spec.namespace set explicitly anyway.
func currentNamespace() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := string(data); ns != "" {
			return ns
		}
	}
	return "default"
}
