package api

import (
	"fmt"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/tokenizer"
)

// See docs/request.md, "Step 6 in detail".
const (
	chatTemplateMinimumTokens = 4
)

// ContextLengthError is a request whose prompt and max_tokens a host's context cannot hold together, which the host would refuse.
type ContextLengthError struct {
	ContextLength uint64
	PromptTokens  uint64
	MaxTokens     uint64
}

func (e *ContextLengthError) Error() string {
	total := e.PromptTokens + e.MaxTokens
	message := fmt.Sprintf("This model's maximum context length is %d tokens. However, the request asks for up to %d output tokens (max_tokens) and its messages take at least %d tokens with the chat template, at least %d in all. Shorten the messages by at least %d tokens",
		e.ContextLength, e.MaxTokens, e.PromptTokens, total, total-e.ContextLength)
	if e.PromptTokens < e.ContextLength {
		message += fmt.Sprintf(", or lower max_tokens to at most %d", e.ContextLength-e.PromptTokens)
	}
	return message + "."
}

// TokenizerPrompt reads body's messages for counter, known false without a counter or without prompt text.
func TokenizerPrompt(counter *tokenizer.Counter, body []byte) (PromptText, bool) {
	if counter == nil {
		return nil, false
	}
	parts, err := filters.PromptText(body)
	if err != nil {
		return nil, false
	}
	return tokenizerPrompt{counter: counter, parts: parts}, true
}

type tokenizerPrompt struct {
	counter *tokenizer.Counter
	parts   []string
}

func (p tokenizerPrompt) MaximumTokens() uint64 { return p.counter.MaximumTokens(p.parts) }

func (p tokenizerPrompt) Count() (TokenizedPrompt, bool) {
	return toTokenizedPrompt(p.counter.EstimateTokens(p.parts))
}

func (p tokenizerPrompt) CountWithPieceCap() (TokenizedPrompt, bool) {
	return toTokenizedPrompt(p.counter.EstimateTokensWithPieceCap(p.parts))
}

func toTokenizedPrompt(estimate tokenizer.Estimate, err error) (TokenizedPrompt, bool) {
	if err != nil {
		return TokenizedPrompt{}, false
	}
	return TokenizedPrompt{Tokens: estimate.Tokens, MinimumTokens: estimate.Minimum(), Sampled: estimate.Sampled}, true
}

type promptCount struct {
	tokens  uint64
	minimum uint64
	source  filters.InputTokensSource
}

type requestPrompt struct {
	bodyBytes int
	text      PromptText
	count     promptCount
	counted   bool
}

func (s *Server) checkContext(configured config.Limits, normalized filters.Result) (requestPrompt, error) {
	prompt := requestPrompt{bodyBytes: len(normalized.Body)}
	if s.promptTokens == nil {
		return prompt, nil
	}
	contextLength := configured.ContextLength(normalized.Model, s.snapshots.Snapshot().Models[normalized.Model].MaxModelLen)
	if contextLength == config.NoContextLength && !s.statsTokenizerEnabled {
		return prompt, nil
	}
	text, known := s.promptTokens.Prompt(normalized.Model, normalized.Body)
	if !known {
		return prompt, nil
	}
	if s.statsTokenizerEnabled {
		prompt.text = text
	}
	if contextLength == config.NoContextLength || text.MaximumTokens()+chatTemplateMinimumTokens+normalized.MaxTokens <= contextLength {
		return prompt, nil
	}
	prompt.count, prompt.counted = prompt.countWith(text.Count), true
	if !prompt.count.source.Tokenized() {
		return prompt, nil
	}
	promptTokens := prompt.count.minimum + chatTemplateMinimumTokens
	if promptTokens+normalized.MaxTokens <= contextLength {
		return prompt, nil
	}
	return prompt, &ContextLengthError{ContextLength: contextLength, PromptTokens: promptTokens, MaxTokens: normalized.MaxTokens}
}

func (s *Server) countPrompt(model string, prompt requestPrompt) promptCount {
	if prompt.counted {
		return prompt.count
	}
	if prompt.text == nil {
		return byteEstimate(prompt.bodyBytes)
	}
	if !s.promptCounts.tryAcquire() {
		if s.statsTokenizerFallbacks != nil {
			s.statsTokenizerFallbacks.GateFull(model)
		}
		return byteEstimate(prompt.bodyBytes)
	}
	defer s.promptCounts.release()
	return prompt.countWith(prompt.text.CountWithPieceCap)
}

func (prompt requestPrompt) countSoFar() promptCount {
	if prompt.counted {
		return prompt.count
	}
	return byteEstimate(prompt.bodyBytes)
}

func (prompt requestPrompt) countWith(count func() (TokenizedPrompt, bool)) promptCount {
	counted, known := count()
	if !known {
		return byteEstimate(prompt.bodyBytes)
	}
	source := filters.InputTokensTokenizerExact
	if counted.Sampled {
		source = filters.InputTokensTokenizerSampled
	}
	return promptCount{tokens: counted.Tokens, minimum: counted.MinimumTokens, source: source}
}

func byteEstimate(bodyBytes int) promptCount {
	return promptCount{tokens: filters.EstimatedPromptTokens(bodyBytes), source: filters.InputTokensBytesEstimate}
}

type promptCountGate chan struct{}

func newPromptCountGate(slots int64) promptCountGate {
	return make(promptCountGate, slots)
}

func (g promptCountGate) tryAcquire() bool {
	select {
	case g <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g promptCountGate) release() { <-g }
