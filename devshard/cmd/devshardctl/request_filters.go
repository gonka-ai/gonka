package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"unicode/utf8"
)

type chatRequest struct {
	Model               string `json:"model"`
	Stream              bool   `json:"stream"`
	MaxTokens           uint64 `json:"max_tokens"`
	MaxCompletionTokens uint64 `json:"max_completion_tokens"`
	N                   uint64 `json:"n"`
}

type outputTokenLimits struct {
	DefaultMaxTokens uint64
	MaxTokensCap     uint64
}

type chatRequestFilterError struct {
	status  int
	message string
	wrapped error
}

func (e *chatRequestFilterError) Error() string {
	return e.message
}

// Unwrap exposes the underlying error so callers can use errors.Is/errors.As to identify
// the rejection class even though the outer error carries an HTTP status. This lets pure
// validators in subpackages (filters_parameters/...) define sentinel errors and have them
// remain detectable after passing through the badChatRequest wrapper.
func (e *chatRequestFilterError) Unwrap() error {
	return e.wrapped
}

func chatRequestErrorStatus(err error, fallback int) int {
	var filterErr *chatRequestFilterError
	if errors.As(err, &filterErr) {
		return filterErr.status
	}
	return fallback
}

func badChatRequest(format string, args ...any) error {
	return &chatRequestFilterError{
		status:  http.StatusBadRequest,
		message: fmt.Sprintf(format, args...),
	}
}

// wrapBadChatRequest preserves the error chain (so errors.Is/As works) while attaching the
// HTTP 400 status that the gateway uses for rejection.
func wrapBadChatRequest(err error) error {
	return &chatRequestFilterError{
		status:  http.StatusBadRequest,
		message: err.Error(),
		wrapped: err,
	}
}

type ChatRequestPipeline struct {
	parameters VLLMParameterCatalog
	messages   ChatMessageProcessor
}

func defaultChatRequestPipeline() ChatRequestPipeline {
	return ChatRequestPipeline{
		parameters: defaultParameterCatalog,
		messages:   defaultMessageProcessor,
	}
}

// Normalize runs the catalog (generic + per-model rules) and emits the rewritten body.
// routedModel is the proxy's fallback used when body.model is missing.
func (p ChatRequestPipeline) Normalize(body []byte, adminAuthenticated bool, limits outputTokenLimits, routedModel string) ([]byte, chatRequest, error) {
	ctx, err := newRequestFilterContext(body, adminAuthenticated, limits)
	if err != nil {
		return nil, chatRequest{}, err
	}
	ctx.ResolveRoutedModel(routedModel)
	if err := p.parameters.Apply(RequestFilterStagePreValidation, ctx); err != nil {
		return nil, chatRequest{}, err
	}
	if err := p.messages.NormalizeDocument(&ctx.Document); err != nil {
		return nil, chatRequest{}, err
	}
	if err := p.messages.ValidateDocument(&ctx.Document); err != nil {
		return nil, chatRequest{}, err
	}
	if err := ctx.DecodeRequest(); err != nil {
		return nil, chatRequest{}, err
	}
	p.applyOutputTokenLimits(ctx)
	if err := p.parameters.Apply(RequestFilterStagePostLimits, ctx); err != nil {
		return nil, chatRequest{}, err
	}
	if err := ctx.SyncRequestView(); err != nil {
		return nil, chatRequest{}, err
	}
	updatedBody, err := ctx.Document.Marshal()
	if err != nil {
		return nil, chatRequest{}, err
	}
	return updatedBody, ctx.Request, nil
}

func (p ChatRequestPipeline) applyOutputTokenLimits(ctx *RequestFilterContext) {
	_, hasMaxTokens := ctx.Document.Get("max_tokens")
	_, hasMaxCompletionTokens := ctx.Document.Get("max_completion_tokens")
	limits := normalizedOutputTokenLimits(ctx.OutputLimits)

	switch {
	case hasMaxTokens && hasMaxCompletionTokens:
		maxTokens := capOutputTokens(ctx.Request.MaxTokens, true, ctx.AdminAuthenticated, limits)
		maxCompletionTokens := capOutputTokens(ctx.Request.MaxCompletionTokens, true, ctx.AdminAuthenticated, limits)
		if maxCompletionTokens < maxTokens {
			maxTokens = maxCompletionTokens
		} else {
			maxCompletionTokens = maxTokens
		}
		ctx.Document.Set("max_tokens", maxTokens)
		ctx.Document.Set("max_completion_tokens", maxCompletionTokens)
		ctx.Request.MaxTokens = maxTokens
		ctx.Request.MaxCompletionTokens = maxCompletionTokens
	case hasMaxTokens:
		maxTokens := capOutputTokens(ctx.Request.MaxTokens, true, ctx.AdminAuthenticated, limits)
		ctx.Document.Set("max_tokens", maxTokens)
		ctx.Request.MaxTokens = maxTokens
		ctx.Request.MaxCompletionTokens = 0
	case hasMaxCompletionTokens:
		maxCompletionTokens := capOutputTokens(ctx.Request.MaxCompletionTokens, true, ctx.AdminAuthenticated, limits)
		ctx.Document.Set("max_completion_tokens", maxCompletionTokens)
		ctx.Request.MaxCompletionTokens = maxCompletionTokens
		ctx.Request.MaxTokens = maxCompletionTokens
	default:
		maxTokens := capOutputTokens(0, false, ctx.AdminAuthenticated, limits)
		ctx.Document.Set("max_tokens", maxTokens)
		ctx.Request.MaxTokens = maxTokens
		ctx.Request.MaxCompletionTokens = 0
	}
}

func readLimitedChatRequestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, badChatRequest("request body is required")
	}
	defer r.Body.Close()

	body, err := io.ReadAll(io.LimitReader(r.Body, MaxChatRequestBodySize+1))
	if err != nil {
		return nil, badChatRequest("read body: %v", err)
	}
	if len(body) > MaxChatRequestBodySize {
		logRequestStage(r.Context(), "chat_request_body_too_large", "body_bytes", len(body), "limit_bytes", MaxChatRequestBodySize)
		return nil, &chatRequestFilterError{status: http.StatusRequestEntityTooLarge, message: "request body too large"}
	}
	return body, nil
}

func prepareChatRequestBody(r *http.Request) ([]byte, chatRequest, error) {
	return prepareChatRequestBodyWithTokenLimits(r, defaultOutputTokenLimits(), "")
}

func prepareChatRequestBodyWithTokenLimits(r *http.Request, limits outputTokenLimits, routedModel string) ([]byte, chatRequest, error) {
	body, err := readLimitedChatRequestBody(r)
	if err != nil {
		return nil, chatRequest{}, err
	}
	originalBody := append([]byte(nil), body...)
	logResponseFormatDiagnostics(r.Context(), body)
	updatedBody, req, err := normalizeChatRequestForAuthAndLimits(body, requestHasAdminAuth(r), limits, routedModel)
	if err != nil {
		captureFilterRejectedRequest(r, originalBody, err, chatRequestModel(body), "")
		return nil, chatRequest{}, err
	}
	return updatedBody, req, nil
}

func normalizeChatRequest(body []byte) ([]byte, chatRequest, error) {
	return normalizeChatRequestForAuthAndLimits(body, false, defaultOutputTokenLimits(), "")
}

func normalizeChatRequestForModel(body []byte, routedModel string) ([]byte, chatRequest, error) {
	return normalizeChatRequestForAuthAndLimits(body, false, defaultOutputTokenLimits(), routedModel)
}

func normalizeChatRequestForAuthAndLimits(body []byte, adminAuthenticated bool, limits outputTokenLimits, routedModel string) ([]byte, chatRequest, error) {
	return defaultChatRequestPipeline().Normalize(body, adminAuthenticated, limits, routedModel)
}

func logResponseFormatDiagnostics(ctx context.Context, body []byte) {
	document, err := decodeChatRequestDocument(body)
	if err != nil {
		return
	}
	responseFormat, ok := document.Object("response_format")
	if !ok {
		if document.Has("response_format") {
			logRequestStage(ctx, "response_format_rejected_details", "reason", "not_object")
		}
		return
	}
	rawType, hasType := responseFormat["type"]
	responseFormatType, typeIsString := rawType.(string)
	if !hasType {
		logRequestStage(ctx, "response_format_rejected_details", "reason", "missing_type", "fields", sortedObjectKeys(responseFormat))
		return
	}
	if !typeIsString {
		logRequestStage(ctx, "response_format_rejected_details", "reason", "non_string_type", "fields", sortedObjectKeys(responseFormat))
		return
	}
	switch responseFormatType {
	case "text", "json_object", "json_schema":
		encoded, err := json.Marshal(responseFormat)
		if err != nil {
			logRequestStage(ctx, "response_format_rejected_details", "type", responseFormatType, "marshal_error", err)
			return
		}
		logRequestStage(ctx, "response_format_rejected_details", "type", responseFormatType, "response_format", truncateUTF8String(string(encoded), MaxLoggedResponseFormatBytes))
	default:
		logRequestStage(ctx, "response_format_rejected_details", "reason", "unsupported_type", "type", responseFormatType)
	}
}

func sortedObjectKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func truncateUTF8String(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	if end == 0 {
		end = maxBytes
	}
	return value[:end]
}

func defaultOutputTokenLimits() outputTokenLimits {
	return outputTokenLimits{DefaultMaxTokens: DefaultRequestMaxTokens, MaxTokensCap: RequestMaxTokensCap}
}

func normalizedOutputTokenLimits(limits outputTokenLimits) outputTokenLimits {
	if limits.DefaultMaxTokens == 0 {
		limits.DefaultMaxTokens = DefaultRequestMaxTokens
	}
	if limits.MaxTokensCap == 0 {
		limits.MaxTokensCap = RequestMaxTokensCap
	}
	return limits
}

func capOutputTokens(value uint64, explicitlySet bool, bypassLimit bool, limits outputTokenLimits) uint64 {
	limits = normalizedOutputTokenLimits(limits)
	if value == 0 {
		return limits.DefaultMaxTokens
	}
	if explicitlySet && !bypassLimit && limits.MaxTokensCap > 0 && value > limits.MaxTokensCap {
		return limits.MaxTokensCap
	}
	return value
}

func unsupportedChatParameterMessage(name string) string {
	return fmt.Sprintf("Chat completions parameter %q is currently rejected by the Gonka network. Some non-standard parameters can crash the vLLM engine on Gonka Host MLNodes, so the network rejects parameters that are not explicitly supported (see: https://github.com/gonka-ai/gonka/blob/main/docs/chat-api/README.md). If you do not need this parameter, remove it from the request; if you need it, file a request at https://github.com/gonka-ai/gonka/issues", name)
}
