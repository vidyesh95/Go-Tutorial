// Package telemetry builds the OpenTelemetry tracing pieces every service in this track shares.
//
// DISTRIBUTED TRACING in one paragraph: a request that crosses services leaves a trail of spans
// (one per unit of work). All spans of one request share a trace ID, and each span records its
// parent's span ID, so a backend (Jaeger, Tempo, Datadog...) can draw the whole tree. The glue
// is context propagation: the caller injects a `traceparent` header (HTTP) or metadata entry
// (gRPC) and the callee extracts it, so its spans join the same trace.
//
// Design choice: no globals. Real programs often call otel.SetTracerProvider once and let the
// instrumentation libraries read it. Here each service gets its OWN TracerProvider (so spans
// carry the right service.name even when the demo runs three services in one process) and
// passes it explicitly to the otelhttp / otelgrpc instrumentation.
package telemetry

import (
	"context"
	"io"
	"os"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Propagator is the W3C Trace Context format (the `traceparent` header), the cross-vendor
// standard. Every service must use the same format or traces break at the boundary.
func Propagator() propagation.TextMapPropagator { return propagation.TraceContext{} }

// NewProvider returns a TracerProvider whose spans are tagged with service.name and sent to exp.
// A nil exporter is allowed: spans are still created (so trace context keeps propagating
// through this service) but are discarded, which is the right default when nobody is listening.
//
// Spans are exported in batches by a background goroutine, so call Shutdown (or ForceFlush)
// before the process exits or the last spans are lost.
func NewProvider(service string, exp sdktrace.SpanExporter) *sdktrace.TracerProvider {
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", service))),
	}
	if exp != nil {
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	return sdktrace.NewTracerProvider(opts...)
}

// StdoutExporter prints each finished span as JSON. Good for learning and local debugging;
// production services export with OTLP to a collector (go.opentelemetry.io/otel/exporters/otlp).
func StdoutExporter(w io.Writer) (sdktrace.SpanExporter, error) {
	return stdouttrace.New(stdouttrace.WithWriter(w))
}

// FromEnv is what the service mains use: with TRACE_STDOUT=1 spans are printed to stdout,
// otherwise they are created but discarded. The returned func flushes pending spans; defer it.
func FromEnv(service string) (*sdktrace.TracerProvider, func()) {
	var exp sdktrace.SpanExporter
	if os.Getenv("TRACE_STDOUT") == "1" {
		exp, _ = StdoutExporter(os.Stdout) // New only fails on invalid options; none are passed
	}
	tp := NewProvider(service, exp)
	return tp, func() { tp.Shutdown(context.Background()) }
}
