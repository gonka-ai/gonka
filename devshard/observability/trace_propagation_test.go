package observability

import (
	"context"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TestTracePropagation_W3CExtractInject verifies the two halves of the
// distributed-trace contract:
//  1. ExtractRequestContext reads a W3C `traceparent` header and produces a
//     context whose StartRequest span inherits that trace id (parent id).
//  2. InjectRequestContext writes the active context back into outgoing
//     HTTP headers so downstream services see the same trace id.
func TestTracePropagation_W3CExtractInject(t *testing.T) {
	// Install a real (in-memory) tracer provider so spans are sampled and
	// have non-zero ids. Restore the previous global on exit so other
	// tests that rely on the default no-op provider stay deterministic.
	prev := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
		otel.SetTextMapPropagator(prevProp)
	})

	const incomingTraceID = "0af7651916cd43dd8448eb211c80319c"
	incomingHeader := http.Header{}
	incomingHeader.Set("traceparent",
		"00-"+incomingTraceID+"-b7ad6b7169203331-01")

	ctx := ExtractRequestContext(context.Background(), incomingHeader)

	tracer := Default.Request()
	ctx, op := tracer.StartRequest(ctx, http.MethodGet, "/v1/test")
	defer op.Finish(nil)

	gotTrace := op.span.SpanContext().TraceID().String()
	if gotTrace != incomingTraceID {
		t.Fatalf("inbound trace id not propagated to span: got %q want %q",
			gotTrace, incomingTraceID)
	}

	out := http.Header{}
	InjectRequestContext(ctx, out)
	tp2 := out.Get("traceparent")
	if tp2 == "" {
		t.Fatalf("InjectRequestContext did not set traceparent header")
	}
	// The injected header must reference the same trace id.
	if !containsTraceID(tp2, incomingTraceID) {
		t.Fatalf("outgoing traceparent %q does not carry trace id %q",
			tp2, incomingTraceID)
	}
}

func containsTraceID(traceparent, traceID string) bool {
	// W3C format: "00-<traceID>-<spanID>-<flags>"; the trace id is the
	// second dash-separated segment.
	parts := splitDash(traceparent)
	return len(parts) >= 2 && parts[1] == traceID
}

func splitDash(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == '-' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
