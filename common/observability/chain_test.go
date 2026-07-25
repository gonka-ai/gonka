package observability_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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
