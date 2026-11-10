package filters

import (
	"bytes"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"slices"
)

// See README.md, "SSE framing" and "Folding a stream into one body".
const (
	MaxStreamCarryBytes = 32 << 20

	chunkObject = "chat.completion.chunk"

	jsonWhitespace      = " \t\r\n"
	firstNonControlByte = 0x20
	crlfLength          = len("\r\n")
)

// See README.md, "SSE framing" and "What the fold returns".
var (
	sseLineSeparator      = []byte("\n")
	sseEventSeparator     = []byte("\n\n")
	sseEventSeparatorCRLF = []byte("\r\n\r\n")
	sseDataPrefix         = []byte("data: ")
	sseDoneMarker         = []byte("[DONE]")

	sseDataParsePrefix = []byte("data:")

	SSEDoneEvent = []byte("data: [DONE]\n\n")

	NoResponseDataBody = []byte(`{"error":{"message":"no response data"}}`)

	TruncatedResponseBody = []byte(`{"error":{"message":"response exceeded the assembler's event budget"}}`)

	ErrStreamCarryOverflow  = errors.New("sse event exceeds carry limit")
	ErrStreamTruncatedEvent = errors.New("sse stream ended mid-event")

	usageKey        = []byte("usage")
	messageKey      = []byte("message")
	cachedTokensKey = []byte(cachedTokensField)
)

// StreamRewriter strips the hidden fields from an SSE stream, emitting complete events only.
type StreamRewriter struct {
	intent                  LogprobIntent
	keepUsage               bool
	promptTokens            uint64
	carry                   []byte
	scanned                 int
	failed                  bool
	lastOutputMayCarryError bool
}

// NewStreamRewriter drops the forced usage event unless keepUsage says the client asked for it, and bounds its cached_tokens by promptTokens, the gateway's own count.
func NewStreamRewriter(intent LogprobIntent, keepUsage bool, promptTokens uint64) *StreamRewriter {
	return &StreamRewriter{intent: intent, keepUsage: keepUsage, promptTokens: promptTokens}
}

// Write returns every event chunk completes, rewritten; past MaxStreamCarryBytes the rewriter fails permanently.
func (r *StreamRewriter) Write(chunk []byte) ([]byte, error) {
	r.lastOutputMayCarryError = false
	if r.failed {
		return nil, ErrStreamCarryOverflow
	}
	r.carry = append(r.carry, chunk...)
	var out bytes.Buffer
	eventStart, searchFrom := 0, r.scanned
	for {
		offset := indexEventEnd(r.carry[searchFrom:])
		if offset < 0 {
			break
		}
		eventEnd := searchFrom + offset
		rewritten, _, provenErrorFree := rewriteEvent(r.carry[eventStart:eventEnd], r.intent, r.keepUsage, r.promptTokens)
		r.lastOutputMayCarryError = r.lastOutputMayCarryError || !provenErrorFree
		out.Write(rewritten)
		eventStart, searchFrom = eventEnd, eventEnd
	}
	r.carry = append(r.carry[:0], r.carry[eventStart:]...)
	r.scanned = max(0, len(r.carry)-len(sseEventSeparatorCRLF)+1)
	if len(r.carry) > MaxStreamCarryBytes {
		carried := len(r.carry)
		r.carry, r.scanned = nil, 0
		r.failed = true
		return out.Bytes(), fmt.Errorf("%w: %d bytes", ErrStreamCarryOverflow, carried)
	}
	return out.Bytes(), nil
}

// Close rewrites a well-formed trailing partial, and drops an unparseable one with ErrStreamTruncatedEvent.
func (r *StreamRewriter) Close() ([]byte, error) {
	carry := r.carry
	r.carry, r.scanned, r.lastOutputMayCarryError = nil, 0, false
	if r.failed {
		return nil, ErrStreamCarryOverflow
	}
	if len(carry) == 0 {
		return nil, nil
	}
	final, malformed, provenErrorFree := rewriteEvent(carry, r.intent, r.keepUsage, r.promptTokens)
	if malformed {
		return nil, ErrStreamTruncatedEvent
	}
	r.lastOutputMayCarryError = !provenErrorFree
	return final, nil
}

// LastOutputMayCarryUpstreamError reports whether what the last Write or Close returned holds an event the rewrite did not prove free of an upstream error.
func (r *StreamRewriter) LastOutputMayCarryUpstreamError() bool {
	return r.lastOutputMayCarryError
}

// rewriteEvent returns the event as the client must read it, forwarding only what the decoded payload allows. See README.md, "Rewriting an event".
func rewriteEvent(event []byte, intent LogprobIntent, keepUsage bool, promptTokens uint64) (rewritten []byte, malformed, provenErrorFree bool) {
	dataLines, payload, held := eventPayload(event)
	if !held {
		return event, false, true
	}
	if dataLines == 1 {
		if !intent.Keep && carriesNothingToRewrite(payload, keepUsage) {
			return event, false, true
		}
		if stripped, changed, handled := stripWithoutDecoding(payload, intent, keepUsage); handled {
			if !changed {
				return event, false, false
			}
			return rebuildEvent(event, stripped), false, false
		}
	}
	rewritten, malformed = rewriteDecodedEvent(event, dataLines, payload, intent, keepUsage, promptTokens)
	return rewritten, malformed, false
}

