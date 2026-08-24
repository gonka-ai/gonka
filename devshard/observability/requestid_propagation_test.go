package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	commonobs "common/observability"

	"devshard/logging"
)

func installTestPropagator(t *testing.T) {
	t.Helper()
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
}

func TestBindRequestID_PrefersInboundOverExisting(t *testing.T) {
	ctx, existing := logging.WithRequestID(context.Background(), "req-local")
	require.Equal(t, "req-local", existing)

	ctx = BindRequestID(ctx, "req-from-gateway")
	got, ok := logging.RequestID(ctx)
	require.True(t, ok)
	require.Equal(t, "req-from-gateway", got, "inbound X-Request-Id must win over a locally minted id")
}

func TestBindRequestID_MintsWhenInboundAbsent(t *testing.T) {
	ctx := BindRequestID(context.Background(), "")
	got, ok := logging.RequestID(ctx)
	require.True(t, ok)
	require.NotEmpty(t, got)
	require.Contains(t, got, "req-")
}

func TestBindRequestID_TrimsInbound(t *testing.T) {
	ctx := BindRequestID(context.Background(), "  req-trimmed  ")
	got, ok := logging.RequestID(ctx)
	require.True(t, ok)
	require.Equal(t, "req-trimmed", got)
}

func TestBindRequestID_RejectsInvalidAndMints(t *testing.T) {
	ctx := BindRequestID(context.Background(), "req id\nwith/control")
	got, ok := logging.RequestID(ctx)
	require.True(t, ok)
	require.NotEqual(t, "req id\nwith/control", got)
	require.Contains(t, got, "req-")
	_, valid := commonobs.NormalizeRequestID(got)
	require.True(t, valid, "minted id must itself be normalize-safe")
}

func TestBindRequestID_RejectsOversized(t *testing.T) {
	oversized := strings.Repeat("a", commonobs.MaxRequestIDLength+1)
	ctx := BindRequestID(context.Background(), oversized)
	got, ok := logging.RequestID(ctx)
	require.True(t, ok)
	require.NotEqual(t, oversized, got)
	require.LessOrEqual(t, len(got), commonobs.MaxRequestIDLength)
}

func TestBindRequestID_InvalidInboundKeepsExisting(t *testing.T) {
	ctx, existing := logging.WithRequestID(context.Background(), "req-local")
	ctx = BindRequestID(ctx, "bad\nid")
	got, ok := logging.RequestID(ctx)
	require.True(t, ok)
	require.Equal(t, existing, got, "invalid inbound must not clobber an existing id")
}

func TestBindEchoRequestID_PrefersInboundAndEchoesResponse(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	req.Header.Set(RequestIDHeader, "req-inbound-1")
	// Seed a different id on the request context to simulate a prior mint.
	seeded, _ := logging.WithRequestID(req.Context(), "req-already-on-ctx")
	req = req.WithContext(seeded)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	ctx := BindEchoRequestID(c)
	got, ok := logging.RequestID(ctx)
	require.True(t, ok)
	require.Equal(t, "req-inbound-1", got)
	require.Equal(t, "req-inbound-1", rec.Header().Get(RequestIDHeader))
	require.Equal(t, "req-inbound-1", c.Request().Header.Get(RequestIDHeader))
	// Context on the echo request must match.
	bound, ok := logging.RequestID(c.Request().Context())
	require.True(t, ok)
	require.Equal(t, "req-inbound-1", bound)
}

func TestInjectOutboundHeaders_SetsTraceRequestAndInference(t *testing.T) {
	installTestPropagator(t)

	ctx, _ := logging.WithRequestID(context.Background(), "req-out-1")
	ctx = WithInferenceID(ctx, 4711)
	ctx, op := Default.Request().StartRequest(ctx, http.MethodPost, "/sessions/:id/chat/completions")
	defer op.Finish(nil)

	header := http.Header{}
	InjectOutboundHeaders(ctx, header)

	require.NotEmpty(t, header.Get("traceparent"), "traceparent must be injected")
	require.Equal(t, "req-out-1", header.Get(RequestIDHeader))
	require.Equal(t, "4711", header.Get(InferenceIDHeader))
}

func TestInjectOutboundHeaders_OmitsInferenceWhenUnset(t *testing.T) {
	installTestPropagator(t)

	ctx, _ := logging.WithRequestID(context.Background(), "req-out-2")
	header := http.Header{}
	InjectOutboundHeaders(ctx, header)

	require.Equal(t, "req-out-2", header.Get(RequestIDHeader))
	require.Empty(t, header.Get(InferenceIDHeader))
}
