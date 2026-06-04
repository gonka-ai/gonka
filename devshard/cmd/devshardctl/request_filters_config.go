package main

// Body / request-level caps.
const (
	MaxChatRequestBodySize       = 10 * 1024 * 1024
	MaxLoggedResponseFormatBytes = 2048 * 1024
	MaxChatRequestChoices        = 5
	MaxTemperature               = 2.0
	MaxRepetitionPenalty         = 2.0
)

// MaxRequestNestingDepth bounds JSON nesting before we hand the bytes to encoding/json.
// encoding/json allocates O(input size) per recursion level, so a 7 KiB body nested 200
// levels deep blows up to ~180 KiB of map[string]any wrappers. The whitelist rules then
// reject in nanoseconds, but the decoder has already paid the cost. The pre-scan defuses
// that amplification cheaply.
//
// 32 leaves at least 3x headroom over the deepest legitimate request shape we forward:
// `tools[].function.parameters` or `response_format.json_schema.schema` nested under their
// wrappers (~9-10 levels) plus a small allowance for client-side structuring.
const MaxRequestNestingDepth = 32

// Per-parameter bounds wired into the catalog. Values match supported-params.md.
const (
	MessagesMaxEntries = 2048

	LogitBiasMinValue   = -100
	LogitBiasMaxValue   = 100
	LogitBiasMaxEntries = 1024

	StopMaxEntries  = 16
	StopMaxEntryLen = 256

	StopTokenIdsMaxEntries = 64

	BadWordsMaxEntries  = 64
	BadWordsMaxEntryLen = 128

	PenaltyMin               = -2.0
	PenaltyMax               = 2.0
	KimiK2PenaltyForcedValue = 0.0

	TopLogprobsForcedValue = 5

	ChatTemplateKwargsMaxDepth = 16
	ChatTemplateKwargsMaxSize  = 16 * 1024
	ChatTemplateKwargsMaxNodes = 128

	ToolsMaxDepth      = 16
	ToolsMaxSize       = 16 * 1024
	ToolsMaxNodes      = 256
	ToolsMaxBranch     = 16
	ToolsMaxEnum       = 256
	ToolsMaxPatternLen = 512

	ToolChoiceMaxNameLen = 64

	ResponseFormatMaxDepth      = 16
	ResponseFormatMaxSize       = 16 * 1024
	ResponseFormatMaxNodes      = 128
	ResponseFormatMaxBranch     = 16
	ResponseFormatMaxEnum       = 256
	ResponseFormatMaxNameLen    = 64
	ResponseFormatMaxPatternLen = 512

	UserMaxLen             = 512
	SafetyIdentifierMaxLen = 512
	
	StructuredOutputsMaxDepth            = 16
	StructuredOutputsMaxSize             = 16 * 1024
	StructuredOutputsMaxNodes            = 128
	StructuredOutputsMaxBranch           = 16
	StructuredOutputsMaxEnum             = 256
	StructuredOutputsMaxPatternLen       = 512
	StructuredOutputsMaxChoiceEntries    = 256
	StructuredOutputsMaxChoiceEntryLen   = 1024
	StructuredOutputsMaxGrammarLen       = 8 * 1024
	StructuredOutputsMaxGrammarNesting   = 200
	StructuredOutputsMaxStructuralTagLen = 4 * 1024

	kimiThinkingTokenBudgetDefaultDivisor uint64 = 2
	kimiThinkingTokenBudgetMax            uint64 = 96_000

	// Below this floor Kimi-K2.6 emits only </think> (special token vLLM drops from content).
	kimiMaxTokensMin uint64 = 16
)

// Routed model identifiers. The catalog wires per-model behavior keyed on these strings.
const kimiK26ModelID = "moonshotai/Kimi-K2.6"

// Sentinel content used by message normalization when an upstream tool result is empty.
const emptyToolResultContent = "<empty tool result>"
