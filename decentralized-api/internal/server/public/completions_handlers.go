package public

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"decentralized-api/utils"

	"github.com/labstack/echo/v4"
)

const (
	scannerInitBufSize = 64 * 1024   // 64 KB initial buffer for SSE scanner
	scannerMaxBufSize  = 1024 * 1024 // 1 MB max buffer for SSE scanner
)

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

func transformCompletionsToChatRequestWithPrompt(req *CompletionsRequest, prompt string) map[string]interface{} {
	clone := *req
	clone.Prompt = StringOrArray{prompt}
	return transformCompletionsToChatRequest(&clone)
}

func buildChatBodyFromCompletions(req *CompletionsRequest, prompt string) ([]byte, error) {
	chatReq := transformCompletionsToChatRequestWithPrompt(req, prompt)
	return json.Marshal(chatReq)
}

func (s *Server) executeChatRequest(ctx echo.Context, body []byte, signBodyHash string) (int, []byte, error) {
	rec := httptest.NewRecorder()
	req := ctx.Request().Clone(ctx.Request().Context())

	echoCtx := ctx.Echo().NewContext(req, rec)
	if err := s.postChatWithBody(echoCtx, body, signBodyHash); err != nil {
		echoCtx.Echo().HTTPErrorHandler(err, echoCtx)
	}

	return rec.Code, rec.Body.Bytes(), nil
}

func (s *Server) completionFromChat(ctx echo.Context, body []byte, signBodyHash string) (*CompletionResponse, int, []byte, error) {
	statusCode, respBody, err := s.executeChatRequest(ctx, body, signBodyHash)
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

func (s *Server) postCompletions(ctx echo.Context) error {
	body, err := io.ReadAll(ctx.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to read request body")
	}
	ctx.Request().Body.Close()
	signBodyHash := utils.GenerateSHA256Hash(string(body))

	var completionsReq CompletionsRequest
	if err := json.Unmarshal(body, &completionsReq); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request format")
	}

	if len(completionsReq.Prompt) == 0 || completionsReq.Prompt.First() == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "prompt is required")
	}

	if completionsReq.Stream {
		return s.handleStreamingCompletions(ctx, &completionsReq, signBodyHash)
	}

	if len(completionsReq.Prompt) > 1 {
		return s.handleBatchCompletions(ctx, &completionsReq, signBodyHash)
	}

	return s.handleSingleCompletion(ctx, &completionsReq, signBodyHash)
}

func (s *Server) handleSingleCompletion(ctx echo.Context, completionsReq *CompletionsRequest, signBodyHash string) error {
	newBody, err := buildChatBodyFromCompletions(completionsReq, completionsReq.Prompt.First())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create chat request")
	}

	completionResp, statusCode, respBody, err := s.completionFromChat(ctx, newBody, signBodyHash)
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

func (s *Server) handleBatchCompletions(ctx echo.Context, completionsReq *CompletionsRequest, signBodyHash string) error {
	prompts := completionsReq.Prompt
	results := make([]*CompletionResponse, len(prompts))
	errors := make([]error, len(prompts))
	statusCodes := make([]int, len(prompts))
	responseBodies := make([][]byte, len(prompts))

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

			resp, statusCode, respBody, err := s.completionFromChat(ctx, newBody, signBodyHash)
			if err != nil {
				errors[idx] = err
				return
			}

			if resp == nil {
				statusCodes[idx] = statusCode
				responseBodies[idx] = respBody
				return
			}

			results[idx] = resp
		}(i, prompt)
	}

	wg.Wait()

	for i, err := range errors {
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if results[i] == nil {
			statusCode := statusCodes[i]
			if statusCode == 0 {
				return echo.NewHTTPError(http.StatusInternalServerError, "request failed without status")
			}
			ctx.Response().WriteHeader(statusCode)
			_, _ = ctx.Response().Write(responseBodies[i])
			return nil
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

func (s *Server) handleStreamingCompletions(ctx echo.Context, completionsReq *CompletionsRequest, signBodyHash string) error {
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

	pipeResponseWriter := &pipeResponseWriter{
		header:     make(http.Header),
		pipeWriter: pw,
		statusChan: statusChan,
	}

	echoCtx := ctx.Echo().NewContext(req, pipeResponseWriter)

	go func() {
		defer pw.Close()
		if err := s.postChatWithBody(echoCtx, newBody, signBodyHash); err != nil {
			echoCtx.Echo().HTTPErrorHandler(err, echoCtx)
		}
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
	scanner.Buffer(make([]byte, 0, scannerInitBufSize), scannerMaxBufSize)

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
