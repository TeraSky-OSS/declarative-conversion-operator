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

package webhookserver

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Tracer is the package-level tracer used on the conversion hot path.
// Defaults to a no-op tracer so disabled tracing adds negligible overhead
// beyond a few function calls that the compiler can inline.
var Tracer trace.Tracer = noop.NewTracerProvider().Tracer("declarative-conversion-operator/webhook-server")

// InitTracing configures an OTLP/gRPC exporter when endpoint is non-empty.
// sampleRatio is clamped to [0, 1]. insecure=true disables TLS (for trusted
// in-cluster collectors only); TLS is the default. Returns a shutdown func;
// callers must invoke it on process exit. When endpoint is empty, this is a
// no-op and Tracer remains the package no-op tracer.
func InitTracing(ctx context.Context, endpoint string, sampleRatio float64, insecure bool) (func(context.Context) error, error) {
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	if sampleRatio < 0 {
		sampleRatio = 0
	}
	if sampleRatio > 1 {
		sampleRatio = 1
	}

	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
	if insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP trace exporter: %w", err)
	}

	// Schema-less override attributes avoid ErrSchemaURLConflict when
	// merging with resource.Default()'s detectors (which may use a newer
	// semconv schema than this import path).
	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(semconv.ServiceName("declarative-conversion-webhook-server")),
	)
	if err != nil {
		return nil, fmt.Errorf("building OTel resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRatio))),
	)
	otel.SetTracerProvider(tp)
	Tracer = tp.Tracer("declarative-conversion-operator/webhook-server")
	return tp.Shutdown, nil
}
