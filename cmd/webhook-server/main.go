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
	"errors"
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
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
	"github.com/terasky-oss/declarative-conversion-operator/internal/webhookserver"
)

var scheme = runtime.NewScheme()

// effectiveInitialSyncWorkers reports the pool size InitialSync actually
// used, so the cold-start log line states the parallelism that produced
// the elapsed time next to it rather than the flag value, which is 0 by
// default and says nothing.
func effectiveInitialSyncWorkers(flagValue, targets int) int {
	workers := flagValue
	if workers <= 0 {
		workers = webhookserver.DefaultInitialSyncWorkers()
	}
	if targets > 0 && workers > targets {
		workers = targets
	}
	return workers
}

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
		maxRequestBytes  int64
		requestTimeout   time.Duration
		shutdownTimeout  time.Duration
		initialSyncPar   int
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
	flag.StringVar(&cacheSelector, "cache-label-selector", "", "JSON metav1.LabelSelector scoping this replica's informers. It covers the XRDConversionConfig and CRDConversionConfig objects AND the CustomResourceDefinition/CompositeResourceDefinition objects holding their schemas, so targets must carry the label too. Empty watches everything.")
	flag.Int64Var(&maxRequestBytes, "max-request-bytes", webhookserver.DefaultMaxRequestBytes, "Maximum ConversionReview request body size. A larger body is answered with a ConversionReview failure rather than being read. Raise it if legitimate batches are being rejected.")
	flag.DurationVar(&requestTimeout, "request-timeout", webhookserver.DefaultRequestTimeout, "Maximum time one ConversionReview may occupy a worker. Must stay below the apiserver's own fixed 30s conversion timeout plus this server's write timeout.")
	flag.IntVar(&initialSyncPar, "initial-sync-workers", 0, "How many conversion plans this replica compiles concurrently during its cold start. 0 uses GOMAXPROCS. Compilation is CPU-bound and independent per target; the bound exists because each worker holds a decoded schema and a half-built plan. Lower it if the cold start is competing with something else for CPU; raising it above GOMAXPROCS buys nothing.")
	flag.DurationVar(&shutdownTimeout, "shutdown-timeout", webhookserver.DefaultShutdownTimeout, "How long to let in-flight ConversionReviews finish after a termination signal. This value plus the pod's preStop sleep must stay below terminationGracePeriodSeconds, or the kubelet SIGKILLs mid-review and the apiserver reports a failed write.")
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
	// The Server treats a negative limit as "no cap", which is only ever
	// right in a test. Reached through the flag it would silently remove the
	// body limit from a process on the apiserver's write path, so the flag
	// refuses it rather than quietly obeying.
	if maxRequestBytes <= 0 {
		fmt.Fprintf(os.Stderr, "--max-request-bytes must be positive, got %d\n", maxRequestBytes)
		os.Exit(1)
	}
	if requestTimeout <= 0 {
		fmt.Fprintf(os.Stderr, "--request-timeout must be positive, got %s\n", requestTimeout)
		os.Exit(1)
	}
	if shutdownTimeout <= 0 {
		fmt.Fprintf(os.Stderr, "--shutdown-timeout must be positive, got %s\n", shutdownTimeout)
		os.Exit(1)
	}
	if initialSyncPar < 0 {
		fmt.Fprintf(os.Stderr, "--initial-sync-workers must not be negative, got %d\n", initialSyncPar)
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
	cacheOpts, err := webhookserver.CacheOptionsFromSelectorJSON(cacheSelector, enableXRDSupport, enableCRDSupport)
	if err != nil {
		logger.Error(err, "invalid --cache-label-selector")
		os.Exit(1)
	}
	opts.Cache = cacheOpts
	opts.Client = webhookserver.ClientOptions()
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), opts)
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	registry := webhookserver.NewRegistry()
	metricsReg := prometheus.NewRegistry()
	// A dedicated registry starts empty — unlike the process-wide default
	// one, it has no Go runtime or process collectors. Without those,
	// /metrics would expose this operator's own counters and nothing about
	// the process serving them: no resident memory, no goroutine count, no
	// GC behaviour, which makes the replica's memory footprint — the thing
	// --cache-label-selector exists to control — unmeasurable from outside
	// the pod.
	//
	// They are not registered here. controller-runtime's own registry,
	// which CombinedGatherer pairs with this one, already carries both —
	// and its Go collector is configured with the full runtime/metrics
	// set, a superset of the plain one. Registering a second copy here
	// would be a duplicate metric name, and prometheus.Gatherers fails the
	// whole scrape on one of those, taking every dco_webhook_* series with
	// it. Asserted by TestCombinedGatherer_NoDuplicateSeries.
	// The gatherer is a pair, not just metricsReg: controller-runtime
	// registers its own workqueue depth/latency and reconcile counters on
	// its package-global registry, and this binary serves /metrics from a
	// dedicated one — so without this, a replica's registry reconcile loop
	// was the one controller in the system with no queue-depth signal at
	// all. Gathering both means the shipped "Controller health" dashboard
	// row covers the webhook-server as well as the manager.
	metrics := webhookserver.NewMetrics(metricsReg, webhookserver.CombinedGatherer(metricsReg))

	// The publisher writes this replica's servable target set into its own
	// Lease, which is how the operator knows it is safe to move a target
	// onto this instance. Its identity comes from the downward API; a
	// Deployment that predates those env vars leaves it disabled, which
	// costs nothing but the ability to receive moved work safely.
	publisher := &webhookserver.TargetPublisher{
		Client:     mgr.GetClient(),
		Registry:   registry,
		ServerName: serverName,
		Namespace:  os.Getenv("POD_NAMESPACE"),
		PodName:    os.Getenv("POD_NAME"),
		PodUID:     types.UID(os.Getenv("POD_UID")),
	}
	publisher.Init()
	if !publisher.Enabled() {
		logger.Info("served-target publishing is disabled: POD_NAME/POD_NAMESPACE are not set. " +
			"Conversions are unaffected, but the operator cannot verify this instance is ready before moving a target onto it")
	}

	reconciler := &webhookserver.Reconciler{
		Client: mgr.GetClient(), ServerName: serverName, Registry: registry, Metrics: metrics,
		EnableXRDSupport: enableXRDSupport, EnableCRDSupport: enableCRDSupport,
		InitialSyncWorkers: initialSyncPar, Publisher: publisher,
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

	server := &webhookserver.Server{
		Registry: registry, Metrics: metrics,
		MaxRequestBytes: maxRequestBytes, RequestTimeout: requestTimeout,
	}

	ctx := rootCtx

	// Both servers carry the same timeouts. The conversion endpoint needs
	// them because it is in the apiserver's write path; the plain endpoint
	// needs them because it also serves /debug/registry, and an endpoint
	// that can be held open is an endpoint that can be used to hold the
	// process open. See webhookserver's Default*Timeout constants for the
	// reasoning behind each value.
	conversionSrv := &http.Server{
		Addr:              conversionAddr,
		Handler:           server.ConversionMux(),
		TLSConfig:         &tls.Config{GetCertificate: certReloader.GetCertificate, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: webhookserver.DefaultReadHeaderTimeout,
		ReadTimeout:       webhookserver.DefaultReadTimeout,
		WriteTimeout:      webhookserver.DefaultWriteTimeout,
		IdleTimeout:       webhookserver.DefaultIdleTimeout,
		MaxHeaderBytes:    webhookserver.DefaultMaxHeaderBytes,
	}
	plainSrv := &http.Server{
		Addr:              plainAddr,
		Handler:           server.PlainMux(),
		ReadHeaderTimeout: webhookserver.DefaultReadHeaderTimeout,
		ReadTimeout:       webhookserver.DefaultReadTimeout,
		WriteTimeout:      webhookserver.DefaultWriteTimeout,
		IdleTimeout:       webhookserver.DefaultIdleTimeout,
		MaxHeaderBytes:    webhookserver.DefaultMaxHeaderBytes,
	}

	mgrErrCh := make(chan error, 1)
	go func() { mgrErrCh <- mgr.Start(ctx) }()
	go certReloader.Run(ctx)

	// The plain endpoint comes up before the cache sync, not after it. It
	// carries /healthz, /readyz and /metrics, and a cold start is the one
	// time those are worth having: previously nothing listened until the
	// registry was fully compiled, so a slow start was indistinguishable
	// from a hung process — every probe got connection-refused and the
	// only evidence was the pod log. /readyz stays false throughout (it
	// reads the same gate SetReady flips below), so nothing joins the
	// Service early; what changes is that the startupProbe now measures a
	// live process rather than an absent listener.
	go func() {
		logger.Info("serving health/metrics/debug", "address", plainAddr)
		if err := plainSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(err, "plain HTTP server exited unexpectedly")
			os.Exit(1)
		}
	}()

	if !mgr.GetCache().WaitForCacheSync(ctx) {
		logger.Error(errors.New("cache sync failed"), "unable to sync cache before initial registry population")
		os.Exit(1)
	}
	// Readiness stays strictly behind a completed InitialSync. A
	// "--registry-ready-timeout" that reported ready anyway after N
	// seconds was considered and deliberately not added: a replica that
	// joins the Service with a partially-populated registry answers
	// ConversionReviews for the targets it has not compiled yet with a
	// failure, and the apiserver turns that into a failed write on an
	// unrelated resource. An unavailable replica degrades throughput; a
	// half-loaded one corrupts the answer. The startupProbe is the
	// supported lever for a slow cold start, sized from the budget this
	// metric and log line publish. See docs/operations/capacity.md.
	syncStats, err := reconciler.InitialSync(ctx)
	if err != nil {
		logger.Error(err, "initial registry sync encountered errors; continuing, affected XRDs will retry via watch events")
	}
	// Published synchronously, before readiness rather than after: a
	// replica that is about to start taking traffic should already be
	// eligible to receive moved work, not eligible one heartbeat later.
	if err := publisher.Publish(ctx); err != nil {
		logger.Error(err, "unable to publish served targets after the initial sync; retrying on the heartbeat")
	}
	go publisher.Run(ctx)

	server.SetReady(true)
	logger.Info("registry synced, marking replica ready",
		"serverName", serverName,
		"targets", syncStats.Targets,
		"workers", effectiveInitialSyncWorkers(initialSyncPar, syncStats.Targets),
		"elapsed", syncStats.Duration.String())

	// The conversion endpoint, unlike the plain one, only starts once the
	// registry is populated: it is on the apiserver's write path, and a
	// listener that accepts before it can answer correctly is worse than
	// no listener at all.
	go func() {
		logger.Info("serving conversion requests", "address", conversionAddr)
		if err := conversionSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			logger.Error(err, "conversion server exited unexpectedly")
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

	// Stop advertising readiness first: a replica that is going away should
	// fail its readiness probe rather than keep being an Endpoint while it
	// drains. The preStop sleep is what actually buys the time for that to
	// propagate; this makes the state honest in the meantime.
	server.SetReady(false)

	// One deadline shared by both servers, sized by --shutdown-timeout.
	// It is deliberately long enough for an in-flight ConversionReview to
	// finish (the apiserver's own conversion timeout is a fixed 30s): a
	// shutdown that cuts a review short turns a routine rollout into a
	// failed write.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	logger.Info("draining", "shutdownTimeout", shutdownTimeout)
	_ = conversionSrv.Shutdown(shutdownCtx)
	_ = plainSrv.Shutdown(shutdownCtx)
}
