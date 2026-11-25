package api

import (
	"net/http"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Test flow:
//  1. Install a sampling tracer provider, as DEVSHARD_OTEL_ENABLED does at boot.
//  2. Send one chat through the harness.
//  3. Assert the race ran under a live span, so every host call it makes carries the request's traceparent.
func TestAChatRacesUnderItsOwnRequestSpan(t *testing.T) {
	previous := otel.GetTracerProvider()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(t.Context())
	})
	live := newHarness(t)

	if recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil); recorder.Code != http.StatusOK {
		t.Fatalf("chat: got %d (%s)", recorder.Code, recorder.Body.String())
	}

	spanContext := live.inference.racedIn.Load()
	if spanContext == nil || !spanContext.IsValid() || !spanContext.IsSampled() {
		t.Fatalf("race span context = %+v, want a sampled request span", spanContext)
	}
}
