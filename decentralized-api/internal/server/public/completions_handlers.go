package public

import (
	"encoding/json"
	"net/http"
	"strings"

	"decentralized-api/observability"
	"decentralized-api/utils"

	"github.com/labstack/echo/v4"
)

const completionsPath = "/v1/completions"

func (s *Server) postCompletions(ctx echo.Context) (err error) {
	req := ctx.Request()
	traceCtx := observability.Inference.ExtractRequestContext(req.Context(), req.Header)
	traceCtx, op := observability.Inference.StartRequest(traceCtx, req.Method)
	ctx.SetRequest(req.WithContext(traceCtx))
	defer func() {
		observability.Inference.SetHTTPStatus(op, ctx.Response().Status)
		op.FinishErr(&err)
	}()

	body, err := readRequestBody(ctx.Request(), ctx.Response().Writer)
	if err != nil {
		return mapRequestBodyReadError(err)
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

	rawPrompt := completionsReq.Prompt.First()
	if strings.TrimSpace(rawPrompt) == "" {
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
	promptText := rawPrompt

	return OpenAiRequest{
		Model:               completionsReq.Model,
		Seed:                seed,
		MaxTokens:           maxTokens,
		MaxCompletionTokens: maxTokens,
		Messages: []Message{{
			Role:    "user",
			Content: MessageContent{Text: &promptText},
		}},
	}, true
}
