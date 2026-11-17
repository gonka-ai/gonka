package engine

import (
	"bytes"
	"strconv"

	json "github.com/goccy/go-json"

	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/internal/strictjson"
)

// finishReasonStop is the upstream token that lets generated tokens stand in for content.
const finishReasonStop = "stop"

var (
	deltaLabels   = sourceLabels{sourceDeltaContent, sourceDeltaReasoning, sourceDeltaReasoningContent, sourceDeltaToolCalls}
	messageLabels = sourceLabels{sourceMessageContent, sourceMessageReasoning, sourceMessageReasoningContent, sourceMessageToolCalls}
)

type sseError struct {
	Source  string
	Code    string
	Type    string
	Message string
	Payload string
}

func (e sseError) present() bool { return e.Source != "" }

type chunkSignal struct {
	chunkScan
	Error sseError
}

// crownsWinner admits content and nothing else. See race.md, "An SSE error event counts as a chunk but never crowns".
func (s chunkSignal) crownsWinner() bool { return s.ContentSource != "" }

func classifyChunk(events []byte, emptyAnswerCountsTokens bool) chunkSignal {
	signal := chunkSignal{chunkScan: scanChunk(events, emptyAnswerCountsTokens)}
	if failure, ok := errorPayload(events); ok {
		signal.Error = failure
	}
	return signal
}

// chunkScan is what one pass over a chunk answers.
type chunkScan struct {
	ContentSource         string
	UsageCompletionTokens int64
	UsagePromptTokens     int64
	LogprobTokens         int64
	LogprobsDecoded       bool
	FinishReason          string
}

// streamedEvent is every field one pass over an event reads, in the one shape the decoder is asked for.
type streamedEvent struct {
	Choices []streamedChoice `json:"choices"`
	Usage   *streamedUsage   `json:"usage"`
}

type streamedChoice struct {
	FinishReason string           `json:"finish_reason"`
	Delta        renderedParts    `json:"delta"`
	Message      renderedParts    `json:"message"`
	Logprobs     streamedLogprobs `json:"logprobs"`
}

type streamedLogprobs struct {
	Content []struct {
		Token       string `json:"token"`
		TopLogprobs []struct {
			Token string `json:"token"`
		} `json:"top_logprobs"`
	} `json:"content"`
}

type streamedUsage struct {
	CompletionTokens int64 `json:"completion_tokens"`
	PromptTokens     int64 `json:"prompt_tokens"`
}

