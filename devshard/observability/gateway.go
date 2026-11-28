package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

const (
	GatewayServiceName = "gateway"

	gatewayTracer      = tracerID("gateway")
	gatewayRequestSpan = spanID("gateway.request")
)

// StartGatewayRequest opens the root span for an inbound gateway HTTP request.
// The caller must End the returned span when the request finishes (including
// after a streaming response completes).
func StartGatewayRequest(ctx context.Context) (context.Context, trace.Span) {
	return otel.Tracer(string(gatewayTracer)).Start(
		ctx,
		string(gatewayRequestSpan),
		trace.WithSpanKind(trace.SpanKindServer),
	)
}
