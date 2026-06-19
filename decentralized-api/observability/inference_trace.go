package observability

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// InferenceTracer is the inference-flow tracer. Methods come in two flavours:
//
//   - StartXxx returns (ctx, *Operation) and starts a new span. Defer the
//     returned op.Finish/op.FinishErr to close it.
//   - SetXxx / AddXxx mutate an in-flight Operation, attaching attributes or
//     emitting span events.
//
// All methods are nil-receiver-safe.
type InferenceTracer struct{}

// ExtractRequestContext extracts trace context from HTTP request headers.
func (*InferenceTracer) ExtractRequestContext(ctx context.Context, headers http.Header) context.Context {
	return ExtractRequestContext(ctx, headers)
}

// InjectRequestContext writes trace context into outgoing HTTP headers.
func (*InferenceTracer) InjectRequestContext(ctx context.Context, headers http.Header) {
	InjectRequestContext(ctx, headers)
}

// StartRequest opens the root server-side span for an inference HTTP request.
func (*InferenceTracer) StartRequest(ctx context.Context, method string) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Public,
		spanName.Inference.Request,
		trace.SpanKindServer,
		[]attribute.KeyValue{
			attribute.String("http.method", method),
			attribute.String("http.route", "/v1/chat/completions"),
		},
		nil,
	)
}

// SetRequestIdentity tags the request span with the model and requester.
func (*InferenceTracer) SetRequestIdentity(op *Operation, model, requester string) {
	op.SetAttributes(
		attribute.String("model", model),
		attribute.String("requester.address", requester),
	)
}

// SetTransferAddress tags the active span with the transfer agent address.
func (*InferenceTracer) SetTransferAddress(op *Operation, transferAddress string) {
	if transferAddress == "" {
		return
	}
	op.SetAttributes(attribute.String("transfer.address", transferAddress))
}

// MarkTransferPath labels the request span as taking the transfer-agent code path.
func (*InferenceTracer) MarkTransferPath(op *Operation) {
	op.SetAttributes(attribute.String("request.path", "transfer"))
}

// MarkExecutorPath labels the request span as taking the executor code path
// and records the inference id assigned by the transfer agent.
func (*InferenceTracer) MarkExecutorPath(op *Operation, inferenceID string) {
	op.SetAttributes(
		attribute.String("request.path", "executor"),
		attribute.String("inference.id", inferenceID),
	)
}

// StartTransfer opens the span that covers transfer-agent processing.
func (*InferenceTracer) StartTransfer(ctx context.Context, model, requester string) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Public,
		spanName.Inference.Transfer,
		trace.SpanKindInternal,
		[]attribute.KeyValue{
			attribute.String("model", model),
			attribute.String("requester.address", requester),
		},
		modelMetric(model),
	)
}

// StartForwardExecutor opens the client-side span that wraps the transfer
// agent's HTTP call to a chosen executor.
func (*InferenceTracer) StartForwardExecutor(ctx context.Context, model, executorAddress, executorURL string) (context.Context, *Operation) {
	attrs := []attribute.KeyValue{
		attribute.String("executor.address", executorAddress),
		attribute.String("model", model),
	}
	attrs = appendStr(attrs, "executor.url", executorURL)
	return StartOperation(
		ctx,
		tracerName.Public,
		spanName.Inference.ForwardExecutor,
		trace.SpanKindClient,
		attrs,
		modelMetric(model),
	)
}

// StartExecutor opens the span covering executor-side processing.
func (*InferenceTracer) StartExecutor(ctx context.Context, inferenceID, model, requester, transferAddress string) (context.Context, *Operation) {
	attrs := []attribute.KeyValue{
		attribute.String("inference.id", inferenceID),
		attribute.String("model", model),
		attribute.String("requester.address", requester),
	}
	attrs = appendStr(attrs, "transfer.address", transferAddress)
	return StartOperation(
		ctx,
		tracerName.Public,
		spanName.Inference.Execute,
		trace.SpanKindInternal,
		attrs,
		modelMetric(model),
	)
}

