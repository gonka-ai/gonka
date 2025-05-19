package completionapi

import (
	"encoding/json"
	"strings"
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
	Index    int      `json:"index"`
	Message  *Message `json:"message"`
	Delta    *Delta   `json:"delta"`
	Logprobs struct {
		Content []Logprob `json:"content"`
	} `json:"logprobs"`
	FinishReason string `json:"finish_reason"`
	StopReason   string `json:"stop_reason"`
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
	TotalTokens      uint64 `json:"total_tokens"`
}

const DataPrefix = "data: "

type SerializedStreamedResponse struct {
	Events []string `json:"events"`
}

type StreamedResponse struct {
	Data []Response `json:"data"`
}

func UnmarshalEvent(event string) (*Response, error) {
	if !strings.HasPrefix(event, DataPrefix) {
		return nil, nil
	}

	trimmed := strings.TrimSpace(strings.TrimPrefix(event, DataPrefix))
	if strings.HasPrefix(trimmed, "[DONE]") {
		return nil, nil
	}

	var response Response
	if err := json.Unmarshal([]byte(trimmed), &response); err != nil {
		return nil, err
	}

	return &response, nil
}

type JsonOrStreamedResponse struct {
	JsonResponse     *Response
	StreamedResponse *StreamedResponse
}

func (r JsonOrStreamedResponse) GetModel() (string, error) {
	if r.JsonResponse != nil {
		return r.JsonResponse.Model, nil
	} else if r.StreamedResponse != nil && len(r.StreamedResponse.Data) > 0 {
		return r.StreamedResponse.Data[0].Model, nil
	}
	return "", nil
}

func (r JsonOrStreamedResponse) GetInferenceId() (string, error) {
	if r.JsonResponse != nil {
		return r.JsonResponse.ID, nil
	} else if r.StreamedResponse != nil && len(r.StreamedResponse.Data) > 0 {
		return r.StreamedResponse.Data[0].ID, nil
	}
	return "", nil
}

func (r JsonOrStreamedResponse) GetUsage() (*Usage, error) {
	if r.JsonResponse != nil {
		return &r.JsonResponse.Usage, nil
	} else if r.StreamedResponse != nil && len(r.StreamedResponse.Data) > 0 {
		return &r.StreamedResponse.Data[0].Usage, nil
	}
	return nil, nil
}

func (r JsonOrStreamedResponse) GetBodyBytes() ([]byte, error) {
	if r.JsonResponse != nil {
		return json.Marshal(r.JsonResponse)
	} else if r.StreamedResponse != nil {
		return json.Marshal(r.StreamedResponse)
	}
	return nil, nil
}
