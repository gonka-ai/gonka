package filters

import (
	"strconv"
	"strings"
)

// The phrases vLLM emits for a capability refusal, verbatim. See README.md, "Capability errors".
const (
	ToolChoiceUnsupportedMessage = "tool choice requires --enable-auto-tool-choice and --tool-call-parser to be set"
	contextLimitPhrase           = "maximum context length is "
	contextTotalPhrase           = "for a total of "
	lowerBoundQualifier          = "at least "
	inputBoundPhrase             = "which is the upper bound for "
	inputTokensPhrase            = "your request has "
	outputTooLargePhrase         = "is too large: "
	requestedPhrase              = "you requested "
)

// CapabilityLimits reads the context window and the tokens needed from a vLLM refusal, in any release's phrasing; 0 when absent.
func CapabilityLimits(message string) (contextLimit, contextRequested uint64) {
	return uintAfterPhrase(message, contextLimitPhrase), requestedTokens(message)
}

func requestedTokens(message string) uint64 {
	if total := uintAfterPhrase(message, contextTotalPhrase); total > 0 {
		return total
	}
	requested := uintAfterPhrase(message, requestedPhrase)
	if inputBound := uintAfterPhrase(message, inputBoundPhrase); inputBound > 0 {
		return inputBound + requested + 1
	}
	if inputTokens := uintAfterPhrase(message, inputTokensPhrase); inputTokens > 0 {
		return inputTokens + uintAfterPhrase(message, outputTooLargePhrase)
	}
	return requested
}

// Search and slice both run on the lowered copy: lowercasing can shorten a string, so a mixed index lands mid-word.
func uintAfterPhrase(message, phrase string) uint64 {
	lowered := strings.ToLower(message)
	_, digits, found := strings.Cut(lowered, phrase)
	if !found {
		return 0
	}
	digits = strings.TrimPrefix(digits, lowerBoundQualifier)
	end := strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(digits)
	}
	if end == 0 {
		return 0
	}
	parsed, err := strconv.ParseUint(digits[:end], 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}
