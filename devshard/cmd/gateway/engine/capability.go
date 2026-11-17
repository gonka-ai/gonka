package engine

import (
	"net/http"
	"regexp"
	"strings"

	"devshard/cmd/gateway/filters"
	"devshard/storage"
)

var versionNotFound = regexp.MustCompile(`version "[^"]*" not found`)

const contextEstimateMargin = 2

type CapabilitySignal struct {
	ToolsUnsupported   bool
	VersionUnsupported bool
	ContextLimit       uint64
	ContextRequested   uint64
}

// Retriable reports a refusal another host may not repeat: a tool call or protocol version the answering host's build lacks.
func (s CapabilitySignal) Retriable() bool {
	return s.ToolsUnsupported || s.VersionUnsupported
}

// ServableByALongerHost reports a context-length refusal from a host running shorter than the model, for a request the model's full length would take.
func (s CapabilitySignal) ServableByALongerHost(modelContextLength uint64) bool {
	return s.ContextLimit > 0 && s.ContextLimit < modelContextLength && s.ContextRequested <= modelContextLength
}

// servableElsewhere reports a context-length refusal another host may still serve: a shorter host's, or a claim the gateway's own estimate contradicts.
func (s CapabilitySignal) servableElsewhere(modelContextLength, estimatedTokens uint64) bool {
	return s.ServableByALongerHost(modelContextLength) || s.contradictedByEstimate(modelContextLength, estimatedTokens)
}

func (s CapabilitySignal) contradictedByEstimate(modelContextLength, estimatedTokens uint64) bool {
	return s.ContextLimit > 0 && estimatedTokens != noTokenEstimate && estimatedTokens*contextEstimateMargin <= modelContextLength
}

// Refused reports any refusal the gateway recognises, retriable or not.
func (s CapabilitySignal) Refused() bool {
	return s.Retriable() || s.ContextLimit > 0
}

func ParseVersionRefusal(body string) CapabilitySignal {
	if !versionNotFound.MatchString(body) && !strings.Contains(body, storage.ErrSessionVersionConflict.Error()) {
		return CapabilitySignal{}
	}
	return CapabilitySignal{VersionUnsupported: true}
}

func ParseCapabilityError(message string) CapabilitySignal {
	if strings.Contains(message, filters.ToolChoiceUnsupportedMessage) {
		return CapabilitySignal{ToolsUnsupported: true}
	}
	contextLimit, contextRequested := filters.CapabilityLimits(message)
	if contextLimit == 0 {
		return CapabilitySignal{}
	}
	return CapabilitySignal{ContextLimit: contextLimit, ContextRequested: contextRequested}
}

func capabilityOfDispatchError(err error) CapabilitySignal {
	status, refused := hostRefusal(err)
	if !refused {
		return CapabilitySignal{}
	}
	return ParseVersionRefusal(status.Body)
}

func CapabilityOf(attempt AttemptOutcome) CapabilitySignal {
	if attempt.Capability.Refused() {
		return attempt.Capability
	}
	if attempt.ErrorSource == "" || attempt.ErrorMessage == "" {
		return CapabilitySignal{}
	}
	return ParseCapabilityError(attempt.ErrorMessage)
}

// rulesOutRetry reports a trusted answer no other host would change: a context-length refusal a longer host could still serve keeps the search going. See race.md, "Escalation".
func rulesOutRetry(attempt AttemptOutcome, modelContextLength, estimatedTokens uint64) bool {
	return answersForEveryHost(attempt) && !CapabilityOf(attempt).servableElsewhere(modelContextLength, estimatedTokens)
}

// answersForEveryHost reports a trusted host's answer the race takes as every host's: a refusal that is not Retriable, or a rejection of the request itself.
func answersForEveryHost(attempt AttemptOutcome) bool {
	if attempt.Suspicious {
		return false
	}
	refusal := CapabilityOf(attempt)
	return (refusal.Refused() && !refusal.Retriable()) || rejectsRequest(attempt)
}

// rejectsRequest reports an error event from an attempt that streamed no content, with a structured 400 that filters reads as about the request, not the host. See ../filters/README.md, "Cacheability".
func rejectsRequest(attempt AttemptOutcome) bool {
	if attempt.ErrorSource == "" || attempt.ContentSource != "" {
		return false
	}
	named := HostApplicationError{Code: attempt.ErrorCode, Type: attempt.ErrorType}
	return named.HTTPStatus() == http.StatusBadRequest &&
		filters.IsCacheableUpstreamError(http.StatusBadRequest, []byte(attempt.ErrorPayload))
}

type CapabilityRecorder interface {
	RecordContextLimit(participant, model string, maxTokens uint64)
	RecordToolUnsupported(participant, model string)
	RecordVersionUnsupported(participant string)
}

func RecordCapability(recorder CapabilityRecorder, participant, model string, signal CapabilitySignal) {
	if recorder == nil || participant == "" {
		return
	}
	switch {
	case signal.ToolsUnsupported:
		recorder.RecordToolUnsupported(participant, model)
	case signal.VersionUnsupported:
		recorder.RecordVersionUnsupported(participant)
	case signal.ContextLimit > 0:
		recorder.RecordContextLimit(participant, model, signal.ContextLimit)
	}
}
