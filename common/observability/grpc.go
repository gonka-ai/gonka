package observability

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ObservedConn wraps a gRPC client connection with chain.grpc.query spans.
// ABCIQuery is excluded — call sites add chain.store.query spans explicitly
// (same split as decentralized-api/cosmosclient/query.go vs query_client_conn.go).
type ObservedConn struct {
	grpc.ClientConnInterface
}

// NewObservedConn returns a conn wrapper that records OTel spans.
func NewObservedConn(conn grpc.ClientConnInterface) ObservedConn {
	return ObservedConn{ClientConnInterface: conn}
}

func (c ObservedConn) Invoke(ctx context.Context, method string, args any, reply any, opts ...grpc.CallOption) error {
	if strings.HasSuffix(method, "/ABCIQuery") {
		return c.ClientConnInterface.Invoke(ctx, method, args, reply, opts...)
	}

	service, rpcMethod := splitGRPCMethod(method)
	queryCtx, queryOp := Chain.StartGRPCQuery(ctx, service, rpcMethod)
	var (
		err     error
		spanErr error
	)
	defer func() {
		Chain.SetRPCStatus(queryOp, status.Code(err).String())
		queryOp.FinishErr(&spanErr)
	}()

	queryCtx = injectGRPCTraceContext(queryCtx)
	err = c.ClientConnInterface.Invoke(queryCtx, method, args, reply, opts...)
	if err != nil {
		spanErr = fmt.Errorf("grpc query: service=%s, method=%s: %w", service, rpcMethod, err)
	}
	return err
}

func (c ObservedConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.ClientConnInterface.NewStream(ctx, desc, method, opts...)
}

func splitGRPCMethod(fullMethod string) (string, string) {
	trimmed := strings.TrimPrefix(fullMethod, "/")
	if trimmed == "" {
		return "unknown", "unknown"
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 {
		return trimmed, "unknown"
	}
	return parts[0], parts[1]
}

type grpcMetadataCarrier struct {
	metadata.MD
}

func (c grpcMetadataCarrier) Get(key string) string {
	values := c.MD.Get(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (c grpcMetadataCarrier) Set(key string, value string) {
	c.MD.Set(key, value)
}

func (c grpcMetadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c.MD))
	for key := range c.MD {
		keys = append(keys, key)
	}
	return keys
}

func injectGRPCTraceContext(ctx context.Context) context.Context {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		md = metadata.New(nil)
	} else {
		md = md.Copy()
	}
	otel.GetTextMapPropagator().Inject(ctx, grpcMetadataCarrier{MD: md})
	return metadata.NewOutgoingContext(ctx, md)
}
