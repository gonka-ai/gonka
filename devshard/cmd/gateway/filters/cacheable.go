package filters

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	json "github.com/goccy/go-json"

	"devshard/cmd/gateway/internal/strictjson"
)

// UpstreamError is the OpenAI-compatible error shape extracted from a response body or SSE event.
type UpstreamError struct {
	Type    string
	Code    string
	Message string
}

// CacheRefusal names why a response may not be stored. See README.md, "Cacheability".
func CacheRefusal(status int, body []byte) string {
	if len(body) == 0 {
		return CacheRefusedEmptyBody
	}
	scan := scanResponse(body, true)
	replayableRejection := status == http.StatusBadRequest && scan.failed && isCacheableErrorDetails(scan.upstream)
	storableStatus := (status >= 200 && status < 300) || replayableRejection
	switch {
	case scan.failed && !replayableRejection:
		return CacheRefusedFailure
	case !storableStatus:
		return CacheRefusedStatus
	case scan.unreadable:
		return CacheRefusedUnreadable
	case !scan.finish.finished():
		return CacheRefusedUnfinished
	}
	return CacheStorable
}

// IsCacheableResponse reports whether a completed upstream response may be stored and replayed.
func IsCacheableResponse(status int, body []byte) bool {
	return CacheRefusal(status, body) == CacheStorable
}

// HasNonCacheableError reports a failure that must not be replayed, so a poisoned entry drops itself on read.
func HasNonCacheableError(body []byte) bool {
	details, ok := parseUpstreamErrorDetails(body)
	return ok && !isCacheableErrorDetails(details)
}

// IsCacheableUpstreamError reports a deterministic client-input error, safe to cache.
func IsCacheableUpstreamError(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	details, ok := parseUpstreamErrorDetails(body)
	if !ok {
		return false
	}
	return isCacheableErrorDetails(details)
}

// UpstreamFailure reports the error a whole response body carries. See README.md, "Cacheability".
func UpstreamFailure(body []byte) (UpstreamError, bool) {
	return parseUpstreamErrorDetails(body)
}

func parseUpstreamErrorDetails(payload []byte) (UpstreamError, bool) {
	scan := scanResponse(payload, false)
	return scan.upstream, scan.failed
}

// Read on its own, so a host cannot hide its failure behind a sibling field it typed wrongly. See README.md, "Cacheability".
type scannedError struct {
	Error     json.RawMessage `json:"error"`
	ErrorType string          `json:"error_type"`
	Object    string          `json:"object"`
	Type      string          `json:"type"`
	Code      any             `json:"code"`
	Message   string          `json:"message"`
}

// The reasons stay raw: a wrong type in one would otherwise fail the decode that also finds the error.
type scannedEvent struct {
	scannedError
	Choices []scannedChoice `json:"choices"`

	choicesUnreadable bool
}

type scannedChoice struct {
	Index        any             `json:"index"`
	FinishReason json.RawMessage `json:"finish_reason"`
	StopReason   json.RawMessage `json:"stop_reason"`
}

type responseScan struct {
	upstream   UpstreamError
	failed     bool
	unreadable bool
	finish     answerFinish
}

// Events, not `data:` lines: a client joins the lines of one event, so an object split across two of them
// must reach the decoder whole. judgeAnswer false skips the choices, for a caller that needs only the failure.
func scanResponse(body []byte, judgeAnswer bool) responseScan {
	var scan responseScan
	if event, ok := decodeScannedEvent(body, judgeAnswer); ok {
		// Nothing later can carry the error, so an empty one is still the answer this body gave.
		if details, held := event.upstreamError(); held {
			scan.upstream, scan.failed = details, true
		}
		scan.noteChoices(event)
		return scan
	}
	framed := false
	forEachSSEEvent(body, func(raw []byte) bool {
		_, payload, held := eventPayload(raw)
		payload = bytes.TrimSpace(payload)
		if !held {
			return false
		}
		framed = true
		if len(payload) == 0 || bytes.Equal(payload, sseDoneMarker) {
			return false
		}
		event, ok := decodeScannedEvent(payload, judgeAnswer)
		if !ok {
			scan.unreadable = true
			return false
		}
		scan.note(event)
		return false
	})
	// An event or a body nobody can read says nothing about what the reply carried, so nothing replays it.
	scan.unreadable = scan.unreadable || !framed
	return scan
}

// note keeps the first failure: an empty {"error":{}} carries none, so it must not stop the walk.
func (scan *responseScan) note(event scannedEvent) {
	if !scan.failed {
		if details, held := event.upstreamError(); held && details != (UpstreamError{}) {
			scan.upstream, scan.failed = details, true
		}
	}
	scan.noteChoices(event)
}

func (scan *responseScan) noteChoices(event scannedEvent) {
	if event.choicesUnreadable {
		scan.unreadable = true
		return
	}
	scan.finish.note(event.Choices)
}

