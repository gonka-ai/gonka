package completionapi

import (
	"decentralized-api/logging"
	"decentralized-api/utils"
	"encoding/json"
	"errors"
	"strings"

	"github.com/productscience/inference/x/inference/types"
)

type CompletionResponse interface {
	GetModel() (string, error)
	GetInferenceId() (string, error)
	GetUsage() (*Usage, error)
	GetBodyBytes() ([]byte, error)
	GetHash() (string, error)

	// Validation-related methods
	GetEnforcedStr() (string, error)
	GetEnforcedTokens() (EnforcedTokens, error)
	ExtractLogits() []Logprob
}

type JsonCompletionResponse struct {
	Bytes []byte
	Resp  Response
}

func (r *JsonCompletionResponse) GetModel() (string, error) {
	return r.Resp.Model, nil
}

func (r *JsonCompletionResponse) GetInferenceId() (string, error) {
	return r.Resp.ID, nil
}

func (r *JsonCompletionResponse) GetUsage() (*Usage, error) {
	if r.Resp.Usage.IsEmpty() {
		return nil, errors.New("JsonCompletionResponse: no usage found")
	}
	return &r.Resp.Usage, nil
}

func (r *JsonCompletionResponse) GetBodyBytes() ([]byte, error) {
	return r.Bytes, nil
}

func (r *JsonCompletionResponse) GetHash() (string, error) {
	var builder strings.Builder
	for _, choice := range r.Resp.Choices {
		builder.WriteString(choice.Message.Content)
	}

	return computeHash(builder.String())
}

func (r *JsonCompletionResponse) GetEnforcedStr() (string, error) {
	if len(r.Resp.Choices) == 0 {
		return "", errors.New("JsonResponse has no choices")
	}

	if len(r.Resp.Choices) > 1 {
		// TODO: We should learn how to process/validate multiple options completions
		logging.Warn("More than one choice in a non-steamed inference response, defaulting to first one", types.Validation, "choices", r.Resp.Choices)
	}

	content := r.Resp.Choices[0].Message.Content
	if content == "" {
		logging.Error("Model return empty response", types.Validation, "inference_id", r.Resp.ID)
		return "", errors.New("JsonResponse has no content")
	}

	return content, nil
}

type EnforcedToken struct {
	Token     string   `json:"token"`
	TopTokens []string `json:"top_tokens"`
}

type EnforcedTokens struct {
	Tokens []EnforcedToken `json:"tokens"`
}

func (r *JsonCompletionResponse) GetEnforcedTokens() (EnforcedTokens, error) {
	if len(r.Resp.Choices) == 0 {
		logging.Error("JsonCompletionResponse has no choices for enforced tokens", types.Validation, "inference_id", r.Resp.ID)
		return EnforcedTokens{}, errors.New("JsonCompletionResponse: no choices found")
	}

	if len(r.Resp.Choices) > 1 {
		logging.Warn(
			"More than one choice in a non-streamed inference response for enforced tokens, defaulting to first one",
			types.Validation,
			"inference_id",
			r.Resp.ID,
			"choices",
			r.Resp.Choices,
		)
	}

	var enforcedTokens EnforcedTokens
	for _, c := range r.Resp.Choices[0].Logprobs.Content {
		if c.TopLogprobs == nil {
			continue
		}

		if len(c.TopLogprobs) == 0 {
			logging.Error(
				"Choice has no logprobs content for enforced tokens",
				types.Validation,
				"inference_id",
				r.Resp.ID,
			)
			return EnforcedTokens{}, errors.New("JsonCompletionResponse: choice has no logprobs content")
		}

		var topTokens []string
		for _, topToken := range c.TopLogprobs {
			topTokens = append(topTokens, topToken.Token)
		}
		enforcedTokens.Tokens = append(enforcedTokens.Tokens, EnforcedToken{
			Token:     c.Token,
			TopTokens: topTokens,
		})
	}
	return enforcedTokens, nil
}

func (r *StreamedCompletionResponse) GetEnforcedTokens() (EnforcedTokens, error) {
	if len(r.Resp.Data) == 0 {
		logging.Error("StreamedCompletionResponse has no data for enforced tokens", types.Validation)
		return EnforcedTokens{}, ErrorNoDataAvailableInStreamedResponse
	}

	var enforcedTokens EnforcedTokens
	for _, c := range r.Resp.Data {
		if len(c.Choices) == 0 {
			continue
		}

		if len(c.Choices) > 1 {
			logging.Warn("More than one choice in a streamed inference response for enforced tokens, defaulting to first one", types.Validation, "inference_id", c.ID, "choices", c.Choices)
		}

		for _, choice := range c.Choices {
			if choice.Logprobs.Content == nil {
				continue
			}

			if len(choice.Logprobs.Content) == 0 {
				logging.Error("Choice has no logprobs content for enforced tokens", types.Validation, "inference_id", c.ID)
				return EnforcedTokens{}, errors.New("StreamedCompletionResponse: choice has no logprobs content")
			}

			var topTokens []string
			for _, topToken := range choice.Logprobs.Content[0].TopLogprobs {
				topTokens = append(topTokens, topToken.Token)
			}
			enforcedTokens.Tokens = append(enforcedTokens.Tokens, EnforcedToken{
				Token:     choice.Logprobs.Content[0].Token,
				TopTokens: topTokens,
			})
		}
	}

	if len(enforcedTokens.Tokens) == 0 {
		logging.Error("No enforced tokens found in streamed response", types.Validation)
		return EnforcedTokens{}, errors.New("StreamedCompletionResponse: no enforced tokens found")
	}

	return enforcedTokens, nil
}

