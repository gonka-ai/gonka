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
		Index        int                          `json:"index"`
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
	// TextBytes are the bytes of text the first choice's deltas carry and ToolCallIndexes the
	// indexes of the tool calls they name. A whole message in a chunk is not counted: a streaming
	// client reads deltas only.
	TextBytes       uint64
	ToolCallIndexes []int
	// ToolCallArguments are the argument fragments of the tool calls, by call index, in order.
	ToolCallArguments []toolCallArguments
}

type toolCallArguments struct {
	index int
	text  string
}

// maxCountedToolCalls caps the calls an attempt's set remembers; the executor writes the
// indexes, and a count past the cap only lowers its own bound.
const maxCountedToolCalls = 1024

// toolCallSet counts the tool calls of an attempt's deltas by index, as a client assembles them:
// a name spread over several deltas opens one call. It also counts the top-level arguments of
// each call across the fragments its deltas stream.
type toolCallSet struct {
	seen      map[int]struct{}
	arguments map[int]*argumentScan
	keys      uint64
}

// argumentScan follows one call's streamed arguments far enough to count its top-level keys:
// a colon outside strings at depth one.
type argumentScan struct {
	depth    int
	inString bool
	escaped  bool
}

func (a *argumentScan) feed(text string) (keys uint64) {
	for i := 0; i < len(text); i++ {
		c := text[i]
		if a.inString {
			switch {
			case a.escaped:
				a.escaped = false
			case c == '\\':
				a.escaped = true
			case c == '"':
				a.inString = false
			}
			continue
		}
		switch c {
		case '"':
			a.inString = true
		case '{', '[':
			a.depth++
		case '}', ']':
			a.depth--
		case ':':
			if a.depth == 1 {
				keys++
			}
		}
	}
	return keys
}

// addArguments feeds the fragments to their calls' scans and returns the top-level arguments
// counted so far.
func (s *toolCallSet) addArguments(fragments []toolCallArguments) uint64 {
	for _, fragment := range fragments {
		if s.arguments == nil {
			s.arguments = make(map[int]*argumentScan)
		}
		scan, ok := s.arguments[fragment.index]
		if !ok {
			if len(s.arguments) >= maxCountedToolCalls {
				continue
			}
			scan = &argumentScan{}
			s.arguments[fragment.index] = scan
		}
		s.keys += scan.feed(fragment.text)
	}
	return s.keys
}

// add records the indexes of one chunk and returns the calls counted so far.
func (s *toolCallSet) add(indexes []int) uint64 {
	for _, index := range indexes {
		if s.seen == nil {
			s.seen = make(map[int]struct{})
		}
		if len(s.seen) < maxCountedToolCalls {
			s.seen[index] = struct{}{}
		}
	}
	return uint64(len(s.seen))
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
			if choice.Index == 0 {
				textBytes, calls, arguments := choiceTextBytes(choice.Delta)
				scan.TextBytes += textBytes
				scan.ToolCallIndexes = append(scan.ToolCallIndexes, calls...)
				scan.ToolCallArguments = append(scan.ToolCallArguments, arguments...)
			}
		}
	}
	return scan
}

// sseToolCallDelta is the part of a streamed tool call that reaches a client as text.
type sseToolCallDelta struct {
	Index    *int `json:"index"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// choiceTextBytes counts the text a delta hands a client: content, reasoning, and the names and
// arguments of tool calls, and returns the indexes of the calls it names and their argument
// fragments. A call costs at least a
// byte of name, so headers do not come free; a call without an index is its position in the
// delta. vLLM sends reasoning under one key, so a delta that repeats it under reasoning_content
// counts once.
func choiceTextBytes(delta sseChunkMessage) (textBytes uint64, calls []int, fragments []toolCallArguments) {
	textBytes = uint64(len(delta.Content))
	if delta.Reasoning != "" {
		textBytes += uint64(len(delta.Reasoning))
	} else {
		textBytes += uint64(len(delta.ReasoningContent))
	}
	if !hasJSONArrayElements(delta.ToolCalls) {
		return textBytes, nil, nil
	}
	var deltas []sseToolCallDelta
	if json.Unmarshal(delta.ToolCalls, &deltas) != nil {
		return textBytes, nil, nil
	}
	for i, call := range deltas {
		index := i
		if call.Index != nil {
			index = *call.Index
		}
		if call.Function.Name != "" {
			calls = append(calls, index)
		}
		textBytes += uint64(len(call.Function.Name))
		var arguments string
		if json.Unmarshal(call.Function.Arguments, &arguments) == nil {
			textBytes += uint64(len(arguments))
			if arguments != "" {
				fragments = append(fragments, toolCallArguments{index: index, text: arguments})
			}
		} else if len(call.Function.Arguments) > 0 && string(call.Function.Arguments) != "null" {
			textBytes += uint64(len(call.Function.Arguments))
		}
	}
	return textBytes, calls, fragments
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
