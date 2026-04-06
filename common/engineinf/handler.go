package engineinf

import (
	"encoding/json"
	"net/http"
	"strconv"

	"common/queryapi/gen"

	"github.com/labstack/echo/v4"
)

// PostChatCompletions implements gen.ServerInterface for POST /v1/chat/completions.
// Header validation is partially handled by the generated wrapper (required fields);
// timestamp format and freshness are validated here.
// Body size limit is enforced upstream by middleware.BodyLimit.
func (e *Engine) PostChatCompletions(c echo.Context, params gen.PostChatCompletionsParams) error {
	tsNano, err := strconv.ParseInt(params.XTimestamp, 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "X-Timestamp must be a nanosecond Unix timestamp"})
	}
	if err := ValidateTimestamp(tsNano); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	// TODO:
	// Early TA whitelist check - covers both transfer and executor paths:
	// - Transfer requests: TransferAddress = this node's address (set by readRequest)
	// - Executor requests: TransferAddress = forwarding TA's address (from X-Transfer-Address header)
	// if err := s.enforceTransferAgentAccess(chatRequest.TransferAddress); err != nil {
	// 	return err
	// }
	// if chatRequest.AuthKey == "" {
	// 	logging.Warn("Request without authorization", types.Server, "path", ctx.Request().URL.Path)
	// 	return ErrRequestAuth
	// }

	// if chatRequest.OpenAiRequest.Model == "" {
	// 	logging.Warn("Request without model", types.Server, "path", ctx.Request().URL.Path)
	// 	return ErrNoModelSpecified
	// }

	// if err := s.enforceDeveloperAccessGate(ctx.Request().Context(), chatRequest.RequesterAddress); err != nil {
	// 	return err
	// }

	// Read and bind body
	var body gen.ChatCompletionRequest
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to re-encode body"})
	}

	// Branch on inference ID
	if params.XInferenceId != nil && *params.XInferenceId != "" && params.XSeed != nil && *params.XSeed != "" {
		return e.executor.Handle(c, *params.XInferenceId, body.Model, rawBody)
	}
	return e.transfer.Handle(c)
}
