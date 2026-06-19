package observability

// tracerID and spanID are typed strings to keep the call-site API self-
// documenting and to prevent accidental string-to-string mix-ups (e.g. passing
// a span name where a tracer name is expected).
type (
	tracerID string
	spanID   string
)

// tracerNames holds the OTel tracer name namespaces used across this process.
// One namespace per logical sub-system makes Jaeger filtering easier.
type tracerNames struct {
	Public        tracerID
	EventListener tracerID
	Validation    tracerID
	Chain         tracerID
}

// inferenceSpanNames are span identifiers for the inference request flow:
// transfer-agent reception, executor execution, ML node call, finish
// submission, and validation.
type inferenceSpanNames struct {
	Request                spanID
	Transfer               spanID
	ForwardExecutor        spanID
	Execute                spanID
	FinishSubmit           spanID
	ValidationEvent        spanID
	StatusUpdateEvent      spanID
	ValidationSample       spanID
	ValidationExecute      spanID
	PayloadRetrieve        spanID
	PayloadRetrieveAttempt spanID
	PayloadFetch           spanID
	CompareLogits          spanID
}

// mlNodeSpanNames identifies HTTP calls to the ML node (vLLM-compatible).
type mlNodeSpanNames struct {
	ChatCompletions           spanID
	ChatCompletionsValidation spanID
}

// chainSpanNames identifies blockchain interactions: tx broadcast, ABCI store
// queries, and gRPC queries.
type chainSpanNames struct {
	TxBroadcast spanID
	StoreQuery  spanID
	GRPCQuery   spanID
}

type spanNames struct {
	Inference inferenceSpanNames
	MLNode    mlNodeSpanNames
	Chain     chainSpanNames
}

var tracerName = tracerNames{
	Public:        "decentralized-api.public",
	EventListener: "decentralized-api.event-listener",
	Validation:    "decentralized-api.validation",
	Chain:         "decentralized-api.chain",
}

var spanName = spanNames{
	Inference: inferenceSpanNames{
		Request:                "inference.request",
		Transfer:               "inference.transfer",
		ForwardExecutor:        "inference.transfer.forward_executor",
		Execute:                "inference.executor.execute",
		FinishSubmit:           "inference.finish.submit",
		ValidationEvent:        "inference.validation.event",
		StatusUpdateEvent:      "inference.status_update.event",
		ValidationSample:       "inference.validation.sample",
		ValidationExecute:      "inference.validation.execute",
		PayloadRetrieve:        "inference.payload.retrieve",
		PayloadRetrieveAttempt: "inference.payload.retrieve.attempt",
		PayloadFetch:           "inference.payload.fetch",
		CompareLogits:          "inference.validation.compare_logits",
	},
	MLNode: mlNodeSpanNames{
		ChatCompletions:           "mlnode.chat.completions",
		ChatCompletionsValidation: "mlnode.chat.completions.validation",
	},
	Chain: chainSpanNames{
		TxBroadcast: "chain.tx.broadcast",
		StoreQuery:  "chain.store.query",
		GRPCQuery:   "chain.grpc.query",
	},
}
