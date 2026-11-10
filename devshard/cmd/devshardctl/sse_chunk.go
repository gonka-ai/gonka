package main

import (
	"bytes"
	"encoding/json"

	"common/completionapi"
)

// sseChunkMessage is the half of a choice that can carry something a client renders, in both the
// streaming `delta` and non-streaming `message` shapes.
type sseChunkMessage struct {
	Content          string          `json:"content"`
	Reasoning        string          `json:"reasoning"`
	ReasoningContent string          `json:"reasoning_content"`
	ToolCalls        json.RawMessage `json:"tool_calls"`
}

// sseChunkEvent is every field the classifier reads from one event, so a chunk is decoded once rather
// than once per question asked of it. Logprobs keeps completionapi's type for its UnmarshalJSON, which
// normalizes the chat `content` and completions `tokens` shapes the way the validator normalizes them.
type sseChunkEvent struct {
	Choices []struct {
		FinishReason string                       `json:"finish_reason"`
		Delta        sseChunkMessage              `json:"delta"`
		Message      sseChunkMessage              `json:"message"`
		Logprobs     completionapi.ChoiceLogprobs `json:"logprobs"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// sseChunkScan is what one pass over a chunk answers. The scan keeps walking after it has a content
// source, because the logprob judgement has to see every token the chunk carries.
type sseChunkScan struct {
	ContentSource   string
	HasContent      bool
	LogprobsFound   bool
	LogprobsDecoded bool
}

// scanSSEChunk reads a chunk once and answers both questions the write path asks of it. The error shape
// is not among them: it shares no field with these, and it is only ever consulted when no content was
// found, so it stays its own scan.
func scanSSEChunk(p []byte) sseChunkScan {
	var scan sseChunkScan
	if len(p) == 0 {
		return scan
	}
	for line := range bytes.SplitSeq(p, []byte("\n")) {
		data, ok := sseEventData(line)
		if !ok {
			continue
		}
		var event sseChunkEvent
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}
		for _, choice := range event.Choices {
			if !scan.HasContent {
				if source, found := choiceContentSource(choice.Delta, "delta"); found {
					scan.ContentSource, scan.HasContent = source, true
				} else if source, found := choiceContentSource(choice.Message, "message"); found {
					scan.ContentSource, scan.HasContent = source, true
				} else if choice.FinishReason == "stop" && event.Usage.CompletionTokens > 0 {
					scan.ContentSource, scan.HasContent = "message.empty_stop_completion_tokens", true
				}
			}
			if !scan.LogprobsDecoded {
				judgeLogprobs(choice.Logprobs, &scan)
			}
		}
	}
	return scan
}

// choiceContentSource names the field that carried something renderable, in the order the classifier
// reports it.
func choiceContentSource(message sseChunkMessage, shape string) (string, bool) {
	switch {
	case message.Content != "":
		return shape + ".content", true
	case message.Reasoning != "":
		return shape + ".reasoning", true
	case message.ReasoningContent != "":
		return shape + ".reasoning_content", true
	case hasJSONArrayElements(message.ToolCalls):
		return shape + ".tool_calls", true
	}
	return "", false
}

// judgeLogprobs marks a chunk that names any token by its decoded text instead of its id. A validator
// replays the inference from these ids; text cannot be replayed, so it votes the answer invalid and the
// host loses the reward. Every token and every alternative is inspected, because the validator rejects
// on the first one of either it cannot replay.
func judgeLogprobs(logprobs completionapi.ChoiceLogprobs, scan *sseChunkScan) {
	for _, token := range logprobs.Content {
		scan.LogprobsFound = true
		if !isTokenID(token.Token) {
			scan.LogprobsDecoded = true
			return
		}
		for _, alternative := range token.TopLogprobs {
			if !isTokenID(alternative.Token) {
				scan.LogprobsDecoded = true
				return
			}
		}
	}
}

// sseChunkHasContent reports whether the given bytes contain at least one SSE data event carrying a
// non-empty payload that an OpenAI-compatible client can surface. `content`, `reasoning`,
// `reasoning_content`, non-empty `tool_calls`, and a stopped completion with generated tokens all
// qualify in both the streaming `delta` and non-streaming `message` shapes.
//
// Deliberately NOT treated as content (even though earlier versions did):
//   - `choices[].text` — the legacy `/v1/completions` shape. The proxy's streaming path only serves
//     `/v1/chat/completions`; a host emitting `text` here produces the same "1 chunk, 0 rendered
//     tokens" failure.
//
// Role-only chunks, empty deltas, finish-only chunks, and `[DONE]` markers continue to return false.
func sseChunkHasContent(p []byte) bool {
	return scanSSEChunk(p).HasContent
}

// sseChunkContentSource is the classifying variant of sseChunkHasContent: when content is present it
// returns a short label naming the field that carried it. Used for forensic logging, so that a
// short-content winner can be told apart after the fact by what it was emitting.
func sseChunkContentSource(p []byte) (string, bool) {
	scan := scanSSEChunk(p)
	return scan.ContentSource, scan.HasContent
}

// sseChunkLogprobsDecoded reports whether a chunk names any logprob token by its decoded text instead
// of its id, and whether any token was found to judge at all. A host forwards logprobs only to a client
// that asked, so ordinary traffic leaves nothing here to judge.
func sseChunkLogprobsDecoded(p []byte) (decoded, found bool) {
	scan := scanSSEChunk(p)
	return scan.LogprobsDecoded, scan.LogprobsFound
}

// sseEventData returns one SSE line's JSON payload, skipping the terminator and anything not an event.
func sseEventData(line []byte) ([]byte, bool) {
	line = bytes.TrimRight(line, "\r")
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false
	}
	event := bytes.TrimSpace(line[len("data:"):])
	if len(event) == 0 || bytes.Equal(event, []byte("[DONE]")) {
		return nil, false
	}
	return event, true
}
