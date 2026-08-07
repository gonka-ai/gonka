package observability

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// RequestIDMetadataKey carries the caller's request id across a gRPC hop, the
// same identity the HTTP hops pass as X-Request-Id. gRPC lowercases metadata
// keys, so this must stay lowercase to match on the receiving side.
const RequestIDMetadataKey = "x-request-id"

// UnaryClientTraceInterceptor injects W3C trace context and the caller's
// request id into outgoing metadata. Register it on clients whose server side
// runs UnaryServerTraceInterceptor so both ends land on one trace.
func UnaryClientTraceInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		return invoker(OutgoingTraceContext(ctx), method, req, reply, cc, opts...)
	}
}

// StreamClientTraceInterceptor is UnaryClientTraceInterceptor for streaming RPCs.
func StreamClientTraceInterceptor() grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		return streamer(OutgoingTraceContext(ctx), desc, cc, method, opts...)
	}
}

// OutgoingTraceContext returns ctx with traceparent and request-id metadata set
// for an outbound gRPC call.
func OutgoingTraceContext(ctx context.Context) context.Context {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		md = metadata.New(nil)
	} else {
		md = md.Copy()
	}
	otel.GetTextMapPropagator().Inject(ctx, grpcMetadataCarrier{MD: md})
	if id, ok := RequestID(ctx); ok {
		md.Set(RequestIDMetadataKey, id)
	}
	return metadata.NewOutgoingContext(ctx, md)
}

// IncomingTraceContext lifts the caller's trace context and request id off
// inbound metadata. Handlers that do not want a server span can call this
// directly instead of registering the interceptor.
func IncomingTraceContext(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	ctx = otel.GetTextMapPropagator().Extract(ctx, grpcMetadataCarrier{MD: md})
	for _, raw := range md.Get(RequestIDMetadataKey) {
		if id := strings.TrimSpace(raw); id != "" {
			return SetRequestID(ctx, id)
		}
	}
	return ctx
}

// UnaryServerTraceInterceptor continues the caller's trace, binds the caller's
// request id onto the handler context, and opens a server span per RPC.
// tracerName names the instrumentation scope (e.g. "mock-dapi.nodemanager").
func UnaryServerTraceInterceptor(tracerName string) grpc.UnaryServerInterceptor {
	if tracerName == "" {
		tracerName = "grpc.server"
	}
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		ctx = IncomingTraceContext(ctx)

		fullMethod := ""
		if info != nil {
			fullMethod = info.FullMethod
		}
		service, method := splitGRPCMethod(fullMethod)
		ctx, span := otel.Tracer(tracerName).Start(
			ctx,
			strings.TrimPrefix(fullMethod, "/"),
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("rpc.system", "grpc"),
				attribute.String("rpc.service", service),
				attribute.String("rpc.method", method),
			),
		)
		defer span.End()

		resp, err := handler(ctx, req)
		span.SetAttributes(attribute.String("rpc.grpc.status_code", status.Code(err).String()))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return resp, err
	}
}
