package completionapi

import (
	"decentralized-api/logging"
	"decentralized-api/utils"
	"encoding/json"
	"errors"
	"github.com/productscience/inference/x/inference/types"
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

func (u *Usage) IsEmpty() bool {
	return u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0
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
	JsonResponse *struct {
		Bytes []byte
		Resp  Response
	}
	StreamedResponse *struct {
		Lines []string
		Resp  StreamedResponse
	}
}

func NewJsonOrStreamedResponseFromBytes(bytes []byte) (*JsonOrStreamedResponse, error) {
	var response Response
	if err := json.Unmarshal(bytes, &response); err != nil {
		logging.Error("Failed to unmarshall json response into completionapi.Response", types.Inferences, "responseString", string(bytes), "err", err)
		return nil, err
	}

	return &JsonOrStreamedResponse{
		JsonResponse: &struct {
			Bytes []byte
			Resp  Response
		}{
			Bytes: bytes,
			Resp:  response,
		},
	}, nil
}

func NewJsonOrStreamedResponseFromLines(lines []string) (*JsonOrStreamedResponse, error) {
	data := make([]Response, 0)
	for _, event := range lines {
		trimmedEvent := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(event), "data:"))
		if trimmedEvent == "[DONE]" || trimmedEvent == "" {
			// TODO: should we make sure somehow that [DONE] was indeed received?
			continue
		}

		var response Response
		if err := json.Unmarshal([]byte(trimmedEvent), &response); err != nil {
			logging.Error("Failed to unmarshall streamed response line into completionapi.Response", types.Inferences, "event", event, "trimmedEvent", trimmedEvent, "err", err)
			return nil, err
		}
		data = append(data, response)
	}
	streamedResponse := StreamedResponse{
		Data: data,
	}
	return &JsonOrStreamedResponse{
		StreamedResponse: &struct {
			Lines []string
			Resp  StreamedResponse
		}{
			Lines: lines,
			Resp:  streamedResponse,
		},
	}, nil
}

func NewJsonOrStreamedResponseFromLinesFromResponsePayload(payload string) (*JsonOrStreamedResponse, error) {
	var genericMap map[string]interface{}
	bytes := []byte(payload)
	if err := json.Unmarshal(bytes, &genericMap); err != nil {
		logging.Error("Failed to unmarshal response payload into var genericMap map[string]interface{}", types.Inferences, "err", err)
		return nil, err
	}

	if _, exists := genericMap["events"]; exists {
		logging.Info("Unmarshalling streamed response", types.Inferences)

		var serialized SerializedStreamedResponse
		if err := json.Unmarshal(bytes, &serialized); err != nil {
			logging.Error("Failed to unmarshal response payload into SerializedStreamedResponse", types.Inferences, "err", err)
			return nil, err
		}

		return NewJsonOrStreamedResponseFromLines(serialized.Events)
	} else {
		return NewJsonOrStreamedResponseFromBytes(bytes)
	}
}

var JsonAndStreamedResponseAreEmtpy = errors.New("JsonOrStreamedResponse: both jsonResponse and streamedResponse are empty")

func (r JsonOrStreamedResponse) GetModel() (string, error) {
	if r.JsonResponse != nil {
		return r.JsonResponse.Resp.Model, nil
	} else if r.StreamedResponse != nil && len(r.StreamedResponse.Resp.Data) > 0 {
		return r.StreamedResponse.Resp.Data[0].Model, nil
	}
	return "", JsonAndStreamedResponseAreEmtpy
}

func (r JsonOrStreamedResponse) GetInferenceId() (string, error) {
	if r.JsonResponse != nil {
		return r.JsonResponse.Resp.ID, nil
	} else if r.StreamedResponse != nil && len(r.StreamedResponse.Resp.Data) > 0 {
		return r.StreamedResponse.Resp.Data[0].ID, nil
	}
	return "", JsonAndStreamedResponseAreEmtpy
}

func (r JsonOrStreamedResponse) GetUsage() (*Usage, error) {
	if r.JsonResponse != nil {
		return &r.JsonResponse.Resp.Usage, nil
	} else if r.StreamedResponse != nil && len(r.StreamedResponse.Resp.Data) > 0 {
		for _, d := range r.StreamedResponse.Resp.Data {
			if d.Usage.IsEmpty() {
				continue
			}
			return &d.Usage, nil
		}
		return nil, errors.New("JsonOrStreamedResponse: no usage found in streamed response")
	}
	return nil, JsonAndStreamedResponseAreEmtpy
}

func (r JsonOrStreamedResponse) GetBodyBytes() ([]byte, error) {
	if r.JsonResponse != nil {
		return r.JsonResponse.Bytes, nil
	} else if r.StreamedResponse != nil {
		serialized := SerializedStreamedResponse{
			Events: r.StreamedResponse.Lines,
		}
		return json.Marshal(&serialized)
	}
	return nil, JsonAndStreamedResponseAreEmtpy
}

func (r JsonOrStreamedResponse) GetHash() (string, error) {
	var builder strings.Builder
	if r.JsonResponse != nil {
		for _, choice := range r.JsonResponse.Resp.Choices {
			builder.WriteString(choice.Message.Content)
		}

	} else if r.StreamedResponse != nil {
		for _, choice := range r.StreamedResponse.Resp.Data {
			for _, c := range choice.Choices {
				delta := c.Delta.Content
				if delta != nil {
					builder.WriteString(*delta)
				}
			}
		}
	} else {
		return "", JsonAndStreamedResponseAreEmtpy
	}

	content := builder.String()
	if content == "" {
		return "", errors.New("JsonOrStreamedResponse: empty content")
	}

	hash := utils.GenerateSHA256Hash(content)
	return hash, nil
}
