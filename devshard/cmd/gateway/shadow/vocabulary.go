package shadow

import "devshard/cmd/gateway/config"

// How a probe ended; the wire strings of the metric and the journal line.
const (
	OutcomeServed = "served"
	OutcomeFailed = "failed"
)

// The probe asks for a random number of output tokens in [ProbeMinOutputTokens, min(ProbeMaxOutputTokens, the model's output cap, the context room)], drawn per probe; the rest is how it is built, bounded and read, and the members of a chat request body it rewrites. See README.md.
const (
	ProbeMinOutputTokens = config.ProbeMinOutputTokens
	ProbeMaxOutputTokens = 1024
	maxCaptureBytes      = 64 << 10
	dataPrefix           = "data:"
	doneMarker           = "[DONE]"

	fieldMaxTokens           = "max_tokens"
	fieldMaxCompletionTokens = "max_completion_tokens"
	fieldMinTokens           = "min_tokens"
	fieldTools               = "tools"
	fieldStream              = "stream"
	fieldStreamOptions       = "stream_options"
	fieldIncludeUsage        = "include_usage"
)