// StartMLNodeExecution opens the client-side span around the ML node call.
func (*InferenceTracer) StartMLNodeExecution(ctx context.Context, inferenceID, model string) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Public,
		spanName.MLNode.ChatCompletions,
		trace.SpanKindClient,
		[]attribute.KeyValue{
			attribute.String("inference.id", inferenceID),
			attribute.String("model", model),
		},
		modelMetric(model),
	)
}

// SetMLNodeTarget records which ML node served the request.
func (*InferenceTracer) SetMLNodeTarget(op *Operation, nodeID, nodeURL string) {
	attrs := []attribute.KeyValue{}
	attrs = appendStr(attrs, "mlnode.node.id", nodeID)
	attrs = appendStr(attrs, "mlnode.url", nodeURL)
	op.SetAttributes(attrs...)
}

// StartFinishSubmission opens the span around publishing the FinishInference tx.
func (*InferenceTracer) StartFinishSubmission(ctx context.Context, inferenceID, executorAddress, model string) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Public,
		spanName.Inference.FinishSubmit,
		trace.SpanKindInternal,
		[]attribute.KeyValue{
			attribute.String("inference.id", inferenceID),
			attribute.String("executor.address", executorAddress),
			attribute.String("model", model),
		},
		modelMetric(model),
	)
}

// SetModel attaches/overwrites the model attribute on the span.
func (*InferenceTracer) SetModel(op *Operation, model string) {
	if model == "" {
		return
	}
	op.SetAttributes(attribute.String("model", model))
}

// SetResponseHash records the response hash used for validation.
func (*InferenceTracer) SetResponseHash(op *Operation, hash string) {
	if hash == "" {
		return
	}
	op.SetAttributes(attribute.String("response.hash", hash))
}

// SetHTTPStatus records the HTTP response status the handler returned.
func (*InferenceTracer) SetHTTPStatus(op *Operation, statusCode int) {
	op.SetAttributes(attribute.Int("http.status_code", statusCode))
}

// --- Validation flow spans -------------------------------------------------

// StartValidationEvent opens the span around handling the InferenceFinished
// chain event for a batch of inferences.
func (*InferenceTracer) StartValidationEvent(ctx context.Context, inferenceCount int) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.EventListener,
		spanName.Inference.ValidationEvent,
		trace.SpanKindConsumer,
		[]attribute.KeyValue{attribute.Int("inference.count", inferenceCount)},
		nil,
	)
}

// StartStatusUpdateEvent opens the span around handling chain status updates.
func (*InferenceTracer) StartStatusUpdateEvent(ctx context.Context, inferenceCount int) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.EventListener,
		spanName.Inference.StatusUpdateEvent,
		trace.SpanKindConsumer,
		[]attribute.KeyValue{attribute.Int("inference.count", inferenceCount)},
		nil,
	)
}

// StartValidationSample opens the span that picks which inferences this
// validator should re-execute.
func (*InferenceTracer) StartValidationSample(ctx context.Context, candidateCount int) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Validation,
		spanName.Inference.ValidationSample,
		trace.SpanKindInternal,
		[]attribute.KeyValue{attribute.Int("candidate.count", candidateCount)},
		nil,
	)
}

// SetSampledCount tags how many inferences were sampled for re-validation.
func (*InferenceTracer) SetSampledCount(op *Operation, sampled int) {
	op.SetAttributes(attribute.Int("sampled.count", sampled))
}

// AddValidationSampleDecision emits one event per sampling decision so
// operators can audit why a particular inference was (or wasn't) re-validated.
func (*InferenceTracer) AddValidationSampleDecision(
	op *Operation,
	inferenceID, model, executorAddress, validatorAddress string,
	shouldValidate bool,
	reason string,
	seed int64,
	validatorPower, executorPower, totalPower uint64,
) {
	op.AddEvent(
		"validation.sample.decision",
		attribute.String("inference.id", inferenceID),
		attribute.String("model", model),
		attribute.String("executor.address", executorAddress),
		attribute.String("validator.address", validatorAddress),
		attribute.Bool("should_validate", shouldValidate),
		attribute.String("decision.reason", reason),
		attribute.Int64("validation.seed", seed),
		attribute.Int64("validator.power", int64(validatorPower)),
		attribute.Int64("executor.power", int64(executorPower)),
		attribute.Int64("total.power", int64(totalPower)),
	)
}

