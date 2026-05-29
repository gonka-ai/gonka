package observability

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Service is the public façade. Use Default in code that doesn't have DI.
type Service struct {
	request *RequestTracer
}

// Default is the process-wide Service instance.
var Default = NewService()

// Request is a shorthand for Default.Request().
var Request = Default.Request()

// NewService constructs a fresh Service.
func NewService() *Service {
	return &Service{request: &RequestTracer{}}
}

// Request returns the request tracer.
func (s *Service) Request() *RequestTracer {
	if s == nil {
		return (&Service{request: &RequestTracer{}}).request
	}
	return s.request
}

// RequestTracer instruments inbound HTTP and downstream ML calls. Methods are
// nil-receiver-safe.
type RequestTracer struct{}

// StartRequest opens the server-side span around an inbound HTTP request.
// route is the parametrised route (e.g. "/sessions/:id/inference").
func (*RequestTracer) StartRequest(ctx context.Context, method, route string) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Server,
		spanName.Request,
		trace.SpanKindServer,
		[]attribute.KeyValue{
			attribute.String("http.method", method),
			attribute.String("http.route", route),
		},
		nil,
	)
}

// StartInference opens the span around HandleInference processing.
func (*RequestTracer) StartInference(ctx context.Context, sessionID, model string) (context.Context, *Operation) {
	attrs := []attribute.KeyValue{}
	if sessionID != "" {
		attrs = append(attrs, attribute.String("session.id", sessionID))
	}
	if model != "" {
		attrs = append(attrs, attribute.String("model", model))
	}
	return StartOperation(
		ctx,
		tracerName.Host,
		spanName.Inference,
		trace.SpanKindInternal,
		attrs,
		modelMetric(model),
	)
}

// StartMLNodeCall opens the client-side span around the ML node HTTP call.
func (*RequestTracer) StartMLNodeCall(ctx context.Context, model, nodeURL string) (context.Context, *Operation) {
	attrs := []attribute.KeyValue{}
	if model != "" {
		attrs = append(attrs, attribute.String("model", model))
	}
	if nodeURL != "" {
		attrs = append(attrs, attribute.String("mlnode.url", nodeURL))
	}
	return StartOperation(
		ctx,
		tracerName.Host,
		spanName.MLNode,
		trace.SpanKindClient,
		attrs,
		modelMetric(model),
	)
}

// StartValidation opens the span around validation re-execution.
func (*RequestTracer) StartValidation(ctx context.Context, inferenceID, model string) (context.Context, *Operation) {
	attrs := []attribute.KeyValue{}
	if inferenceID != "" {
		attrs = append(attrs, attribute.String("inference.id", inferenceID))
	}
	if model != "" {
		attrs = append(attrs, attribute.String("model", model))
	}
	return StartOperation(
		ctx,
		tracerName.Host,
		spanName.Validation,
		trace.SpanKindInternal,
		attrs,
		modelMetric(model),
	)
}

// SetHTTPStatus tags the span with the HTTP response status.
func (*RequestTracer) SetHTTPStatus(op *Operation, status int) {
	op.SetAttributes(attribute.Int("http.status_code", status))
}

// StartHandler opens a generic internal span around handler logic. Use this
// for transport handlers without a dedicated span constructor.
func (*RequestTracer) StartHandler(ctx context.Context, handlerName, sessionID string) (context.Context, *Operation) {
	attrs := []attribute.KeyValue{
		attribute.String("devshard.handler", handlerName),
	}
	if sessionID != "" {
		attrs = append(attrs, attribute.String("session.id", sessionID))
	}
	return StartOperation(
		ctx,
		tracerName.Server,
		spanID("devshardd.handler."+handlerName),
		trace.SpanKindInternal,
		attrs,
		nil,
	)
}

// SetHTTPRequest tags the active span with inbound HTTP request metadata.
// Safe to call with empty values; missing fields are skipped.
func (*RequestTracer) SetHTTPRequest(op *Operation, method, route, target, peerAddr string, contentLength int64) {
	attrs := make([]attribute.KeyValue, 0, 5)
	if method != "" {
		attrs = append(attrs, attribute.String("http.method", method))
	}
	if route != "" {
		attrs = append(attrs, attribute.String("http.route", route))
	}
	if target != "" {
		attrs = append(attrs, attribute.String("http.target", target))
	}
	if peerAddr != "" {
		attrs = append(attrs, attribute.String("peer.address", peerAddr))
	}
	if contentLength >= 0 {
		attrs = append(attrs, attribute.Int64("http.request_content_length", contentLength))
	}
	op.SetAttributes(attrs...)
}

