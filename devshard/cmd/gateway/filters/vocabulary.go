package filters

// The strings a cacheability answer puts into the record, one per reason a reply may not be stored.
const (
	CacheStorable          = ""
	CacheRefusedEmptyBody  = "empty_body"
	CacheRefusedFailure    = "transient_failure"
	CacheRefusedUnreadable = "unreadable_event"
	CacheRefusedUnfinished = "unfinished_answer"
	CacheRefusedStatus     = "unreplayable_status"
)

// How a host names a failure of the moment rather than of the request: in prose, and as a class in `type`
// or in a `code` that is not a status. A class is matched with its separators removed, so it lists only
// what no phrase above already spells. See README.md, "Cacheability".
var (
	momentaryFailureMessages = []string{
		"context canceled",
		"context cancelled",
		"client disconnected",
		"request canceled",
		"request cancelled",
		"timeout",
		"timed out",
		"rate limit",
		"overloaded",
		"temporarily unavailable",
		"service unavailable",
		"internal server error",
		"unsupported model",
		"model not found",
		"model_not_found",
		"does not exist",
		"not supported on this model",
	}

	momentaryFailureClasses = []string{
		"server",
		"toomanyrequests",
		"notfound",
		"connection",
		"quota",
		"cancel",
		"disconnect",
	}
)

// InputTokensSource names where the gateway's count of a request's prompt tokens came from. See docs/request.md, "The prompt token count".
type InputTokensSource string

const (
	InputTokensTokenizerExact   InputTokensSource = "tokenizer_exact"
	InputTokensTokenizerSampled InputTokensSource = "tokenizer_sampled"
	InputTokensBytesEstimate    InputTokensSource = "bytes_estimate"
)

// Tokenized reports whether the count came from the model's tokenizer; any other source, the unset one included, is the byte estimate.
func (s InputTokensSource) Tokenized() bool {
	return s == InputTokensTokenizerExact || s == InputTokensTokenizerSampled
}

// PresetName names a built-in filter preset an operator binds a model to. See README.md, "Model profiles".
type PresetName string

const (
	PresetDefault PresetName = "default"
	presetUnnamed PresetName = ""
)

type thinkingWire string

const (
	thinkingWireNormalize      thinkingWire = "normalize"
	thinkingWireMirrorToKwargs thinkingWire = "mirror_to_kwargs"
	thinkingWireStrip          thinkingWire = "strip"
	thinkingWireForceOn        thinkingWire = "force_on"
	thinkingWireUnknown        thinkingWire = ""
)

// ReasoningEffortMode is whether a model's requests carry reasoning_effort on to the host.
type ReasoningEffortMode string

const (
	ReasoningEffortForward ReasoningEffortMode = "forward"
	ReasoningEffortStrip   ReasoningEffortMode = "strip"

	reasoningEffortModeUnset ReasoningEffortMode = ""
)

// ToolsPolicy is whether a model's chat template and parser take tools at all.
type ToolsPolicy string

const (
	ToolsAllow  ToolsPolicy = "allow"
	ToolsReject ToolsPolicy = "reject"
)

// ParallelToolCallsPolicy is whether parallel_tool_calls reaches the host.
type ParallelToolCallsPolicy string

const (
	ParallelToolCallsKeep  ParallelToolCallsPolicy = "keep"
	ParallelToolCallsStrip ParallelToolCallsPolicy = "strip"
)

var jsonStringStart = []byte(`"`)
