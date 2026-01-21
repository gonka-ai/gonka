package public

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

var (
	openRouterSupportedParameters = []string{
		"temperature", "top_p", "top_k", "max_tokens",
		"frequency_penalty", "presence_penalty", "stop", "seed", "stream",
	}

	openRouterDefaultPricing = &OpenRouterPricing{
		Prompt:     "0",
		Completion: "0",
		Request:    "0",
		Image:      "0",
	}
)

func (s *Server) getModelsOpenRouter(ctx echo.Context) error {
	queryClient := s.recorder.NewInferenceQueryClient()
	context := s.recorder.GetContext()

	currentEpoch, err := queryClient.CurrentEpochGroupData(context, &types.QueryCurrentEpochGroupDataRequest{})
	if err != nil {
		return err
	}

	var models []OpenRouterModel
	parentEpochData := currentEpoch.GetEpochGroupData()

	for _, modelId := range parentEpochData.SubGroupModels {
		req := &types.QueryGetEpochGroupDataRequest{
			EpochIndex: parentEpochData.EpochIndex,
			ModelId:    modelId,
		}
		modelEpochData, err := queryClient.EpochGroupData(context, req)
		if err != nil {
			continue
		}

		if modelEpochData.EpochGroupData.ModelSnapshot != nil {
			m := modelEpochData.EpochGroupData.ModelSnapshot
			models = append(models, OpenRouterModel{
				ID:            m.Id,
				Name:          m.Id,
				Created:       0,
				ContextLength: m.ContextWindow,
				Architecture: &OpenRouterArchitecture{
					Modality:         "text->text",
					InputModalities:  []string{"text"},
					OutputModalities: []string{"text"},
					Tokenizer:        "Unknown",
				},
				Pricing: openRouterDefaultPricing,
				TopProvider: &OpenRouterTopProvider{
					ContextLength:       m.ContextWindow,
					MaxCompletionTokens: m.ContextWindow,
					IsModerated:         false,
				},
				PerRequestLimits:    nil,
				SupportedParameters: openRouterSupportedParameters,
			})
		}
	}

	return ctx.JSON(http.StatusOK, OpenRouterModelsResponse{
		Data: models,
	})
}

func (s *Server) postChatOpenRouter(ctx echo.Context) error {
	body, err := io.ReadAll(ctx.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to read request body")
	}
	ctx.Request().Body.Close()

	var rawReq map[string]interface{}
	if err := json.Unmarshal(body, &rawReq); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request format")
	}

	if _, hasMessages := rawReq["messages"]; !hasMessages {
		promptRaw, hasPrompt := rawReq["prompt"]
		if !hasPrompt {
			return echo.NewHTTPError(http.StatusBadRequest, "messages or prompt is required")
		}
		var prompt StringOrArray
		promptBytes, _ := json.Marshal(promptRaw)
		if err := json.Unmarshal(promptBytes, &prompt); err != nil || len(prompt) == 0 || len(prompt) > 1 || prompt.First() == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "messages or prompt is required")
		}
		rawReq["messages"] = []map[string]string{
			{"role": "user", "content": prompt.First()},
		}
		delete(rawReq, "prompt")
	}

	newBody, err := json.Marshal(rawReq)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to process request")
	}

	ctx.Request().Body = io.NopCloser(bytes.NewReader(newBody))
	ctx.Request().ContentLength = int64(len(newBody))

	return s.postChat(ctx)
}