func computeHash(content string) (string, error) {
	if content == "" {
		return "", errors.New("CompletionResponse: can't compute hash, empty content")
	}

	hash := utils.GenerateSHA256Hash(content)
	return hash, nil
}

type StreamedCompletionResponse struct {
	Lines []string
	Resp  StreamedResponse
}

var ErrorNoDataAvailableInStreamedResponse = errors.New("no data available in streamed response")

func (r *StreamedCompletionResponse) GetModel() (string, error) {
	if len(r.Resp.Data) > 0 {
		return r.Resp.Data[0].Model, nil
	} else {
		return "", ErrorNoDataAvailableInStreamedResponse
	}
}

func (r *StreamedCompletionResponse) GetInferenceId() (string, error) {
	if len(r.Resp.Data) > 0 {
		return r.Resp.Data[0].ID, nil
	} else {
		return "", ErrorNoDataAvailableInStreamedResponse
	}
}

func (r *StreamedCompletionResponse) GetUsage() (*Usage, error) {
	backupLength := 0
	if len(r.Resp.Data) > 0 {
		for _, d := range r.Resp.Data {
			if len(d.Choices) != 0 {
				backupLength += len(d.Choices[0].Logprobs.Content)
			}
			if d.Usage.IsEmpty() {
				continue
			}
			return &d.Usage, nil
		}
		usage := &Usage{
			PromptTokens:     0,
			CompletionTokens: uint64(backupLength),
		}
		return usage, nil
	} else {
		return nil, ErrorNoDataAvailableInStreamedResponse
	}
}

func (r *StreamedCompletionResponse) GetBodyBytes() ([]byte, error) {
	serialized := SerializedStreamedResponse{
		Events: r.Lines,
	}
	return json.Marshal(&serialized)
}

func (r *StreamedCompletionResponse) GetHash() (string, error) {
	var builder strings.Builder
	for _, choice := range r.Resp.Data {
		for _, c := range choice.Choices {
			delta := c.Delta.Content
			if delta != nil {
				builder.WriteString(*delta)
			}
		}
	}

	return computeHash(builder.String())
}

func (r *StreamedCompletionResponse) GetEnforcedStr() (string, error) {
	var id = ""
	var stringBuilder strings.Builder
	for _, event := range r.Resp.Data {
		id = event.ID
		if len(event.Choices) == 0 {
			continue
		}

		if len(event.Choices) > 1 {
			// TODO: We should learn how to process/validate multiple options completions
			logging.Warn("More than one choice in a streamed inference response, defaulting to first one", types.Validation, "inferenceId", event.ID, "choices", event.Choices)
		}

		content := event.Choices[0].Delta.Content
		if content != nil {
			stringBuilder.WriteString(*content)
		}
	}

	responseString := stringBuilder.String()
	if responseString == "" {
		logging.Error("Model return empty response", types.Validation, "inference_id", id)
		return "", errors.New("StreamedResponse has no content")
	}

	return responseString, nil
}

func (r *JsonCompletionResponse) ExtractLogits() []Logprob {
	var logits []Logprob
	// Concatenate all logrpobs
	for _, c := range r.Resp.Choices {
		logits = append(logits, c.Logprobs.Content...)
	}
	return logits
}

func (r *StreamedCompletionResponse) ExtractLogits() []Logprob {
	var logits []Logprob
	for _, r := range r.Resp.Data {
		for _, c := range r.Choices {
			logits = append(logits, c.Logprobs.Content...)
		}
	}
	return logits
}

func NewCompletionResponseFromBytes(bytes []byte) (CompletionResponse, error) {
	var response Response
	if err := json.Unmarshal(bytes, &response); err != nil {
		logging.Error("Failed to unmarshal json response into completionapi.Response", types.Inferences, "responseString", string(bytes), "err", err)
		return nil, err
	}

	return &JsonCompletionResponse{
		Bytes: bytes,
		Resp:  response,
	}, nil
}

func NewCompletionResponseFromLines(lines []string) (CompletionResponse, error) {
	data := make([]Response, 0)
	for _, event := range lines {
		trimmedEvent := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(event), "data:"))
		if trimmedEvent == "[DONE]" || trimmedEvent == "" {
			// TODO: should we make sure somehow that [DONE] was indeed received?
			continue
		}

		var response Response
		if err := json.Unmarshal([]byte(trimmedEvent), &response); err != nil {
			logging.Error("Failed to unmarshal streamed response line into completionapi.Response", types.Inferences, "event", event, "trimmedEvent", trimmedEvent, "err", err)
			return nil, err
		}
		data = append(data, response)
	}
	streamedResponse := StreamedResponse{
		Data: data,
	}
	return &StreamedCompletionResponse{
		Lines: lines,
		Resp:  streamedResponse,
	}, nil
}

func NewCompletionResponseFromLinesFromResponsePayload(payload string) (CompletionResponse, error) {
	var genericMap map[string]interface{}
	bytes := []byte(payload)
	if err := json.Unmarshal(bytes, &genericMap); err != nil {
		logging.Error("Failed to unmarshal response payload into var genericMap map[string]interface{}", types.Inferences, "err", err)
		return nil, err
	}

	if _, exists := genericMap["events"]; exists {
		logging.Info("Unmarshaling streamed response", types.Inferences)

		var serialized SerializedStreamedResponse
		if err := json.Unmarshal(bytes, &serialized); err != nil {
			logging.Error("Failed to unmarshal response payload into SerializedStreamedResponse", types.Inferences, "err", err)
			return nil, err
		}

		return NewCompletionResponseFromLines(serialized.Events)
	} else {
		return NewCompletionResponseFromBytes(bytes)
	}
}