func stripWithoutDecoding(payload []byte, intent LogprobIntent, keepUsage bool) (stripped []byte, changed, handled bool) {
	reshaped := anyKeyMatches(payload, func(name []byte, _ int) bool {
		return (!keepUsage && bytes.EqualFold(name, usageKey)) || bytes.EqualFold(name, messageKey) || bytes.EqualFold(name, cachedTokensKey)
	})
	if reshaped {
		return nil, false, false
	}
	return stripMembersWithoutDecoding(payload, intent)
}

func rewriteDecodedEvent(event []byte, dataLines int, payload []byte, intent LogprobIntent, keepUsage bool, promptTokens uint64) (rewritten []byte, malformed bool) {
	// A payload that opens as an object and does not parse would carry whatever it hides.
	unreadable := func() ([]byte, bool) {
		if bytes.HasPrefix(bytes.TrimLeft(payload, " \t"), []byte("{")) {
			return nil, true
		}
		return event, false
	}
	decoded, changed, ok := decodePayload(payload)
	if !ok {
		return unreadable()
	}
	changed = stripDecodedFields(decoded, intent) || changed
	if !keepUsage {
		dropped, emptied := dropUsage(decoded)
		if emptied {
			return nil, false
		}
		changed = dropped || changed
	}
	changed = boundCachedTokens(decoded, promptTokens) || changed
	filtered := payload
	if changed {
		encoded, err := encodeCompact(decoded)
		if err != nil {
			return unreadable()
		}
		filtered = encoded
	}
	if carriesChoiceMessage(decoded) {
		if _, failed := DecodeUpstreamError(filtered); !failed {
			if chunks, converted := completionAsChunks(filtered); converted {
				return chunks, false
			}
		}
	}
	if !changed && dataLines == 1 {
		return event, false
	}
	return rebuildEvent(event, filtered), false
}

var rewriteTriggers = slices.Concat(clientStrippedFields, []string{"message", cachedTokensField})

func carriesNothingToRewrite(payload []byte, keepUsage bool) bool {
	payload = bytes.Trim(payload, jsonWhitespace)
	if len(payload) == 0 || payload[0] != '{' {
		return false
	}
	triggered := anyKeyMatches(payload, func(name []byte, valueAt int) bool {
		if !keepUsage && bytes.EqualFold(name, usageKey) {
			return true
		}
		return namesUpstreamError(payload, name, valueAt) || slices.ContainsFunc(rewriteTriggers, func(trigger string) bool { return bytes.EqualFold(name, []byte(trigger)) })
	})
	return !triggered && stdjson.Valid(payload)
}

func anyKeyMatches(payload []byte, matches func(name []byte, valueAt int) bool) bool {
	for index := 0; index < len(payload); index++ {
		if payload[index] != '"' {
			continue
		}
		rest := payload[index+1:]
		if len(rest) == 0 {
			return false
		}
		closing := index + stringLiteralEnd(rest)
		if colon, isKey := colonAfter(payload, closing); isKey {
			name := payload[index+1 : closing]
			if bytes.IndexByte(name, '\\') >= 0 {
				var unescaped string
				if stdjson.Unmarshal(payload[index:closing+1], &unescaped) != nil {
					return true
				}
				name = []byte(unescaped)
			}
			if matches(name, colon+1) {
				return true
			}
		}
		index = closing
	}
	return false
}

func colonAfter(payload []byte, closing int) (int, bool) {
	rest := bytes.TrimLeft(payload[closing+1:], jsonWhitespace)
	if len(rest) == 0 || rest[0] != ':' {
		return 0, false
	}
	return len(payload) - len(rest), true
}

// chunkHousekeepingFields are what the final usage event carries besides its usage.
var chunkHousekeepingFields = map[string]bool{
	"id": true, "object": true, "created": true, "model": true, "system_fingerprint": true,
	"service_tier": true, "choices": true,
}

// onlyHousekeepingLeft: testing for empty choices instead would delete a host's error event, which carries none either.
func onlyHousekeepingLeft(decoded map[string]any) bool {
	for field := range decoded {
		if !chunkHousekeepingFields[field] {
			return false
		}
	}
	choices, _ := decoded["choices"].([]any)
	return len(choices) == 0
}

// dropUsage deletes a usage the client never asked for from what the strip decoded, emptied for an event left with none.
func dropUsage(decoded any) (dropped, emptied bool) {
	event, isObject := decoded.(map[string]any)
	if !isObject {
		return false, false
	}
	if _, held := event["usage"]; !held {
		return false, false
	}
	delete(event, "usage")
	return true, onlyHousekeepingLeft(event)
}