func transformCompletionsToChatRequest(req *OpenRouterCompletionsRequest) map[string]interface{} {
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

func transformCompletionsToChatRequestWithPrompt(req *OpenRouterCompletionsRequest, prompt string) map[string]interface{} {
	clone := *req
	clone.Prompt = StringOrArray{prompt}
	return transformCompletionsToChatRequest(&clone)
}

func buildChatBodyFromCompletions(req *OpenRouterCompletionsRequest, prompt string) ([]byte, error) {
	chatReq := transformCompletionsToChatRequestWithPrompt(req, prompt)
	return json.Marshal(chatReq)
}

func (s *Server) executeChatRequest(ctx echo.Context, body []byte) (int, []byte, error) {
	rec := httptest.NewRecorder()
	req := ctx.Request().Clone(ctx.Request().Context())
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))

	echoCtx := ctx.Echo().NewContext(req, rec)
	if err := s.postChat(echoCtx); err != nil {
		return 0, nil, err
	}

	return rec.Code, rec.Body.Bytes(), nil
}

func (s *Server) completionFromChat(ctx echo.Context, body []byte) (*CompletionResponse, int, []byte, error) {
	statusCode, respBody, err := s.executeChatRequest(ctx, body)
	if err != nil {
		return nil, 0, nil, err
	}
	if statusCode != http.StatusOK {
		return nil, statusCode, respBody, nil
	}
	completionResp, err := transformChatToCompletionResponse(respBody)
	if err != nil {
		return nil, statusCode, respBody, nil
	}
	return completionResp, statusCode, respBody, nil
}

func (s *Server) postCompletionsOpenRouter(ctx echo.Context) error {
	body, err := io.ReadAll(ctx.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to read request body")
	}
	ctx.Request().Body.Close()

	var completionsReq OpenRouterCompletionsRequest
	if err := json.Unmarshal(body, &completionsReq); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request format")
	}

	if len(completionsReq.Prompt) == 0 || completionsReq.Prompt.First() == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "prompt is required")
	}

	if completionsReq.Stream {
		return s.handleStreamingCompletions(ctx, &completionsReq)
	}

	if len(completionsReq.Prompt) > 1 {
		return s.handleBatchCompletions(ctx, &completionsReq)
	}

	return s.handleSingleCompletion(ctx, &completionsReq)
}

func (s *Server) handleSingleCompletion(ctx echo.Context, completionsReq *OpenRouterCompletionsRequest) error {
	newBody, err := buildChatBodyFromCompletions(completionsReq, completionsReq.Prompt.First())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create chat request")
	}

	completionResp, statusCode, respBody, err := s.completionFromChat(ctx, newBody)
	if err != nil {
		return err
	}
	if completionResp == nil {
		ctx.Response().WriteHeader(statusCode)
		_, _ = ctx.Response().Write(respBody)
		return nil
	}

	return ctx.JSON(http.StatusOK, completionResp)
}

func (s *Server) handleBatchCompletions(ctx echo.Context, completionsReq *OpenRouterCompletionsRequest) error {
	prompts := completionsReq.Prompt
	results := make([]*CompletionResponse, len(prompts))
	errors := make([]error, len(prompts))

	var wg sync.WaitGroup
	for i, prompt := range prompts {
		wg.Add(1)
		go func(idx int, p string) {
			defer wg.Done()

			newBody, err := buildChatBodyFromCompletions(completionsReq, p)
			if err != nil {
				errors[idx] = err
				return
			}

			resp, statusCode, _, err := s.completionFromChat(ctx, newBody)
			if err != nil {
				errors[idx] = err
				return
			}

			if resp == nil {
				errors[idx] = fmt.Errorf("request failed with status %d", statusCode)
				return
			}

			results[idx] = resp
		}(i, prompt)
	}

	wg.Wait()

	for _, err := range errors {
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
	}

	return ctx.JSON(http.StatusOK, mergeBatchCompletionResponses(results))
}

