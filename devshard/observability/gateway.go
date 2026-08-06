package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

const (
	// GatewayServiceName is the OTel service.name for the gateway binary.
	GatewayServiceName = "devshardctl"

	gatewayTracer      = tracerID("devshardctl.gateway")
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
