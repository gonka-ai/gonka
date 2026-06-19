package observability

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ChainTracer instruments interactions with the Cosmos chain (tx broadcast,
// confirmation, ABCI store queries, gRPC queries). Methods are nil-safe.
type ChainTracer struct{}

// StartTxBroadcast opens the span around publishing a transaction. batchSize
// is recorded only when positive.
func (*ChainTracer) StartTxBroadcast(ctx context.Context, msgType string, batchSize int) (context.Context, *Operation) {
	attrs := []attribute.KeyValue{
		attribute.String("blockchain.system", "cosmos"),
		attribute.String("tx.msg_type", msgType),
	}
	if batchSize > 0 {
		attrs = append(attrs, attribute.Int("tx.batch_size", batchSize))
	}
	return StartOperation(ctx, tracerName.Chain, spanName.Chain.TxBroadcast, trace.SpanKindClient, attrs, nil)
}

// SetTxResult attaches the broadcast result code (and hash if provided).
func (*ChainTracer) SetTxResult(op *Operation, txHash string, code uint32) {
	attrs := []attribute.KeyValue{attribute.Int64("tx.result_code", int64(code))}
	if txHash != "" {
		attrs = append(attrs, attribute.String("tx.hash", txHash))
	}
	op.SetAttributes(attrs...)
}

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
		nil,
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
		nil,
	)
}

// SetRPCStatus attaches a gRPC status code.
func (*ChainTracer) SetRPCStatus(op *Operation, code string) {
	if code == "" {
		return
	}
	op.SetAttributes(attribute.String("rpc.grpc.status_code", code))
}