func mergeBatchCompletionResponses(results []*CompletionResponse) *CompletionResponse {
	if len(results) == 0 {
		return nil
	}

	merged := &CompletionResponse{
		ID:      results[0].ID,
		Object:  "text_completion",
		Created: results[0].Created,
		Model:   results[0].Model,
		Choices: make([]CompletionChoice, 0),
	}

	var totalPromptTokens, totalCompletionTokens, totalTokens int
	hasUsage := false

	for i, r := range results {
		if r == nil {
			continue
		}
		for _, c := range r.Choices {
			c.Index = i
			merged.Choices = append(merged.Choices, c)
		}
		if r.Usage != nil {
			hasUsage = true
			totalPromptTokens += r.Usage.PromptTokens
			totalCompletionTokens += r.Usage.CompletionTokens
			totalTokens += r.Usage.TotalTokens
		}
	}

	if hasUsage {
		merged.Usage = &struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}{
			PromptTokens:     totalPromptTokens,
			CompletionTokens: totalCompletionTokens,
			TotalTokens:      totalTokens,
		}
	}

	return merged
}

func (s *Server) handleStreamingCompletions(ctx echo.Context, completionsReq *OpenRouterCompletionsRequest) error {
	if len(completionsReq.Prompt) > 1 {
		return echo.NewHTTPError(http.StatusBadRequest, "streaming with batch prompts is not supported")
	}

	newBody, err := buildChatBodyFromCompletions(completionsReq, completionsReq.Prompt.First())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create chat request")
	}

	pr, pw := io.Pipe()
	statusChan := make(chan int, 1)

	req := ctx.Request().Clone(ctx.Request().Context())
	req.Body = io.NopCloser(bytes.NewReader(newBody))
	req.ContentLength = int64(len(newBody))

	pipeResponseWriter := &pipeResponseWriter{
		header:     make(http.Header),
		pipeWriter: pw,
		statusChan: statusChan,
	}

	echoCtx := ctx.Echo().NewContext(req, pipeResponseWriter)

	go func() {
		defer pw.Close()
		_ = s.postChat(echoCtx)
	}()

	statusCode := <-statusChan

	if statusCode != http.StatusOK {
		body, _ := io.ReadAll(pr)
		ctx.Response().WriteHeader(statusCode)
		_, _ = ctx.Response().Write(body)
		return nil
	}

	ctx.Response().Header().Set("Content-Type", "text/event-stream")
	ctx.Response().Header().Set("Cache-Control", "no-cache")
	ctx.Response().Header().Set("Connection", "keep-alive")
	ctx.Response().WriteHeader(http.StatusOK)

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			fmt.Fprintln(ctx.Response().Writer, "")
			ctx.Response().Flush()
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")

			if data == "[DONE]" {
				fmt.Fprintln(ctx.Response().Writer, "data: [DONE]")
				ctx.Response().Flush()
				continue
			}

			transformed, err := transformChatChunkToCompletionChunk(data)
			if err != nil {
				fmt.Fprintln(ctx.Response().Writer, line)
			} else {
				fmt.Fprintf(ctx.Response().Writer, "data: %s\n", transformed)
			}
			ctx.Response().Flush()
		} else {
			fmt.Fprintln(ctx.Response().Writer, line)
			ctx.Response().Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(ctx.Response().Writer, "data: {\"error\": \"stream error: %s\"}\n", err.Error())
		ctx.Response().Flush()
	}

	return nil
}

type pipeResponseWriter struct {
	header      http.Header
	statusCode  int
	pipeWriter  *io.PipeWriter
	statusChan  chan int
	headersSent bool
}

func (w *pipeResponseWriter) Header() http.Header {
	return w.header
}

func (w *pipeResponseWriter) Write(data []byte) (int, error) {
	if !w.headersSent {
		w.sendStatus(http.StatusOK)
	}
	return w.pipeWriter.Write(data)
}

func (w *pipeResponseWriter) WriteHeader(statusCode int) {
	w.sendStatus(statusCode)
}

func (w *pipeResponseWriter) sendStatus(statusCode int) {
	if !w.headersSent {
		w.statusCode = statusCode
		w.headersSent = true
		if w.statusChan != nil {
			w.statusChan <- statusCode
		}
	}
}

func (w *pipeResponseWriter) Flush() {}

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
