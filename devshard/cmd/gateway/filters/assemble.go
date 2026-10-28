package filters

import (
	"bytes"
	stdjson "encoding/json"
	"sort"
	"strings"
)

// See README.md, "Folding a stream into one body".
const (
	completionObject = "chat.completion"

	errorObject = "error"

	assistantRole = "assistant"
	noText        = ""

	maxIndexedElements = 256
	maxTopLevelFields  = 64
	maxAssembledEvents = 65_536
)

// growingText keeps the join off the per-chunk path, where it would be quadratic in the answer's length.
type growingText struct{ parts strings.Builder }

func newGrowingText(head string) *growingText {
	text := &growingText{}
	text.parts.WriteString(head)
	return text
}

func (t *growingText) String() string { return t.parts.String() }

// MarshalJSON encodes rather than Marshals: Marshal always escapes HTML, the very thing encodeCompact turns off.
func (t *growingText) MarshalJSON() ([]byte, error) {
	return encodeCompact(t.parts.String())
}

// assembleSSEBody merges the streamed chunks into the single JSON body a non-streaming caller expects.
func assembleSSEBody(body []byte) []byte {
	var merged map[string]any
	var complete []byte
	framed, assembled, truncated := false, 0, false
	forEachSSEEvent(body, func(event []byte) bool {
		if assembled >= maxAssembledEvents {
			truncated = true
			return true
		}
		// Joined the way a client joins them: a host may split one object across two data lines.
		_, payload, held := eventPayload(event)
		if !held {
			return false
		}
		framed = true
		payload = bytes.TrimSpace(payload)
		if len(payload) == 0 || bytes.Equal(payload, sseDoneMarker) {
			return false
		}
		decoded, parsed := decodeStreamedEvent(payload)
		if !parsed {
			return false
		}
		if name, isString := decoded["object"].(string); isString && name == completionObject {
			if merged == nil {
				complete = payload
				return true
			}
			return false
		}
		merged = mergeChunk(merged, decoded)
		assembled++
		return false
	})
	switch {
	case truncated:
		return TruncatedResponseBody
	case len(complete) > 0:
		return complete
	case merged != nil:
		return encodeCompletion(merged)
	case framed || len(bytes.TrimSpace(body)) == 0:
		return NoResponseDataBody
	default:
		return body
	}
}

// decodeStreamedEvent stays on the standard library with UseNumber, as stripInternalFields does.
func decodeStreamedEvent(payload []byte) (map[string]any, bool) {
	decoder := stdjson.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var decoded any
	// More() matches the strip: a payload carrying a second object is one no client can read.
	if err := decoder.Decode(&decoded); err != nil || decoder.More() {
		normalized, replaced := ReplaceNonFiniteNumbers(payload)
		if !replaced {
			return nil, false
		}
		decoder = stdjson.NewDecoder(bytes.NewReader(normalized))
		decoder.UseNumber()
		decoded = nil
		if err := decoder.Decode(&decoded); err != nil || decoder.More() {
			return nil, false
		}
	}
	object, isObject := decoded.(map[string]any)
	return object, isObject
}

// encodeCompletion turns each choice's accumulated delta into its message, ordered by index.
func encodeCompletion(merged map[string]any) []byte {
	finalizeCompletion(merged)
	encoded, err := encodeCompact(merged)
	if err != nil {
		return NoResponseDataBody
	}
	return encoded
}

// finalizeCompletion rewrites the accumulator in place, so it runs once at the end, never as part of measuring it.
func finalizeCompletion(merged map[string]any) {
	choices, _ := merged["choices"].([]any)
	if _, carriesError := merged["error"]; carriesError && len(choices) == 0 {
		return
	}
	if merged["object"] != errorObject {
		merged["object"] = completionObject
	}
	for _, entry := range choices {
		choice, isObject := entry.(map[string]any)
		if !isObject {
			continue
		}
		if delta, held := choice["delta"]; held {
			delete(choice, "delta")
			choice["message"] = delta
			if message, isObject := delta.(map[string]any); isObject {
				finalizeMessage(message)
			}
		}
	}
	sort.SliceStable(choices, func(first, second int) bool {
		left, leftKnown := choiceIndex(choices[first])
		right, rightKnown := choiceIndex(choices[second])
		if leftKnown != rightKnown {
			return leftKnown
		}
		return leftKnown && left < right
	})
}

// mergeChunk folds one chunk in. Everything outside choices is a restated header, so it replaces.
func mergeChunk(accumulated, chunk map[string]any) map[string]any {
	if accumulated == nil {
		accumulated = make(map[string]any, len(chunk))
	}
	for key, value := range chunk {
		if key == "choices" || value == nil {
			continue
		}
		if _, known := accumulated[key]; !known && len(accumulated) >= maxTopLevelFields {
			continue
		}
		accumulated[key] = value
	}
	incoming, _ := chunk["choices"].([]any)
	if len(incoming) == 0 {
		return accumulated
	}
	existing, _ := accumulated["choices"].([]any)
	accumulated["choices"] = mergeIndexedElements(existing, incoming, mergeChoice)
	return accumulated
}

