package public

import (
	"encoding/json"
	"errors"
)

type StringOrArray []string

func (s *StringOrArray) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = nil
		return nil
	}

	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		*s = []string{str}
		return nil
	}

	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*s = arr
		return nil
	}

	return errors.New("expected string or array of strings")
}

func (s StringOrArray) First() string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

type OpenRouterArchitecture struct {
	Modality         string   `json:"modality"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
	Tokenizer        string   `json:"tokenizer"`
	InstructType     string   `json:"instruct_type,omitempty"`
}

type OpenRouterPricing struct {
	Prompt          string `json:"prompt"`
	Completion      string `json:"completion"`
	Request         string `json:"request"`
	Image           string `json:"image"`
	InputCacheRead  string `json:"input_cache_read,omitempty"`
	InputCacheWrite string `json:"input_cache_write,omitempty"`
}

type OpenRouterTopProvider struct {
	ContextLength       uint64 `json:"context_length"`
	MaxCompletionTokens uint64 `json:"max_completion_tokens"`
	IsModerated         bool   `json:"is_moderated"`
}

type OpenRouterModel struct {
	ID                  string                  `json:"id"`
	Name                string                  `json:"name"`
	Created             int64                   `json:"created"`
	Description         string                  `json:"description,omitempty"`
	ContextLength       uint64                  `json:"context_length"`
	Architecture        *OpenRouterArchitecture `json:"architecture"`
	Pricing             *OpenRouterPricing      `json:"pricing"`
	TopProvider         *OpenRouterTopProvider  `json:"top_provider"`
	PerRequestLimits    interface{}             `json:"per_request_limits"`
	SupportedParameters []string                `json:"supported_parameters"`
	CanonicalSlug       string                  `json:"canonical_slug,omitempty"`
	DefaultParameters   map[string]interface{}  `json:"default_parameters,omitempty"`
}

type OpenRouterModelsResponse struct {
	Data []OpenRouterModel `json:"data"`
}

type OpenRouterCompletionsRequest struct {
	Model            string        `json:"model"`
	Prompt           StringOrArray `json:"prompt"`
	MaxTokens        *int32        `json:"max_tokens,omitempty"`
	Temperature      *float32      `json:"temperature,omitempty"`
	TopP             *float32      `json:"top_p,omitempty"`
	TopK             *int32        `json:"top_k,omitempty"`
	FrequencyPenalty *float32      `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float32      `json:"presence_penalty,omitempty"`
	Stream           bool          `json:"stream,omitempty"`
	Stop             StringOrArray `json:"stop,omitempty"`
	Seed             *int32        `json:"seed,omitempty"`
	Logprobs         *int32        `json:"logprobs,omitempty"`
	Echo             bool          `json:"echo,omitempty"`
	Suffix           string        `json:"suffix,omitempty"`
	BestOf           *int32        `json:"best_of,omitempty"`
}

type ChatCompletionResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

type CompletionChoice struct {
	Text         string      `json:"text"`
	Index        int         `json:"index"`
	Logprobs     interface{} `json:"logprobs"`
	FinishReason string      `json:"finish_reason"`
}

type CompletionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []CompletionChoice `json:"choices"`
	Usage   *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

type ChatCompletionChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

type CompletionChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Text         string  `json:"text"`
		Index        int     `json:"index"`
		Logprobs     *int    `json:"logprobs"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}
