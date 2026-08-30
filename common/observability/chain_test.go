package observability_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"common/observability"
)

func TestChainStartStoreQueryCreatesSpan(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	ctx, op := observability.Chain.StartStoreQuery(context.Background(), "inference", true, 42)
	require.NotNil(t, op)
	op.Finish(nil)

	spans := sr.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "chain.store.query", spans[0].Name())
	require.Equal(t, "inference", attrString(spans[0].Attributes(), "store.key"))
	require.Equal(t, true, attrBool(spans[0].Attributes(), "query.with_proof"))
	require.Equal(t, int64(42), attrInt64(spans[0].Attributes(), "query.height"))
	_ = ctx
}

// TestChainSetQueryTransportRecordsTheActiveTransport must be the only test in
// this binary that installs a meter provider: the gauge is built from the global
// meter at package init, and OTel binds such an instrument to the first provider
// installed and no other.
func TestChainSetQueryTransportRecordsTheActiveTransport(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	observability.Chain.SetQueryTransport(observability.TransportCometRPC)

	// Both series are reported every time. A gauge that only emitted the
	// active transport would leave the previous one stuck at 1 forever.
	require.Equal(t, map[string]int64{
		observability.TransportGRPC:     0,
		observability.TransportCometRPC: 1,
	}, collectTransportGauge(t, reader))

	observability.Chain.SetQueryTransport(observability.TransportGRPC)

	require.Equal(t, map[string]int64{
		observability.TransportGRPC:     1,
		observability.TransportCometRPC: 0,
	}, collectTransportGauge(t, reader))
}

func collectTransportGauge(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))

	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "chain.query.transport.active" {
				continue
			}
			gauge, ok := m.Data.(metricdata.Gauge[int64])
			require.True(t, ok, "expected an int64 gauge, got %T", m.Data)

			values := make(map[string]int64, len(gauge.DataPoints))
			for _, point := range gauge.DataPoints {
				transport, ok := point.Attributes.Value("chain.transport")
				require.True(t, ok, "every point must name its transport")
				values[transport.AsString()] = point.Value
			}
			return values
		}
	}
	t.Fatal("chain.query.transport.active was never recorded")
	return nil
}

func attrString(attrs []attribute.KeyValue, key string) string {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsString()
		}
	}
	return ""
}

func attrBool(attrs []attribute.KeyValue, key string) bool {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsBool()
		}
	}
	return false
}

func attrInt64(attrs []attribute.KeyValue, key string) int64 {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsInt64()
		}
	}
	return 0
}