// What grows across chunks; everything else is a restated fact that replaces.
var (
	accumulatedChoiceFields = map[string]bool{"delta": true, "logprobs": true, "token_ids": true}

	accumulatedTextFields = map[string]bool{
		"content": true, "reasoning": true, "reasoning_content": true, "refusal": true, "arguments": true,
	}
)

func mergeChoice(target, incoming map[string]any) {
	for key, value := range incoming {
		if value == nil {
			continue
		}
		if accumulatedChoiceFields[key] {
			target[key] = mergeStreamedValue(key, target[key], value)
			continue
		}
		target[key] = value
	}
}

// mergeStreamedValue accumulates by field name, never by Go type: growing a restated identity field breaks a tool call.
func mergeStreamedValue(field string, accumulated, incoming any) any {
	switch typed := incoming.(type) {
	case nil:
		return accumulated
	case string:
		if !accumulatedTextFields[field] {
			return typed
		}
		return growText(field, accumulated, typed)
	case map[string]any:
		previous, isObject := accumulated.(map[string]any)
		if !isObject {
			return typed
		}
		for key, value := range typed {
			previous[key] = mergeStreamedValue(key, previous[key], value)
		}
		return previous
	case []any:
		previous, isArray := accumulated.([]any)
		if !isArray {
			return typed
		}
		if leadsWithAnIndex(previous) && indexedElements(typed) {
			return mergeIndexedElements(previous, typed, mergeDeltaElement)
		}
		return append(previous, typed...)
	}
	return incoming
}

// growText appends to the accumulator, and replaces instead when a host re-sends arguments whole.
func growText(field string, accumulated any, incoming string) any {
	switch previous := accumulated.(type) {
	case *growingText:
		if resentWhole(field, previous.String(), incoming) {
			return newGrowingText(incoming)
		}
		previous.parts.WriteString(incoming)
		return previous
	case string:
		if resentWhole(field, previous, incoming) {
			return newGrowingText(incoming)
		}
		return newGrowingText(previous + incoming)
	}
	return newGrowingText(incoming)
}

// finalizeMessage gives the folded message the shape a non-streamed OpenAI answer has. See README.md, "What the fold returns".
func finalizeMessage(message map[string]any) {
	if role, _ := message["role"].(string); role == noText {
		message["role"] = assistantRole
	}
	toolCalls, _ := message["tool_calls"].([]any)
	if _, carries := message["tool_calls"]; carries && len(toolCalls) == 0 {
		delete(message, "tool_calls")
	}
	for _, entry := range toolCalls {
		if call, isObject := entry.(map[string]any); isObject {
			delete(call, "index")
		}
	}
	content, carriesContent := message["content"]
	switch {
	case len(toolCalls) > 0 && (!carriesContent || isEmptyText(content)):
		message["content"] = nil
	case !carriesContent:
		message["content"] = noText
	}
}

func isEmptyText(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == noText
	case *growingText:
		return typed.String() == noText
	}
	return false
}

func resentWhole(field, accumulated, incoming string) bool {
	return field == "arguments" && strings.HasPrefix(incoming, accumulated)
}

func mergeDeltaElement(target, incoming map[string]any) {
	for key, value := range incoming {
		target[key] = mergeStreamedValue(key, target[key], value)
	}
}

// mergeIndexedElements folds incoming into existing by index, appending the unclaimed ones.
func mergeIndexedElements(existing, incoming []any, merge func(target, incoming map[string]any)) []any {
	for _, entry := range incoming {
		if len(existing) >= maxIndexedElements {
			return existing
		}
		element, isObject := entry.(map[string]any)
		if !isObject {
			existing = append(existing, entry)
			continue
		}
		if target := elementAtIndex(existing, element["index"]); target != nil {
			merge(target, element)
			continue
		}
		existing = append(existing, element)
	}
	return existing
}

func elementAtIndex(elements []any, wanted any) map[string]any {
	position, known := numericIndex(wanted)
	if !known {
		return nil
	}
	for _, entry := range elements {
		element, isObject := entry.(map[string]any)
		if !isObject {
			continue
		}
		if held, ok := numericIndex(element["index"]); ok && held == position {
			return element
		}
	}
	return nil
}

// leadsWithAnIndex decides by the first element: scanning all of it per chunk was what made the merge quadratic.
func leadsWithAnIndex(elements []any) bool {
	if len(elements) == 0 {
		return false
	}
	return indexedElements(elements[:1])
}

func indexedElements(elements []any) bool {
	for _, entry := range elements {
		element, isObject := entry.(map[string]any)
		if !isObject {
			return false
		}
		if _, known := numericIndex(element["index"]); !known {
			return false
		}
	}
	return len(elements) > 0
}

func choiceIndex(entry any) (int64, bool) {
	choice, isObject := entry.(map[string]any)
	if !isObject {
		return 0, false
	}
	return numericIndex(choice["index"])
}

func numericIndex(value any) (int64, bool) {
	switch typed := value.(type) {
	case stdjson.Number:
		position, err := typed.Int64()
		return position, err == nil
	case float64:
		return int64(typed), typed == float64(int64(typed))
	}
	return 0, false
}
