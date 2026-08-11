package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestEchoMiddlewareSkipsPing(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})

	e := echo.New()
	e.Use(EchoMiddleware())
	e.GET("/clock", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
	e.GET("/work", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/clock", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("ping status=%d", rec.Code)
	}
	if got := len(recorder.Ended()); got != 0 {
		t.Fatalf("ping produced %d spans, want 0", got)
	}

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/work", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("work status=%d", rec.Code)
	}
	if got := len(recorder.Ended()); got != 1 {
		t.Fatalf("work produced %d spans, want 1", got)
	}
}
