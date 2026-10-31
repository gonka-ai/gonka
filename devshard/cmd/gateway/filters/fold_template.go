package filters

import (
	"bytes"
	stdjson "encoding/json"
	"slices"
	"unicode/utf8"
)

type contentTemplate struct {
	head, tail []byte
	delta      map[string]any
	logprobs   *logprobsTemplate
}

const (
	contentKeysWithLogprobs   = 2
	minimalStringLiteralBytes = len(`""`)
)

type logprobsTemplate struct {
	middle []byte
	target map[string]any
	intent LogprobIntent
}

func newContentTemplate(payload []byte, decoded, merged map[string]any, intent LogprobIntent) *contentTemplate {
	choices, _ := decoded["choices"].([]any)
	if len(choices) != 1 {
		return nil
	}
	choice, _ := choices[0].(map[string]any)
	incomingLogprobs, carriesLogprobs := choice["logprobs"].(map[string]any)
	if _, indexed := numericIndex(choice["index"]); !indexed || !onlyDeltaGrows(choice, carriesLogprobs) {
		return nil
	}
	delta, _ := choice["delta"].(map[string]any)
	if !onlyContentGrows(delta) {
		return nil
	}
	mergedChoices, _ := merged["choices"].([]any)
	if len(mergedChoices) >= maxIndexedElements {
		return nil
	}
	mergedChoice := elementAtIndex(mergedChoices, choice["index"])
	target, _ := mergedChoice["delta"].(map[string]any)
	if target == nil {
		return nil
	}
	if !carriesLogprobs {
		start, end, found := soleContentLiteral(payload)
		if !found {
			return nil
		}
		return &contentTemplate{head: bytes.Clone(payload[:start]), tail: bytes.Clone(payload[end:]), delta: target}
	}
	mergedLogprobs, _ := mergedChoice["logprobs"].(map[string]any)
	if !onlyEntriesGrow(incomingLogprobs) || !onlyEntriesGrow(mergedLogprobs) || !entriesOnlyAppend(mergedLogprobs) {
		return nil
	}
	literalStart, literalEnd, entriesStart, entriesEnd, found := contentLiteralThenEntries(payload)
	if !found {
		return nil
	}
	return &contentTemplate{
		head:  bytes.Clone(payload[:literalStart]),
		tail:  bytes.Clone(payload[entriesEnd:]),
		delta: target,
		logprobs: &logprobsTemplate{
			middle: bytes.Clone(payload[literalEnd:entriesStart]),
			target: mergedLogprobs,
			intent: intent,
		},
	}
}

func onlyDeltaGrows(choice map[string]any, logprobsGrow bool) bool {
	for key, value := range choice {
		if key == "delta" || (logprobsGrow && key == "logprobs") {
			continue
		}
		if accumulatedChoiceFields[key] && value != nil {
			return false
		}
	}
	return true
}

func onlyContentGrows(delta map[string]any) bool {
	if _, isString := delta["content"].(string); !isString {
		return false
	}
	for key, value := range delta {
		if _, isString := value.(string); value != nil && !isString {
			return false
		}
		if key != "content" && accumulatedTextFields[key] && value != nil {
			return false
		}
	}
	return true
}

func onlyEntriesGrow(logprobs map[string]any) bool {
	_, isList := logprobs["content"].([]any)
	return len(logprobs) == 1 && isList
}

func entriesOnlyAppend(logprobs map[string]any) bool {
	entries, _ := logprobs["content"].([]any)
	return len(entries) > 0 && !leadsWithAnIndex(entries)
}

func soleContentLiteral(payload []byte) (start, end int, found bool) {
	contentKeys := 0
	unreadableKey := anyKeyMatches(payload, func(name []byte, valueAt int) bool {
		if string(name) == "content" {
			contentKeys++
			start = skipWhitespace(payload, valueAt)
		}
		return false
	})
	if unreadableKey || contentKeys != 1 || start >= len(payload) || payload[start] != '"' {
		return 0, 0, false
	}
	return start, stringEnd(payload, start), true
}

func contentLiteralThenEntries(payload []byte) (literalStart, literalEnd, entriesStart, entriesEnd int, found bool) {
	var valueStarts []int
	unreadableKey := anyKeyMatches(payload, func(name []byte, valueAt int) bool {
		if string(name) == "content" {
			valueStarts = append(valueStarts, skipWhitespace(payload, valueAt))
		}
		return false
	})
	if unreadableKey || len(valueStarts) != contentKeysWithLogprobs {
		return 0, 0, 0, 0, false
	}
	literalStart, entriesStart = valueStarts[0], valueStarts[1]
	if literalStart >= len(payload) || payload[literalStart] != '"' || entriesStart >= len(payload) || payload[entriesStart] != '[' {
		return 0, 0, 0, 0, false
	}
	literalEnd, entriesEnd = stringEnd(payload, literalStart), skipValidatedValue(payload, entriesStart)
	return literalStart, literalEnd, entriesStart, entriesEnd, literalEnd <= entriesStart
}

func (t *contentTemplate) appendChunk(payload []byte) bool {
	if t.logprobs == nil {
		text, matched := t.text(payload)
		if matched {
			t.delta["content"] = growText("content", t.delta["content"], text)
		}
		return matched
	}
	text, entries, matched := t.textAndEntries(payload)
	if matched {
		t.delta["content"] = growText("content", t.delta["content"], text)
		existing, _ := t.logprobs.target["content"].([]any)
		t.logprobs.target["content"] = append(existing, entries...)
	}
	return matched
}

func (t *contentTemplate) text(payload []byte) (string, bool) {
	if !t.enclosedBy(payload, 0) {
		return "", false
	}
	literal := payload[len(t.head) : len(payload)-len(t.tail)]
	if literal[0] != '"' || literal[len(literal)-1] != '"' || stringEnd(literal, 0) != len(literal) {
		return "", false
	}
	return literalText(literal)
}

func (t *contentTemplate) textAndEntries(payload []byte) (string, []any, bool) {
	if !t.enclosedBy(payload, len(t.logprobs.middle)) {
		return "", nil, false
	}
	literalStart := len(t.head)
	if payload[literalStart] != '"' {
		return "", nil, false
	}
	literalEnd := stringEnd(payload, literalStart)
	entriesStart := literalEnd + len(t.logprobs.middle)
	entriesEnd := len(payload) - len(t.tail)
	if entriesStart >= entriesEnd || payload[entriesStart] != '[' || !bytes.Equal(payload[literalEnd:entriesStart], t.logprobs.middle) {
		return "", nil, false
	}
	text, readable := literalText(payload[literalStart:literalEnd])
	if !readable {
		return "", nil, false
	}
	entries, kept := canonicalEntries(payload[entriesStart:entriesEnd], t.logprobs.intent)
	return text, entries, kept
}

func (t *contentTemplate) enclosedBy(payload []byte, innerBytes int) bool {
	return len(payload) >= len(t.head)+innerBytes+len(t.tail)+minimalStringLiteralBytes && bytes.HasPrefix(payload, t.head) && bytes.HasSuffix(payload, t.tail)
}

func literalText(literal []byte) (string, bool) {
	inner := literal[1 : len(literal)-1]
	if bytes.IndexByte(inner, '\\') < 0 && utf8.Valid(inner) && !slices.ContainsFunc(inner, func(symbol byte) bool { return symbol < firstNonControlByte }) {
		return string(inner), true
	}
	var text string
	return text, stdjson.Unmarshal(literal, &text) == nil
}