// SetSender tags the span with the request sender address.
func (*RequestTracer) SetSender(op *Operation, sender string) {
	if sender == "" {
		return
	}
	op.SetAttributes(attribute.String("sender", sender))
}

// SetEscrowID tags the span with the active escrow id.
func (*RequestTracer) SetEscrowID(op *Operation, escrowID string) {
	if escrowID == "" {
		return
	}
	op.SetAttributes(attribute.String("escrow.id", escrowID))
}

// SetModel tags the span with the inference model name.
func (*RequestTracer) SetModel(op *Operation, model string) {
	if model == "" {
		return
	}
	op.SetAttributes(attribute.String("model", model))
}

// SetInferenceID tags the span with the host-assigned inference id.
func (*RequestTracer) SetInferenceID(op *Operation, inferenceID uint64) {
	op.SetAttributes(attribute.Int64("inference.id", int64(inferenceID)))
}

// SetNonce tags the span with the request nonce.
func (*RequestTracer) SetNonce(op *Operation, nonce uint64) {
	op.SetAttributes(attribute.Int64("devshard.nonce", int64(nonce)))
}

// SetSlotID tags the span with the validator slot id.
func (*RequestTracer) SetSlotID(op *Operation, slotID uint32) {
	op.SetAttributes(attribute.Int("devshard.slot_id", int(slotID)))
}

// SetStateHash tags the span with the hex-encoded state hash.
func (*RequestTracer) SetStateHash(op *Operation, stateHashHex string) {
	if stateHashHex == "" {
		return
	}
	op.SetAttributes(attribute.String("devshard.state_hash", stateHashHex))
}

// SetDiffsCount tags the span with the number of diffs in the request.
func (*RequestTracer) SetDiffsCount(op *Operation, count int) {
	op.SetAttributes(attribute.Int("devshard.diffs_count", count))
}

// SetSignaturesReturned tags a get_signatures span with the response size.
func (*RequestTracer) SetSignaturesReturned(op *Operation, count int) {
	op.SetAttributes(attribute.Int("devshard.signatures_returned", count))
}

// SetDiffsRange tags a get_diffs span with the requested nonce window.
func (*RequestTracer) SetDiffsRange(op *Operation, from, to uint64) {
	op.SetAttributes(
		attribute.Int64("devshard.from", int64(from)),
		attribute.Int64("devshard.to", int64(to)),
	)
}

// SetDiffsReturned tags a get_diffs span with the response size.
func (*RequestTracer) SetDiffsReturned(op *Operation, count int) {
	op.SetAttributes(attribute.Int("devshard.diffs_returned", count))
}

// SetMempoolSize tags a get_mempool span with the number of txs returned.
func (*RequestTracer) SetMempoolSize(op *Operation, count int) {
	op.SetAttributes(attribute.Int("devshard.mempool_size", count))
}

// SetResponseContentLength tags the span with the outbound response size.
func (*RequestTracer) SetResponseContentLength(op *Operation, length int) {
	op.SetAttributes(attribute.Int("http.response_content_length", length))
}

// SetGossipTxsBytes tags a gossip_txs span with raw payload size.
func (*RequestTracer) SetGossipTxsBytes(op *Operation, length int) {
	op.SetAttributes(attribute.Int("devshard.txs_bytes", length))
}

// SetGossipTxsCount tags a gossip_txs span with the number of decoded txs.
func (*RequestTracer) SetGossipTxsCount(op *Operation, count int) {
	op.SetAttributes(attribute.Int("devshard.txs_count", count))
}

// SetInferenceBodyBytes tags the inference span with the raw request body size.
func (*RequestTracer) SetInferenceBodyBytes(op *Operation, length int) {
	op.SetAttributes(attribute.Int("http.body_bytes", length))
}

// SetInferenceResponse tags the inference span with host response metadata.
func (*RequestTracer) SetInferenceResponse(op *Operation, responseNonce uint64, executionExpected, cachedResponse bool) {
	op.SetAttributes(
		attribute.Int64("devshard.response_nonce", int64(responseNonce)),
		attribute.Bool("devshard.execution_expected", executionExpected),
		attribute.Bool("devshard.cached_response", cachedResponse),
	)
}

func modelMetric(model string) []attribute.KeyValue {
	if model == "" {
		return nil
	}
	return []attribute.KeyValue{attribute.String("model", model)}
}
