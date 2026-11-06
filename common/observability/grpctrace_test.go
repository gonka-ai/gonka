package observability_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"common/observability"
)

// useTraceContextPropagator mirrors what observability.Init installs at boot;
// the global default is a no-op propagator, so tests must opt in explicitly.
func useTraceContextPropagator(t *testing.T) {
	t.Helper()
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
}

const nodeManagerAcquire = "/nodemanager.NodeManager/AcquireMLNode"

func TestUnaryClientTraceInterceptorInjectsTraceparentAndRequestID(t *testing.T) {
	useTraceContextPropagator(t)
	recordSpans(t)

	ctx, span := otel.Tracer("test").Start(context.Background(), "caller")
	defer span.End()
	ctx = observability.SetRequestID(ctx, "req-abc")

	var got metadata.MD
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		got, _ = metadata.FromOutgoingContext(ctx)
		return nil
	}

	err := observability.UnaryClientTraceInterceptor()(ctx, nodeManagerAcquire, nil, nil, nil, invoker)
	require.NoError(t, err)

	require.Equal(t, []string{"req-abc"}, got.Get(observability.RequestIDMetadataKey))
	traceparent := got.Get("traceparent")
	require.Len(t, traceparent, 1, "traceparent must ride on outgoing metadata")
	require.Contains(t, traceparent[0], span.SpanContext().TraceID().String())
}

func TestUnaryClientTraceInterceptorPreservesExistingMetadata(t *testing.T) {
	useTraceContextPropagator(t)

	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "bearer x")

	var got metadata.MD
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		got, _ = metadata.FromOutgoingContext(ctx)
		return nil
	}
	require.NoError(t, observability.UnaryClientTraceInterceptor()(ctx, nodeManagerAcquire, nil, nil, nil, invoker))

	require.Equal(t, []string{"bearer x"}, got.Get("authorization"))
	// No span and no request id on ctx: nothing to add, nothing to lose.
	require.Empty(t, got.Get(observability.RequestIDMetadataKey))
}

// TestGRPCTraceRoundTrip is the C8 contract in miniature: what the client
// injects is what the server continues.
func TestGRPCTraceRoundTrip(t *testing.T) {
	useTraceContextPropagator(t)
	recorder := recordSpans(t)

	callerCtx, callerSpan := otel.Tracer("test").Start(context.Background(), "caller")
	callerCtx = observability.SetRequestID(callerCtx, "req-round-trip")
	wantTrace := callerSpan.SpanContext().TraceID()

	var outgoing metadata.MD
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		outgoing, _ = metadata.FromOutgoingContext(ctx)
		return nil
	}
	require.NoError(t, observability.UnaryClientTraceInterceptor()(callerCtx, nodeManagerAcquire, nil, nil, nil, invoker))
	callerSpan.End()

	serverCtx := metadata.NewIncomingContext(context.Background(), outgoing)
	var handlerCtx context.Context
	handler := func(ctx context.Context, _ any) (any, error) {
		handlerCtx = ctx
		return "ok", nil
	}
	resp, err := observability.UnaryServerTraceInterceptor("test.server")(
		serverCtx, nil, &grpc.UnaryServerInfo{FullMethod: nodeManagerAcquire}, handler)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)

	require.Equal(t, wantTrace, trace.SpanContextFromContext(handlerCtx).TraceID(),
		"server handler must run under the caller's trace")
	id, ok := observability.RequestID(handlerCtx)
	require.True(t, ok, "request id must survive the gRPC hop")
	require.Equal(t, "req-round-trip", id)

	var server, caller bool
	for _, s := range recorder.Ended() {
		switch s.Name() {
		case "nodemanager.NodeManager/AcquireMLNode":
			server = true
			require.Equal(t, wantTrace, s.SpanContext().TraceID())
			require.Equal(t, "nodemanager.NodeManager", attrString(s.Attributes(), "rpc.service"))
			require.Equal(t, "AcquireMLNode", attrString(s.Attributes(), "rpc.method"))
			require.Equal(t, codes.OK.String(), attrString(s.Attributes(), "rpc.grpc.status_code"))
		case "caller":
			caller = true
		}
	}
	require.True(t, caller, "caller span recorded")
	require.True(t, server, "server span recorded under the caller trace")
}

func TestUnaryServerTraceInterceptorWithoutMetadata(t *testing.T) {
	useTraceContextPropagator(t)
	recordSpans(t)

	// An unpropagated caller (or a probe) must not panic and must still get a span.
	handler := func(ctx context.Context, _ any) (any, error) { return nil, nil }
	_, err := observability.UnaryServerTraceInterceptor("test.server")(
		context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: nodeManagerAcquire}, handler)
	require.NoError(t, err)
}

func TestUnaryServerTraceInterceptorRecordsStatusCode(t *testing.T) {
	useTraceContextPropagator(t)
	recorder := recordSpans(t)

	wantErr := status.Error(codes.ResourceExhausted, "no available ML nodes")
	handler := func(ctx context.Context, _ any) (any, error) { return nil, wantErr }
	_, err := observability.UnaryServerTraceInterceptor("test.server")(
		context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: nodeManagerAcquire}, handler)
	require.True(t, errors.Is(err, wantErr))

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, codes.ResourceExhausted.String(), attrString(spans[0].Attributes(), "rpc.grpc.status_code"))
}

// TestIncomingTraceContextIgnoresBlankRequestID guards against binding an empty
// id, which would replace a locally minted one with nothing.
func TestIncomingTraceContextIgnoresBlankRequestID(t *testing.T) {
	useTraceContextPropagator(t)

	md := metadata.New(map[string]string{observability.RequestIDMetadataKey: "  "})
	ctx := observability.IncomingTraceContext(metadata.NewIncomingContext(context.Background(), md))
	_, ok := observability.RequestID(ctx)
	require.False(t, ok)
}

func TestIncomingTraceContextRejectsInvalidRequestID(t *testing.T) {
	useTraceContextPropagator(t)

	md := metadata.New(map[string]string{observability.RequestIDMetadataKey: "bad id\n"})
	ctx := observability.IncomingTraceContext(metadata.NewIncomingContext(context.Background(), md))
	_, ok := observability.RequestID(ctx)
	require.False(t, ok, "invalid x-request-id must not be bound")
}

func TestIncomingTraceContextRejectsOversizedRequestID(t *testing.T) {
	useTraceContextPropagator(t)

	oversized := strings.Repeat("a", observability.MaxRequestIDLength+1)
	md := metadata.New(map[string]string{observability.RequestIDMetadataKey: oversized})
	ctx := observability.IncomingTraceContext(metadata.NewIncomingContext(context.Background(), md))
	_, ok := observability.RequestID(ctx)
	require.False(t, ok)
}

func TestIncomingTraceContextSkipsInvalidThenAcceptsValid(t *testing.T) {
	useTraceContextPropagator(t)

	md := metadata.MD{
		observability.RequestIDMetadataKey: {"bad id", "req-good"},
	}
	ctx := observability.IncomingTraceContext(metadata.NewIncomingContext(context.Background(), md))
	id, ok := observability.RequestID(ctx)
	require.True(t, ok)
	require.Equal(t, "req-good", id)
}
