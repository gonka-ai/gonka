package observability

type (
	tracerID string
	spanID   string
)

type tracerNames struct {
	Server tracerID
	Host   tracerID
}

type spanNames struct {
	// HTTP server-side span around an inbound devshardd request.
	Request spanID
	// Inference handler span (HandleInference).
	Inference spanID
	// ML node call (vLLM /v1/chat/completions).
	MLNode spanID
	// Validation re-execution span on the validator side.
	Validation spanID
}

var tracerName = tracerNames{
	Server: "devshardd.server",
	Host:   "devshardd.host",
}

var spanName = spanNames{
	Request:    "devshardd.request",
	Inference:  "devshardd.inference",
	MLNode:     "devshardd.mlnode.chat.completions",
	Validation: "devshardd.validation",
}
