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

type ModelDescriptor struct {
	Object           string   `json:"object,omitempty"`
	ID               string   `json:"id"`
	HuggingFaceID    string   `json:"hugging_face_id,omitempty"`
	Name             string   `json:"name"`
	Created          int64    `json:"created"`
	Description      string   `json:"description,omitempty"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
	Quantization     string   `json:"quantization,omitempty"`
	ContextLength    uint64   `json:"context_length"`
	MaxOutputLength  uint64   `json:"max_output_length"`
}

type ModelsListResponse struct {
	Object string            `json:"object"`
	Data   []ModelDescriptor `json:"data"`
}

type CompletionsRequest struct {
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