// SetValidationSampleDecisionStats records aggregate sampling counts.
func (*InferenceTracer) SetValidationSampleDecisionStats(op *Operation, total, selected, skipped int) {
	op.SetAttributes(
		attribute.Int("validation.decisions.total", total),
		attribute.Int("validation.decisions.true", selected),
		attribute.Int("validation.decisions.false", skipped),
	)
}

// StartValidationExecution opens the span that re-executes one inference for
// validation. revalidation indicates a retry of an earlier validation.
func (*InferenceTracer) StartValidationExecution(
	ctx context.Context, inferenceID, model string, epochID int64, revalidation bool,
) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Validation,
		spanName.Inference.ValidationExecute,
		trace.SpanKindInternal,
		[]attribute.KeyValue{
			attribute.String("inference.id", inferenceID),
			attribute.String("model", model),
			attribute.Int64("epoch.id", epochID),
			attribute.Bool("revalidation", revalidation),
		},
		modelMetric(model),
	)
}

// StartPayloadRetrieval opens the umbrella span for fetching a stored payload.
func (*InferenceTracer) StartPayloadRetrieval(ctx context.Context, inferenceID, executorAddress string, epochID int64) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Validation,
		spanName.Inference.PayloadRetrieve,
		trace.SpanKindInternal,
		[]attribute.KeyValue{
			attribute.String("inference.id", inferenceID),
			attribute.String("executor.address", executorAddress),
			attribute.Int64("epoch.id", epochID),
		},
		nil,
	)
}

// StartPayloadRetrievalAttempt opens a span around a single retrieval attempt.
func (*InferenceTracer) StartPayloadRetrievalAttempt(ctx context.Context, inferenceID, executorAddress string, epochID int64, attempt int) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Validation,
		spanName.Inference.PayloadRetrieveAttempt,
		trace.SpanKindInternal,
		[]attribute.KeyValue{
			attribute.String("inference.id", inferenceID),
			attribute.String("executor.address", executorAddress),
			attribute.Int64("epoch.id", epochID),
			attribute.Int("payload.attempt", attempt),
		},
		nil,
	)
}

// StartPayloadFetch opens the client-side HTTP span that pulls the payload.
func (*InferenceTracer) StartPayloadFetch(ctx context.Context, requestURL, validatorAddress string, epochID int64) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Validation,
		spanName.Inference.PayloadFetch,
		trace.SpanKindClient,
		[]attribute.KeyValue{
			attribute.String("executor.url", requestURL),
			attribute.String("validator.address", validatorAddress),
			attribute.Int64("epoch.id", epochID),
		},
		nil,
	)
}

// StartValidationMLNode opens the client-side span for re-running an inference
// against the validator's ML node.
func (*InferenceTracer) StartValidationMLNode(ctx context.Context, inferenceID, model, nodeID string) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Validation,
		spanName.MLNode.ChatCompletionsValidation,
		trace.SpanKindClient,
		[]attribute.KeyValue{
			attribute.String("inference.id", inferenceID),
			attribute.String("model", model),
			attribute.String("mlnode.node.id", nodeID),
		},
		modelMetric(model),
	)
}

// StartCompareLogits opens the span around comparing logits of executor and
// validator outputs.
func (*InferenceTracer) StartCompareLogits(ctx context.Context, inferenceID string) (context.Context, *Operation) {
	return StartOperation(
		ctx,
		tracerName.Validation,
		spanName.Inference.CompareLogits,
		trace.SpanKindInternal,
		[]attribute.KeyValue{attribute.String("inference.id", inferenceID)},
		nil,
	)
}

// SetSimilarity attaches the computed logit similarity value.
func (*InferenceTracer) SetSimilarity(op *Operation, similarity float64) {
	op.SetAttributes(attribute.Float64("validation.similarity", similarity))
}