// looseEvent is the same event with the parts a host can type against the schema left raw, so a wrong type in one of them costs that answer alone rather than every answer the event carries.
type looseEvent struct {
	Choices []struct {
		FinishReason string          `json:"finish_reason"`
		Delta        renderedParts   `json:"delta"`
		Message      renderedParts   `json:"message"`
		Logprobs     json.RawMessage `json:"logprobs"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
}

// tighten decodes each raw part on its own and drops the ones that do not fit their shape.
func (loose looseEvent) tighten() streamedEvent {
	event := streamedEvent{Choices: make([]streamedChoice, 0, len(loose.Choices))}
	var usage streamedUsage
	if len(loose.Usage) > 0 && strictjson.Unmarshal(loose.Usage, &usage) == nil {
		event.Usage = &usage
	}
	for _, choice := range loose.Choices {
		tightened := streamedChoice{FinishReason: choice.FinishReason, Delta: choice.Delta, Message: choice.Message}
		if len(choice.Logprobs) > 0 {
			_ = strictjson.Unmarshal(choice.Logprobs, &tightened.Logprobs)
		}
		event.Choices = append(event.Choices, tightened)
	}
	return event
}

// decodeEvent reads an event, falling back to the shapes that tolerate what a host got wrong: barewords for a non-finite number, then raw parts for a field typed against the schema.
func decodeEvent(payload []byte) (streamedEvent, bool) {
	var event streamedEvent
	if strictjson.Unmarshal(payload, &event) == nil {
		return event, true
	}
	// A host writes NaN/Infinity as barewords; without this its content and usage read as absent.
	if normalized, replaced := filters.ReplaceNonFiniteNumbers(payload); replaced {
		payload = normalized
		event = streamedEvent{}
		if strictjson.Unmarshal(payload, &event) == nil {
			return event, true
		}
	}
	var loose looseEvent
	if strictjson.Unmarshal(payload, &loose) != nil {
		return streamedEvent{}, false
	}
	return loose.tighten(), true
}

// scanChunk decodes each event once and answers every question the classifier asks of it.
func scanChunk(events []byte, emptyAnswerCountsTokens bool) chunkScan {
	var scan chunkScan
	filters.EachSSEDataPayload(events, func(payload []byte) bool {
		event, decoded := decodeEvent(payload)
		if !decoded {
			return false
		}
		tokens, prompt := int64(0), int64(0)
		if event.Usage != nil {
			tokens, prompt = event.Usage.CompletionTokens, event.Usage.PromptTokens
		}
		if tokens > 0 {
			scan.UsageCompletionTokens = tokens
		}
		if prompt > 0 {
			scan.UsagePromptTokens = prompt
		}
		for _, choice := range event.Choices {
			if choice.FinishReason != "" {
				scan.FinishReason = choice.FinishReason
			}
			if scan.ContentSource == "" {
				scan.ContentSource = choiceSource(choice.Delta, choice.Message, choice.FinishReason, emptyAnswerCountsTokens && tokens > 0)
			}
			if !scan.LogprobsDecoded {
				scan.LogprobsDecoded = choice.Logprobs.namesDecodedTokens()
			}
			scan.LogprobTokens += int64(len(choice.Logprobs.Content))
		}
		return false
	})
	return scan
}

// choiceSource names the field that carried something renderable, empty when the choice rendered nothing.
func choiceSource(delta, message renderedParts, finishReason string, stopStandsInForContent bool) string {
	if source, ok := delta.source(deltaLabels); ok {
		return source
	}
	if source, ok := message.source(messageLabels); ok {
		return source
	}
	if finishReason == finishReasonStop && stopStandsInForContent {
		return sourceStopWithTokens
	}
	return ""
}

// namesDecodedTokens reports whether one choice's logprobs name any token by its decoded text instead of its id.
func (l streamedLogprobs) namesDecodedTokens() bool {
	for _, entry := range l.Content {
		if !isTokenID(entry.Token) {
			return true
		}
		for _, alternative := range entry.TopLogprobs {
			if !isTokenID(alternative.Token) {
				return true
			}
		}
	}
	return false
}

func isTokenID(token string) bool {
	id, err := strconv.Atoi(token)
	return err == nil && id >= 0
}

type renderedParts struct {
	Content          string          `json:"content"`
	Reasoning        string          `json:"reasoning"`
	ReasoningContent string          `json:"reasoning_content"`
	ToolCalls        json.RawMessage `json:"tool_calls"`
}

// sourceLabels are one shape's four labels, resolved at compile time rather than built per chunk.
type sourceLabels struct {
	content          string
	reasoning        string
	reasoningContent string
	toolCalls        string
}

func (p renderedParts) source(labels sourceLabels) (string, bool) {
	switch {
	case p.Content != "":
		return labels.content, true
	case p.Reasoning != "":
		return labels.reasoning, true
	case p.ReasoningContent != "":
		return labels.reasoningContent, true
	case jsonArrayHasElements(p.ToolCalls):
		return labels.toolCalls, true
	}
	return "", false
}

// errorPayload extracts the first OpenAI-compatible error, deliberately without a byte-wise pre-filter. See README, "Classification and reassembly".
func errorPayload(events []byte) (sseError, bool) {
	var found sseError
	filters.EachSSEDataPayload(events, func(payload []byte) bool {
		upstream, ok := filters.DecodeUpstreamError(payload)
		if !ok {
			return false
		}
		source := "error"
		if upstream.Type != "" {
			source = "error." + upstream.Type
		}
		found = sseError{
			Source:  source,
			Code:    upstream.Code,
			Type:    upstream.Type,
			Message: upstream.Message,
			Payload: string(payload),
		}
		return true
	})
	return found, found.present()
}

func jsonArrayHasElements(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return false
	}
	return len(bytes.TrimSpace(trimmed[1:len(trimmed)-1])) > 0
}
