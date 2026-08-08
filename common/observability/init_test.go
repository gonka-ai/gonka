package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func restoreOTelGlobals(t *testing.T) {
	t.Helper()
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	prevResource := newOTelResource
	prevExporter := newOTLPExporter
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
		newOTelResource = prevResource
		newOTLPExporter = prevExporter
	})
}

func TestInit_ExporterFailureDegradesWithoutError(t *testing.T) {
	restoreOTelGlobals(t)

	t.Setenv("TEST_OTEL_ENABLED", "true")
	t.Setenv("OTEL_ENDPOINT", "http://127.0.0.1:4317")

	boom := errors.New("exporter boom")
	newOTLPExporter = func(context.Context, ...otlptracegrpc.Option) (sdktrace.SpanExporter, error) {
		return nil, boom
	}

	var logged error
	var event string
	result, err := Init(context.Background(), Config{
		ServiceName: "test-service",
		EnabledEnv:  "TEST_OTEL_ENABLED",
		LogError: func(e, message string, cause error, args ...any) error {
			event = e
			logged = cause
			return cause
		},
	})
	require.NoError(t, err, "exporter failure must not fail process boot")
	require.False(t, result.Ready)
	require.NotNil(t, result.Shutdown)
	require.NoError(t, result.Shutdown(context.Background()))
	require.Equal(t, "init.trace_exporter_failed", event)
	require.ErrorIs(t, logged, boom)
	require.IsType(t, propagation.TraceContext{}, otel.GetTextMapPropagator(),
		"propagator must still be installed after degrade")
}

func TestInit_ResourceFailureDegradesWithoutError(t *testing.T) {
	restoreOTelGlobals(t)

	t.Setenv("TEST_OTEL_ENABLED", "true")
	t.Setenv("OTEL_ENDPOINT", "http://127.0.0.1:4317")

	boom := errors.New("resource boom")
	newOTelResource = func(context.Context, ...resource.Option) (*resource.Resource, error) {
		return nil, boom
	}
	newOTLPExporter = func(context.Context, ...otlptracegrpc.Option) (sdktrace.SpanExporter, error) {
		t.Fatal("exporter must not be constructed after resource failure")
		return nil, nil
	}

	result, err := Init(context.Background(), Config{
		ServiceName: "test-service",
		EnabledEnv:  "TEST_OTEL_ENABLED",
		LogError: func(event, message string, cause error, args ...any) error {
			require.Equal(t, "init.resource_failed", event)
			require.ErrorIs(t, cause, boom)
			return cause
		},
	})
	require.NoError(t, err)
	require.False(t, result.Ready)
	require.NotNil(t, result.Shutdown)
}

func TestInit_ReadyWiresExporter(t *testing.T) {
	restoreOTelGlobals(t)

	t.Setenv("TEST_OTEL_ENABLED", "true")
	t.Setenv("OTEL_ENDPOINT", "http://127.0.0.1:4317")

	// Use a real in-process exporter construction against a valid URL shape;
	// connect is lazy so New should succeed without a collector.
	result, err := Init(context.Background(), Config{
		ServiceName:  "test-service",
		EnabledEnv:   "TEST_OTEL_ENABLED",
		BatchTimeout: time.Millisecond,
	})
	require.NoError(t, err)
	require.True(t, result.Ready)
	require.NotNil(t, result.Shutdown)
	require.NoError(t, result.Shutdown(context.Background()))

	_, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	require.True(t, ok, "ready init must install an SDK tracer provider")
}
