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

// Command webhook-server is the standalone, horizontally-scalable
// conversion webhook runtime described by a ConversionWebhookServer
// instance. It is intentionally a separate binary/image from cmd/manager:
// it carries no operator-controller code, only what's needed to serve
// CRD conversion requests as fast as possible.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/webhookserver"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(teraskyv1alpha1.AddToScheme(scheme))
	utilruntime.Must(extv1.AddToScheme(scheme))
}

func main() {
	var (
		serverName       string
		tlsCertDir       string
		conversionAddr   string
		plainAddr        string
		certReloadEvery  time.Duration
		enableXRDSupport bool
		enableCRDSupport bool
		otelEndpoint     string
		otelSampleRatio  float64
		otelInsecure     bool
		cacheSelector    string
	)
	flag.StringVar(&serverName, "webhook-server-name", "", "Name of the ConversionWebhookServer instance this replica belongs to (required).")
	flag.StringVar(&tlsCertDir, "tls-cert-dir", "/tls", "Directory containing tls.crt and tls.key for the conversion endpoint.")
	flag.StringVar(&conversionAddr, "conversion-bind-address", ":9443", "Address the TLS conversion endpoint listens on.")
	flag.StringVar(&plainAddr, "metrics-bind-address", ":8443", "Address the plain-HTTP health/metrics/debug endpoints listen on.")
	flag.DurationVar(&certReloadEvery, "cert-reload-interval", 30*time.Second, "How often to re-read the TLS certificate from disk.")
	flag.BoolVar(&enableXRDSupport, "enable-xrd-support", true, "Serve conversions for XRDConversionConfig-backed XRDs. Must match the operator's own --enable-xrd-support.")
	flag.BoolVar(&enableCRDSupport, "enable-crd-support", true, "Serve conversions for CRDConversionConfig-backed native CRDs. Must match the operator's own --enable-crd-support.")
	flag.StringVar(&otelEndpoint, "otel-exporter-otlp-endpoint", "", "Optional OTLP/gRPC endpoint for conversion-path tracing (empty = tracing disabled).")
	flag.Float64Var(&otelSampleRatio, "otel-trace-sample-ratio", 0.1, "Trace sampling ratio when --otel-exporter-otlp-endpoint is set (0.0–1.0).")
	flag.BoolVar(&otelInsecure, "otel-exporter-otlp-insecure", false, "Disable TLS when exporting traces (trusted in-cluster collectors only).")
	flag.StringVar(&cacheSelector, "cache-label-selector", "", "JSON metav1.LabelSelector scoping XRDConversionConfig and CRDConversionConfig informers. Empty watches every config.")
	opts := ctrl.Options{Scheme: scheme}
	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	logger := ctrl.Log.WithName("webhook-server")

	if serverName == "" {
		fmt.Fprintln(os.Stderr, "--webhook-server-name is required")
		os.Exit(1)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := webhookserver.InitTracing(rootCtx, otelEndpoint, otelSampleRatio, otelInsecure)
	if err != nil {
		logger.Error(err, "unable to initialize OpenTelemetry tracing")
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			logger.Error(err, "unable to shut down OpenTelemetry tracing")
		}
	}()

	// This manager exists purely to run the registry reconciler's
	// informers/cache — it serves no admission webhooks and no default
	// metrics/health endpoints of its own; the hand-rolled Server below
	// owns all HTTP surfaces so the admission-critical conversion path
	// never shares a listener with anything else.
	opts.Metrics = metricsserver.Options{BindAddress: "0"}
	opts.HealthProbeBindAddress = "0"
	opts.LeaderElection = false // every replica is symmetric; no coordination needed.
	cacheOpts, err := webhookserver.CacheOptionsFromSelectorJSON(cacheSelector)
	if err != nil {
		logger.Error(err, "invalid --cache-label-selector")
		os.Exit(1)
	}
	opts.Cache = cacheOpts
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), opts)
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	registry := webhookserver.NewRegistry()
	metricsReg := prometheus.NewRegistry()
	metrics := webhookserver.NewMetrics(metricsReg)

	reconciler := &webhookserver.Reconciler{
		Client: mgr.GetClient(), ServerName: serverName, Registry: registry, Metrics: metrics,
		EnableXRDSupport: enableXRDSupport, EnableCRDSupport: enableCRDSupport,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to set up registry reconciler")
		os.Exit(1)
	}

	certReloader, err := webhookserver.NewCertReloader(tlsCertDir, certReloadEvery)
	if err != nil {
		logger.Error(err, "unable to load initial TLS certificate")
		os.Exit(1)
	}

	server := &webhookserver.Server{Registry: registry, Metrics: metrics}

	ctx := rootCtx

	mgrErrCh := make(chan error, 1)
	go func() { mgrErrCh <- mgr.Start(ctx) }()
	go certReloader.Run(ctx)

	if !mgr.GetCache().WaitForCacheSync(ctx) {
		logger.Error(fmt.Errorf("cache sync failed"), "unable to sync cache before initial registry population")
		os.Exit(1)
	}
	if err := reconciler.InitialSync(ctx); err != nil {
		logger.Error(err, "initial registry sync encountered errors; continuing, affected XRDs will retry via watch events")
	}
	server.SetReady(true)
	logger.Info("registry synced, marking replica ready", "serverName", serverName)

	conversionSrv := &http.Server{
		Addr:      conversionAddr,
		Handler:   server.ConversionMux(),
		TLSConfig: &tls.Config{GetCertificate: certReloader.GetCertificate, MinVersion: tls.VersionTLS12},
	}
	plainSrv := &http.Server{Addr: plainAddr, Handler: server.PlainMux()}

	go func() {
		logger.Info("serving conversion requests", "address", conversionAddr)
		if err := conversionSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			logger.Error(err, "conversion server exited unexpectedly")
			os.Exit(1)
		}
	}()
	go func() {
		logger.Info("serving health/metrics/debug", "address", plainAddr)
		if err := plainSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(err, "plain HTTP server exited unexpectedly")
			os.Exit(1)
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-mgrErrCh:
		if err != nil {
			logger.Error(err, "manager exited unexpectedly")
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = conversionSrv.Shutdown(shutdownCtx)
	_ = plainSrv.Shutdown(shutdownCtx)
}
