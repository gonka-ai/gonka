package api

import (
	"net/http"
	"testing"

	"devshard/cmd/gateway/internal/logcapture"

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

// Test flow:
//  1. Install a sampling tracer provider and a log capture.
//  2. Send one chat through the harness and flush the journal.
//  3. Assert the race ran with the request id bound to its context, so host calls carry it as X-Request-Id.
//  4. Assert the "request finished" line carries request_id and the request span's trace_id beside request.
func TestARequestsLogLineAndRaceCarryItsIdentity(t *testing.T) {
	previous := otel.GetTracerProvider()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(t.Context())
	})
	logged := logcapture.Install(t)
	live := newHarness(t)

	if recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil); recorder.Code != http.StatusOK {
		t.Fatalf("chat: got %d (%s)", recorder.Code, recorder.Body.String())
	}
	live.events.Flush()

	if bound := live.inference.racedAs.Load(); bound == nil || *bound != "request-1" {
		t.Fatalf("request id on the race context = %v, want request-1", bound)
	}
	line, found := logged.Find("request finished")
	if !found {
		t.Fatalf("no request finished line among %+v", logged.All())
	}
	wantTrace := live.inference.racedIn.Load().TraceID().String()
	if logcapture.Field(line, "request") != "request-1" || logcapture.Field(line, "request_id") != "request-1" || logcapture.Field(line, "trace_id") != wantTrace {
		t.Fatalf("request finished fields = %+v, want request and request_id request-1 and trace_id %s", line.Fields, wantTrace)
	}
}
