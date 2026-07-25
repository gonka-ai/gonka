package observability

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ChainTracer instruments Cosmos chain queries. Mirrors decentralized-api
// observability.Chain for ABCI store and gRPC query spans.
type ChainTracer struct{}

// Chain is the process-wide chain tracer shorthand.
var Chain ChainTracer

// StartStoreQuery opens the span around an ABCI store query.
func (*ChainTracer) StartStoreQuery(ctx context.Context, storeKey string, withProof bool, height int64) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Chain,
		spanName.Chain.StoreQuery,
		trace.SpanKindClient,
		[]attribute.KeyValue{
			attribute.String("blockchain.system", "cosmos"),
			attribute.String("store.key", storeKey),
			attribute.Bool("query.with_proof", withProof),
			attribute.Int64("query.height", height),
		},
	)
}

// StartGRPCQuery opens the span around a Cosmos gRPC query.
func (*ChainTracer) StartGRPCQuery(ctx context.Context, service, method string) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Chain,
		spanName.Chain.GRPCQuery,
		trace.SpanKindClient,
		[]attribute.KeyValue{
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", service),
			attribute.String("rpc.method", method),
		},
	)
}

// SetRPCStatus attaches a gRPC status code string to the span.
func (*ChainTracer) SetRPCStatus(op *Operation, code string) {
	if code == "" {
		return
	}
	op.SetAttributes(attribute.String("rpc.grpc.status_code", code))
}
