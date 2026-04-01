package public

import (
	"encoding/json"
	"net/http"
	"strings"

	"decentralized-api/utils"

	"github.com/labstack/echo/v4"
)

const completionsPath = "/v1/completions"

func transformCompletionsToChatRequest(req *CompletionsRequest) map[string]interface{} {
	chatReq := map[string]interface{}{
		"model": req.Model,
		"messages": []map[string]string{
			{"role": "user", "content": req.Prompt.First()},
		},
	}

	if req.MaxTokens != nil {
		chatReq["max_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		chatReq["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		chatReq["top_p"] = *req.TopP
	}
	if req.TopK != nil {
		chatReq["top_k"] = *req.TopK
	}
	if req.FrequencyPenalty != nil {
		chatReq["frequency_penalty"] = *req.FrequencyPenalty
	}
	if req.PresencePenalty != nil {
		chatReq["presence_penalty"] = *req.PresencePenalty
	}
	if req.Stream {
		chatReq["stream"] = req.Stream
	}
	if len(req.Stop) > 0 {
		chatReq["stop"] = req.Stop
	}
	if req.Seed != nil {
		chatReq["seed"] = *req.Seed
	}

	return chatReq
}

func (s *Server) postCompletions(ctx echo.Context) error {
	body, err := readRequestBody(ctx.Request(), ctx.Response().Writer)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to read request body")
	}

	var completionsReq CompletionsRequest
	if err := json.Unmarshal(body, &completionsReq); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request format")
	}

	if strings.TrimSpace(completionsReq.Model) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "model is required")
	}
	if len(completionsReq.Prompt) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "prompt is required")
	}
	if len(completionsReq.Prompt) > 1 {
		return echo.NewHTTPError(http.StatusBadRequest, "batch prompts are not supported")
	}
	for _, prompt := range completionsReq.Prompt {
		if strings.TrimSpace(prompt) == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "prompt is required")
		}
	}

	// Use the common request pipeline without local proxy recursion.
	// Signature is always validated against the original /v1/completions body.
	signBodyHash := utils.GenerateSHA256Hash(string(body))
	return s.postChatWithBody(ctx, body, signBodyHash, completionsPath, body)
}

func tryBuildOpenAiRequestFromCompletionsBody(body []byte) (OpenAiRequest, bool) {
	var completionsReq CompletionsRequest
	if err := json.Unmarshal(body, &completionsReq); err != nil {
		return OpenAiRequest{}, false
	}
	if strings.TrimSpace(completionsReq.Model) == "" || len(completionsReq.Prompt) != 1 {
		return OpenAiRequest{}, false
	}

	prompt := strings.TrimSpace(completionsReq.Prompt.First())
	if prompt == "" {
		return OpenAiRequest{}, false
	}

	content, err := json.Marshal(prompt)
	if err != nil {
		return OpenAiRequest{}, false
	}

	var maxTokens int32
	if completionsReq.MaxTokens != nil {
		maxTokens = *completionsReq.MaxTokens
	}
	var seed int32
	if completionsReq.Seed != nil {
		seed = *completionsReq.Seed
	}

	return OpenAiRequest{
		Model:               completionsReq.Model,
		Seed:                seed,
		MaxTokens:           maxTokens,
		MaxCompletionTokens: maxTokens,
		Messages: []Message{{
			Role:    "user",
			Content: content,
		}},
	}, true
}

func transformChatChunkToCompletionChunk(chatChunkJSON string) (string, error) {
	var chatChunk ChatCompletionChunk
	if err := json.Unmarshal([]byte(chatChunkJSON), &chatChunk); err != nil {
		return "", err
	}

	completionChunk := CompletionChunk{
		ID:      chatChunk.ID,
		Object:  "text_completion",
		Created: chatChunk.Created,
		Model:   chatChunk.Model,
		Usage:   chatChunk.Usage,
		Choices: make([]struct {
			Text         string  `json:"text"`
			Index        int     `json:"index"`
			Logprobs     *int    `json:"logprobs"`
			FinishReason *string `json:"finish_reason"`
		}, 0),
	}

	for _, c := range chatChunk.Choices {
		completionChunk.Choices = append(completionChunk.Choices, struct {
			Text         string  `json:"text"`
			Index        int     `json:"index"`
			Logprobs     *int    `json:"logprobs"`
			FinishReason *string `json:"finish_reason"`
		}{
			Text:         c.Delta.Content,
			Index:        c.Index,
			Logprobs:     nil,
			FinishReason: c.FinishReason,
		})
	}

	result, err := json.Marshal(completionChunk)
	if err != nil {
		return "", err
	}

	return string(result), nil
}

func transformChatToCompletionResponse(chatResponseBody []byte) (*CompletionResponse, error) {
	var chatResp ChatCompletionResponse
	if err := json.Unmarshal(chatResponseBody, &chatResp); err != nil {
		return nil, err
	}

	choices := make([]CompletionChoice, len(chatResp.Choices))
	for i, c := range chatResp.Choices {
		choices[i] = CompletionChoice{
			Text:         c.Message.Content,
			Index:        c.Index,
			Logprobs:     nil,
			FinishReason: c.FinishReason,
		}
	}

	return &CompletionResponse{
		ID:      chatResp.ID,
		Object:  "text_completion",
		Created: chatResp.Created,
		Model:   chatResp.Model,
		Choices: choices,
		Usage:   chatResp.Usage,
	}, nil
}
