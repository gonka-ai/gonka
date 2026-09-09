package observability_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"

	"common/observability"
)

// stubConn answers every invocation without touching a network.
type stubConn struct{}

func (stubConn) Invoke(context.Context, string, any, any, ...grpc.CallOption) error { return nil }

func (stubConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, nil
}

func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	return recorder
}

func TestObservedConnLabelsSpansWithItsTransport(t *testing.T) {
	cases := []struct {
		name string
		conn observability.ObservedConn
		want string
	}{
		{"direct", observability.NewObservedConn(stubConn{}), observability.TransportGRPC},
		{
			"fallback",
			observability.NewObservedConnWithTransport(stubConn{}, observability.TransportCometRPC),
			observability.TransportCometRPC,
		},
		// A conn built without a transport predates the label and was always
		// direct gRPC, so it must not report an empty one.
		{"zero value", observability.ObservedConn{ClientConnInterface: stubConn{}}, observability.TransportGRPC},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := recordSpans(t)

			require.NoError(t, tc.conn.Invoke(
				context.Background(), "/inference.inference.Query/Params", nil, nil))

			spans := recorder.Ended()
			require.Len(t, spans, 1)
			require.Equal(t, tc.want, attrString(spans[0].Attributes(), "chain.transport"))
		})
	}
}

func TestObservedConnSkipsABCIQuery(t *testing.T) {
	recorder := recordSpans(t)

	// Call sites wrap these in chain.store.query spans themselves, so a span
	// here would double-count them.
	conn := observability.NewObservedConn(stubConn{})
	require.NoError(t, conn.Invoke(
		context.Background(), "/cosmos.base.tendermint.v1beta1.Service/ABCIQuery", nil, nil))

	require.Empty(t, recorder.Ended())
}