func decodeScannedEvent(payload []byte, judgeAnswer bool) (scannedEvent, bool) {
	if judgeAnswer {
		var event scannedEvent
		if err := strictjson.Unmarshal(payload, &event); err == nil {
			return event, true
		}
	}
	var failure scannedError
	if err := strictjson.Unmarshal(payload, &failure); err != nil {
		return scannedEvent{}, false
	}
	return scannedEvent{scannedError: failure, choicesUnreadable: judgeAnswer}, true
}

// DecodeUpstreamError accepts every error shape a host in this fleet answers with. See README.md, "Cacheability".
func DecodeUpstreamError(payload []byte) (UpstreamError, bool) {
	if !mayCarryUpstreamError(payload) {
		return UpstreamError{}, false
	}
	event, ok := decodeScannedEvent(payload, false)
	if !ok {
		return UpstreamError{}, false
	}
	return event.upstreamError()
}

func mayCarryUpstreamError(payload []byte) bool {
	return anyKeyMatches(payload, func(name []byte, valueAt int) bool {
		if bytes.EqualFold(name, []byte("error")) {
			return true
		}
		return bytes.EqualFold(name, []byte("object")) && !plainStringOtherThan(payload[valueAt:], "error")
	})
}

func plainStringOtherThan(value []byte, excluded string) bool {
	value = bytes.TrimLeft(value, jsonWhitespace)
	if len(value) == 0 || value[0] != '"' {
		return false
	}
	closing := stringLiteralEnd(value[1:])
	if closing == 0 {
		return false
	}
	literal := value[1:closing]
	return value[closing] == '"' && bytes.IndexByte(literal, '\\') < 0 && string(literal) != excluded
}

func (event scannedError) upstreamError() (UpstreamError, bool) {
	if failure, named := event.namedError(); named {
		return failure, true
	}
	if event.Object == "error" {
		return UpstreamError{Type: event.Type, Code: codeString(event.Code), Message: event.Message}, true
	}
	return UpstreamError{}, false
}

// namedError reads an "error" field in both shapes the fleet sends it in. See README.md, "Cacheability".
func (event scannedError) namedError() (UpstreamError, bool) {
	raw := bytes.TrimSpace(event.Error)
	if len(raw) == 0 || bytes.Equal(raw, jsonNullLiteral) {
		return UpstreamError{}, false
	}
	if raw[0] == '"' {
		var message string
		if err := strictjson.Unmarshal(raw, &message); err != nil || strings.TrimSpace(message) == "" {
			return UpstreamError{}, false
		}
		return UpstreamError{Type: event.ErrorType, Code: codeString(event.Code), Message: message}, true
	}
	var named struct {
		Type    string `json:"type"`
		Code    any    `json:"code"`
		Message string `json:"message"`
	}
	if err := strictjson.Unmarshal(raw, &named); err != nil {
		return UpstreamError{}, false
	}
	if named.Type == "" {
		named.Type = event.ErrorType
	}
	return UpstreamError{Type: named.Type, Code: codeString(named.Code), Message: named.Message}, true
}

var jsonNullLiteral = []byte("null")

// codeString treats a JSON null code as absent rather than the literal text "<nil>".
func codeString(code any) string {
	if code == nil {
		return ""
	}
	return fmt.Sprint(code)
}

func isCacheableErrorDetails(details UpstreamError) bool {
	if strings.TrimSpace(details.Message) == "" || isCapabilityRefusal(details.Message) {
		return false
	}
	// A numeric code is the status the host would have answered with. 400 and 422 are about the request;
	// 404 names a model another host may still serve, and 408, 429 and 5xx are about the moment.
	if status, err := strconv.Atoi(strings.TrimSpace(details.Code)); err == nil {
		return status == http.StatusBadRequest || status == http.StatusUnprocessableEntity
	}
	if namesMomentaryFailure(details.Type) || namesMomentaryFailure(details.Code) {
		return false
	}
	message := strings.ToLower(details.Message)
	for _, marker := range momentaryFailureMessages {
		if strings.Contains(message, marker) {
			return false
		}
	}
	return true
}

func namesMomentaryFailure(class string) bool {
	normalized := strings.Map(func(letter rune) rune {
		switch {
		case letter >= 'a' && letter <= 'z', letter >= '0' && letter <= '9':
			return letter
		case letter >= 'A' && letter <= 'Z':
			return letter + ('a' - 'A')
		}
		return -1
	}, class)
	if normalized == "" {
		return false
	}
	for _, named := range momentaryFailureClasses {
		if strings.Contains(normalized, named) {
			return true
		}
	}
	for _, marker := range momentaryFailureMessages {
		if strings.Contains(normalized, strings.ReplaceAll(marker, " ", "")) {
			return true
		}
	}
	return false
}

// isCapabilityRefusal keeps a refusal out of the cache: it is one host's unverified word on what it can run, and a replay would answer for hosts that never refused.
func isCapabilityRefusal(message string) bool {
	contextLimit, _ := CapabilityLimits(message)
	return strings.Contains(message, ToolChoiceUnsupportedMessage) || contextLimit > 0
}
