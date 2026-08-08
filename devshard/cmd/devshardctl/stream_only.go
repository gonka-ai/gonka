package main

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

const (
	completionObject = "chat.completion"

	maxIndexedElements = 256
	maxTopLevelFields  = 64
	maxAssembledEvents = 65_536
)

var (
	forcedStreamOptions = map[string]any{"include_usage": true}

	sseEventSeparator = []byte("\n\n")
	sseDataPrefix     = []byte("data:")
	sseDonePayload    = []byte("[DONE]")

	noResponseDataBody = []byte(`{"error":{"message":"no response data"}}`)

	nonFiniteLiterals = [][]byte{[]byte("-Infinity"), []byte("Infinity"), []byte("NaN")}

	accumulatedChoiceFields = map[string]bool{"delta": true, "logprobs": true, "token_ids": true}

	accumulatedTextFields = map[string]bool{
		"content": true, "reasoning": true, "reasoning_content": true, "refusal": true, "arguments": true,
	}
)

type growingText struct{ parts strings.Builder }

func newGrowingText(head string) *growingText {
	text := &growingText{}
	text.parts.WriteString(head)
	return text
}

func (t *growingText) String() string { return t.parts.String() }

func (t *growingText) MarshalJSON() ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(t.parts.String()); err != nil {
		return nil, err
	}
	return bytes.TrimRight(encoded.Bytes(), "\n"), nil
}

func assembleSSEBody(body []byte) []byte {
	var merged map[string]any
	var complete []byte
	framed, assembled := false, 0
	for _, event := range bytes.SplitAfter(body, sseEventSeparator) {
		if assembled >= maxAssembledEvents {
			break
		}
		payload, held := sseEventPayload(event)
		if !held {
			continue
		}
		framed = true
		if len(payload) == 0 || bytes.Equal(payload, sseDonePayload) {
			continue
		}
		decoded, parsed := decodeStreamedEvent(payload)
		if !parsed {
			continue
		}
		if name, isString := decoded["object"].(string); isString && name == completionObject {
			complete = payload
			break
		}
		merged = mergeChunk(merged, decoded)
		assembled++
	}
	switch {
	case len(complete) > 0:
		return complete
	case merged != nil:
		return encodeCompletion(merged)
	case framed || len(bytes.TrimSpace(body)) == 0:
		return noResponseDataBody
	default:
		return body
	}
}

func sseEventPayload(event []byte) ([]byte, bool) {
	var joined []byte
	held := false
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(line, sseDataPrefix) {
			continue
		}
		if held {
			joined = append(joined, '\n')
		}
		joined = append(joined, bytes.TrimPrefix(line, sseDataPrefix)...)
		held = true
	}
	return bytes.TrimSpace(joined), held
}

func decodeStreamedEvent(payload []byte) (map[string]any, bool) {
	decoded, parsed := decodeStreamedJSON(payload)
	if !parsed {
		normalized, replaced := replaceNonFiniteNumbers(payload)
		if !replaced {
			return nil, false
		}
		if decoded, parsed = decodeStreamedJSON(normalized); !parsed {
			return nil, false
		}
	}
	object, isObject := decoded.(map[string]any)
	return object, isObject
}

func decodeStreamedJSON(payload []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil || decoder.More() {
		return nil, false
	}
	return decoded, true
}

func encodeCompletion(merged map[string]any) []byte {
	merged["object"] = completionObject
	choices, _ := merged["choices"].([]any)
	for _, entry := range choices {
		choice, isObject := entry.(map[string]any)
		if !isObject {
			continue
		}
		delta, held := choice["delta"]
		if !held {
			continue
		}
		delete(choice, "delta")
		choice["message"] = delta
		if message, isObject := delta.(map[string]any); isObject {
			if _, carries := message["content"]; !carries {
				message["content"] = nil
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
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(merged); err != nil {
		return noResponseDataBody
	}
	return bytes.TrimRight(encoded.Bytes(), "\n")
}

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

func growText(field string, accumulated any, incoming string) any {
	switch previous := accumulated.(type) {
	case *growingText:
		if field == "arguments" && strings.HasPrefix(incoming, previous.String()) {
			return newGrowingText(incoming)
		}
		previous.parts.WriteString(incoming)
		return previous
	case string:
		if field == "arguments" && previous != "" && strings.HasPrefix(incoming, previous) {
			return newGrowingText(incoming)
		}
		return newGrowingText(previous + incoming)
	}
	return newGrowingText(incoming)
}

func mergeDeltaElement(target, incoming map[string]any) {
	for key, value := range incoming {
		target[key] = mergeStreamedValue(key, target[key], value)
	}
}

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

// leadsWithAnIndex reads only the first element on purpose: scanning all of it per chunk was what
// made the merge quadratic.
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
	case json.Number:
		position, err := typed.Int64()
		return position, err == nil
	case float64:
		return int64(typed), typed == float64(int64(typed))
	}
	return 0, false
}

// replaceNonFiniteNumbers rewrites -Infinity/Infinity/NaN to null outside string literals: none is
// valid JSON, so a chunk carrying one would otherwise be dropped whole.
func replaceNonFiniteNumbers(body []byte) ([]byte, bool) {
	carries := false
	for _, literal := range nonFiniteLiterals {
		if bytes.Contains(body, literal) {
			carries = true
			break
		}
	}
	if !carries {
		return nil, false
	}
	out := make([]byte, 0, len(body))
	inString, escaped, replaced := false, false, false
	for index := 0; index < len(body); {
		current := body[index]
		switch {
		case escaped:
			escaped = false
		case inString && current == '\\':
			escaped = true
		case current == '"':
			inString = !inString
		case !inString:
			if literal := matchNonFinite(body[index:]); literal > 0 {
				out = append(out, []byte("null")...)
				index += literal
				replaced = true
				continue
			}
		}
		out = append(out, current)
		index++
	}
	return out, replaced
}

func matchNonFinite(tail []byte) int {
	for _, literal := range nonFiniteLiterals {
		if bytes.HasPrefix(tail, literal) {
			return len(literal)
		}
	}
	return 0
}
