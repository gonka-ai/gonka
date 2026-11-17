package filters

import (
	"bytes"
	stdjson "encoding/json"
	"strings"

	json "github.com/goccy/go-json"
)

// sseCompletion carries every host-controlled field raw: a typed field lets a host fail the conversion with a wrong type.
type sseCompletion struct {
	ID                json.RawMessage       `json:"id"`
	Created           json.RawMessage       `json:"created"`
	Model             json.RawMessage       `json:"model"`
	SystemFingerprint json.RawMessage       `json:"system_fingerprint,omitempty"`
	Choices           []sseCompletionChoice `json:"choices"`
	Usage             json.RawMessage       `json:"usage"`
}

type sseCompletionChoice struct {
	Index        json.RawMessage            `json:"index"`
	Message      map[string]json.RawMessage `json:"message"`
	Logprobs     json.RawMessage            `json:"logprobs"`
	FinishReason json.RawMessage            `json:"finish_reason"`
	StopReason   json.RawMessage            `json:"stop_reason"`
}

type sseChunk struct {
	ID                json.RawMessage  `json:"id"`
	Object            string           `json:"object"`
	Created           json.RawMessage  `json:"created"`
	Model             json.RawMessage  `json:"model"`
	SystemFingerprint json.RawMessage  `json:"system_fingerprint,omitempty"`
	Choices           []sseChunkChoice `json:"choices"`
	Usage             json.RawMessage  `json:"usage,omitempty"`
}

type sseChunkChoice struct {
	Index        json.RawMessage            `json:"index"`
	Delta        map[string]json.RawMessage `json:"delta"`
	Logprobs     json.RawMessage            `json:"logprobs,omitempty"`
	FinishReason json.RawMessage            `json:"finish_reason"`
	StopReason   json.RawMessage            `json:"stop_reason,omitempty"`
}

// carriesChoiceMessage gates the conversion below, which is a second decode of every event that reaches it: only a choice carrying a message object can become chunks, and a streamed chunk carries a delta instead.
// It admits every spelling of a key, because the conversion's decoder binds them case-insensitively and a gate that resolved one of them would refuse a conversion that decoder would have made.
func carriesChoiceMessage(decoded any) bool {
	object, isObject := decoded.(map[string]any)
	if !isObject {
		return false
	}
	for key, value := range object {
		if !strings.EqualFold(key, "choices") {
			continue
		}
		choices, isList := value.([]any)
		if !isList {
			continue
		}
		for _, entry := range choices {
			choice, isObject := entry.(map[string]any)
			if !isObject {
				continue
			}
			for name, held := range choice {
				if _, isObject := held.(map[string]any); isObject && strings.EqualFold(name, "message") {
					return true
				}
			}
		}
	}
	return false
}

// completionAsChunks converts a complete chat.completion into the chunk events a streaming client renders. See README.md, "A complete reply on a streaming request is rewritten into chunks".
func completionAsChunks(payload []byte) ([]byte, bool) {
	var completion sseCompletion
	if stdjson.Unmarshal(payload, &completion) != nil {
		return nil, false
	}
	var events bytes.Buffer
	for _, choice := range completion.Choices {
		if choice.Message == nil {
			continue
		}
		if role, named := choice.Message["role"]; named {
			emitChunk(&events, completion, []sseChunkChoice{{
				Index: rawOr(choice.Index, "0"),
				Delta: map[string]json.RawMessage{"role": role},
			}}, nil)
		}
		delta := make(map[string]json.RawMessage, len(choice.Message))
		for field, value := range choice.Message {
			if field != "role" {
				delta[field] = value
			}
		}
		if len(delta) > 0 {
			emitChunk(&events, completion, []sseChunkChoice{{
				Index:    rawOr(choice.Index, "0"),
				Delta:    delta,
				Logprobs: presentValue(choice.Logprobs),
			}}, nil)
		}
		if stopReason := presentValue(choice.StopReason); presentValue(choice.FinishReason) != nil || stopReason != nil {
			emitChunk(&events, completion, []sseChunkChoice{{
				Index:        rawOr(choice.Index, "0"),
				Delta:        map[string]json.RawMessage{},
				FinishReason: rawOr(choice.FinishReason, "null"),
				StopReason:   stopReason,
			}}, nil)
		}
	}
	if events.Len() == 0 {
		return nil, false
	}
	if usage := presentValue(completion.Usage); usage != nil {
		emitChunk(&events, completion, []sseChunkChoice{}, usage)
	}
	return events.Bytes(), true
}

// encodeCompact drops HTML escaping, which would inflate every < > & to six bytes on a path carrying whole model responses.
func encodeCompact(value any) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := stdjson.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(encoded.Bytes(), "\n"), nil
}

func emitChunk(events *bytes.Buffer, completion sseCompletion, choices []sseChunkChoice, usage json.RawMessage) {
	encoded, err := encodeCompact(sseChunk{
		ID:                rawOr(completion.ID, `""`),
		Object:            chunkObject,
		Created:           rawOr(completion.Created, "0"),
		Model:             rawOr(completion.Model, `""`),
		SystemFingerprint: completion.SystemFingerprint,
		Choices:           choices,
		Usage:             usage,
	})
	if err != nil {
		return
	}
	events.Write(sseDataPrefix)
	events.Write(encoded)
	events.Write(sseEventSeparator)
}

// rawOr keeps a host's field verbatim; an absent one takes the fallback, since an empty raw message fails the encode.
func rawOr(raw json.RawMessage, fallback string) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(fallback)
	}
	return raw
}

// presentValue counts JSON null as absent, so a field the host spelled out as null is not re-sent.
func presentValue(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	return trimmed
}
