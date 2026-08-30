package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Transports name the paths a chain query can take. They label spans and the
// active-transport gauge, so a service running degraded is visible rather than
// merely slower.
const (
	TransportGRPC     = "grpc"
	TransportCometRPC = "comet-rpc"
)

const transportAttr = "chain.transport"

// ChainTracer instruments Cosmos chain queries. Mirrors decentralized-api
// observability.Chain for ABCI store and gRPC query spans.
type ChainTracer struct{}

// Chain is the process-wide chain tracer shorthand.
var Chain ChainTracer

// chainTransportActive carries 1 for the transport queries currently run on and
// 0 for the others, so an alert on the comet-rpc series firing for longer than a
// blip catches a fallback nobody noticed. The global meter delegates to whatever
// provider the process installs and stays a no-op when it installs none.
var chainTransportActive = newTransportGauge()

func newTransportGauge() metric.Int64Gauge {
	gauge, err := otel.Meter(string(tracerName.Chain)).Int64Gauge(
		"chain.query.transport.active",
		metric.WithDescription("1 for the transport chain queries currently run on, 0 for the others"),
	)
	if err != nil {
		return nil
	}
	return gauge
}

// SetQueryTransport records which transport chain queries are being served by.
// Callers report every transition, including the initial one, so the gauge is
// never absent for a live client.
func (*ChainTracer) SetQueryTransport(active string) {
	if chainTransportActive == nil {
		return
	}
	for _, transport := range []string{TransportGRPC, TransportCometRPC} {
		value := int64(0)
		if transport == active {
			value = 1
		}
		chainTransportActive.Record(context.Background(), value,
			metric.WithAttributes(attribute.String(transportAttr, transport)))
	}
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
	)
}

// StartGRPCQuery opens the span around a Cosmos gRPC query. transport names the
// path actually carrying it, which is not always direct gRPC.
func (*ChainTracer) StartGRPCQuery(ctx context.Context, service, method, transport string) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Chain,
		spanName.Chain.GRPCQuery,
		trace.SpanKindClient,
		[]attribute.KeyValue{
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", service),
			attribute.String("rpc.method", method),
			attribute.String(transportAttr, transport),
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
