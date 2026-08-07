package nodemanager

import (
	"context"
	"net"
	"testing"

	"common/nodemanager/gen"
	"common/observability"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TestClientPropagatesTraceAndRequestID pins the T5 wiring on the dialed
// client: node selection must arrive at dapi on the caller's trace, with the
// caller's request id, without every call site remembering to inject metadata.
func TestClientPropagatesTraceAndRequestID(t *testing.T) {
	prevProp := otel.GetTextMapPropagator()
	prevTP := otel.GetTracerProvider()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	t.Cleanup(func() {
		otel.SetTextMapPropagator(prevProp)
		otel.SetTracerProvider(prevTP)
	})

	var acquireMD, releaseMD metadata.MD
	srv := &mockServer{
		acquireFunc: func(ctx context.Context, _ *gen.AcquireMLNodeRequest) (*gen.AcquireMLNodeResponse, error) {
			acquireMD, _ = metadata.FromIncomingContext(ctx)
			return &gen.AcquireMLNodeResponse{LockId: "lock-1", NodeId: "node-1", Endpoint: "http://node-1"}, nil
		},
		releaseFunc: func(ctx context.Context, _ *gen.ReleaseMLNodeRequest) (*gen.ReleaseMLNodeResponse, error) {
			releaseMD, _ = metadata.FromIncomingContext(ctx)
			return &gen.ReleaseMLNodeResponse{}, nil
		},
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer()
	gen.RegisterNodeManagerServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	t.Cleanup(grpcSrv.Stop)

	client, err := NewClient(lis.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	ctx, span := otel.Tracer("test").Start(context.Background(), "caller")
	defer span.End()
	ctx = observability.SetRequestID(ctx, "req-t5")
	traceID := span.SpanContext().TraceID().String()

	_, err = client.Acquire(ctx, "Qwen/Test", nil, "12")
	require.NoError(t, err)
	require.NoError(t, client.Release(ctx, "lock-1", gen.ReleaseOutcome_SUCCESS))

	for name, md := range map[string]metadata.MD{"acquire": acquireMD, "release": releaseMD} {
		require.Len(t, md.Get("traceparent"), 1, "%s: missing traceparent", name)
		require.Contains(t, md.Get("traceparent")[0], traceID, "%s: wrong trace", name)
		require.Equal(t, []string{"req-t5"}, md.Get(observability.RequestIDMetadataKey), "%s: missing request id", name)
	}
}
