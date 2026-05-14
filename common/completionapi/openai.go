package completionapi

import (
	"encoding/json"
	"sort"
)

type Response struct {
	ID                string   `json:"id"`
	Object            string   `json:"object"`
	Created           int64    `json:"created"`
	Model             string   `json:"model"`
	SystemFingerprint string   `json:"system_fingerprint"`
	Choices           []Choice `json:"choices"`
	Usage             Usage    `json:"usage"`
}

type Choice struct {
	Index        int            `json:"index"`
	Message      *Message       `json:"message"`
	Delta        *Delta         `json:"delta"`
	Text         string         `json:"text,omitempty"`
	Logprobs     ChoiceLogprobs `json:"logprobs"`
	FinishReason string         `json:"finish_reason"`
	StopReason   string         `json:"stop_reason"`
}

type ChoiceLogprobs struct {
	Content []Logprob `json:"content"`
}

type completionsLogprobs struct {
	Tokens        []string             `json:"tokens"`
	TokenLogprobs []*float64           `json:"token_logprobs"`
	TopLogprobs   []map[string]float64 `json:"top_logprobs"`
	Bytes         [][]int              `json:"bytes"`
}

func (l *ChoiceLogprobs) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		l.Content = nil
		return nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// OpenAI chat format:
	// {"content":[{"token":"...","logprob":...,"top_logprobs":[...]}]}
	if contentRaw, ok := raw["content"]; ok {
		if string(contentRaw) == "null" {
			l.Content = nil
			return nil
		}
		var content []Logprob
		if err := json.Unmarshal(contentRaw, &content); err != nil {
			return err
		}
		l.Content = content
		return nil
	}

	// OpenAI completions format:
	// {"tokens":[...],"token_logprobs":[...],"top_logprobs":[{token:logprob,...}],...}
	var completionLogprobs completionsLogprobs
	if err := json.Unmarshal(data, &completionLogprobs); err != nil {
		return err
	}
	if len(completionLogprobs.Tokens) == 0 {
		l.Content = nil
		return nil
	}

	content := make([]Logprob, 0, len(completionLogprobs.Tokens))
	for i, token := range completionLogprobs.Tokens {
		item := Logprob{
			Token: token,
		}

		if i < len(completionLogprobs.TokenLogprobs) && completionLogprobs.TokenLogprobs[i] != nil {
			item.Logprob = *completionLogprobs.TokenLogprobs[i]
		}
		if i < len(completionLogprobs.Bytes) {
			item.Bytes = completionLogprobs.Bytes[i]
		}
		if i < len(completionLogprobs.TopLogprobs) {
			topMap := completionLogprobs.TopLogprobs[i]
			item.TopLogprobs = make([]TopLogprobs, 0, len(topMap))
			for topToken, topLogprob := range topMap {
				item.TopLogprobs = append(item.TopLogprobs, TopLogprobs{
					Token:   topToken,
					Logprob: topLogprob,
				})
			}
			sort.Slice(item.TopLogprobs, func(i, j int) bool {
				if item.TopLogprobs[i].Logprob == item.TopLogprobs[j].Logprob {
					return item.TopLogprobs[i].Token < item.TopLogprobs[j].Token
				}
				return item.TopLogprobs[i].Logprob > item.TopLogprobs[j].Logprob
			})
		}

		content = append(content, item)
	}

	l.Content = content
	return nil
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Delta struct {
	Role    *string `json:"role"`
	Content *string `json:"content"`
}

type TopLogprobs struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Bytes   []int   `json:"bytes"`
}

type Logprob struct {
	Token       string        `json:"token"`
	Logprob     float64       `json:"logprob"`
	Bytes       []int         `json:"bytes"`
	TopLogprobs []TopLogprobs `json:"top_logprobs"`
}

type Usage struct {
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
}

func (u *Usage) IsEmpty() bool {
	return u.PromptTokens == 0 && u.CompletionTokens == 0
}

const DataPrefix = "data: "

type SerializedStreamedResponse struct {
	Events []string `json:"events"`
}

type StreamedResponse struct {
	Data []Response `json:"data"`
}
