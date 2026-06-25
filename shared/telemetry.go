// -------------------------------------------------------------------------------
// Shared Telemetry - OpenTelemetry Tracing Initialization
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Provides OpenTelemetry tracing setup shared across all temporal worker
// domains. Exports traces to Tempo via OTLP gRPC and sets up context
// propagation for service graph visibility in Grafana.
// -------------------------------------------------------------------------------

package shared

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const defaultOTLPEndpoint = "tempo.service.consul:4317"

var tracer trace.Tracer

// InitTracer configures the global OpenTelemetry tracer provider with OTLP
// gRPC export. The serviceName determines the node identity in the Tempo
// service graph. Returns a shutdown function that should be deferred.
func InitTracer(ctx context.Context, serviceName string) func(context.Context) error {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = defaultOTLPEndpoint
	}
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		log.Printf("Warning: failed to create OTLP exporter: %v (tracing disabled)", err)
		return func(context.Context) error { return nil }
	}

	// Merge the custom service.name onto the SDK's default resource. The custom
	// attributes are intentionally schemaless (resource.NewSchemaless, not
	// NewWithAttributes+semconv.SchemaURL): resource.Default() already carries
	// the SDK's own semconv schema URL, and resource.Merge refuses to merge two
	// resources with conflicting non-empty schema URLs. Passing our (older)
	// semconv.SchemaURL here made Merge fail, and the old fallback dropped to a
	// bare resource.Default() -- so traces exported as "unknown_service:*" and
	// never showed up under the worker's name in Tempo.
	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		log.Printf("Warning: failed to create resource: %v", err)
		res = resource.Default()
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	tracer = tp.Tracer(serviceName)

	log.Printf("OpenTelemetry tracing initialized (service: %s, endpoint: %s)", serviceName, endpoint)
	return tp.Shutdown
}

// Tracer returns the global tracer instance.
func Tracer() trace.Tracer {
	if tracer == nil {
		return otel.Tracer("temporal-workers")
	}
	return tracer
}

// StartSpan creates a new span with the given name and attributes.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// StartClientSpan creates a SpanKindClient span for outbound service calls.
// Client spans with peer.service attributes produce edges in the Tempo
// service graph.
func StartClientSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
}

// PeerServiceAttr returns a peer.service attribute for service graph edges.
func PeerServiceAttr(name string) attribute.KeyValue {
	return semconv.PeerService(name)
}

// StartPeerSpan opens a SpanKindClient span named op for an outbound call to
// peer, attaching peer.service=peer so the call produces an edge in the Tempo
// service graph. Extra attributes are appended. Callers must defer span.End()
// and pass the returned context to the downstream call so the request nests
// under the span. It folds the StartClientSpan + PeerServiceAttr pair into one.
func StartPeerSpan(ctx context.Context, peer, op string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return StartClientSpan(ctx, op, append([]attribute.KeyValue{PeerServiceAttr(peer)}, attrs...)...)
}

// otelTransport wraps base with an OTel transport whose spans are named
// "<service>." + request path, producing per-call edges in the service graph.
// base may be nil (defaults to http.DefaultTransport); pass a pre-configured
// transport (e.g. one already carrying TLS settings) to instrument it in place.
func otelTransport(service string, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return service + "." + r.URL.Path
		}),
	)
}
