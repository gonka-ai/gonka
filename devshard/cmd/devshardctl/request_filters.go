package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const MaxChatRequestBodySize = 10 * 1024 * 1024
const MaxChatRequestChoices = 5
const MaxTemperature = 2.0
const kimiK26ModelID = "moonshotai/Kimi-K2.6"

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
}

func (e *chatRequestFilterError) Error() string {
	return e.message
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
		return nil, &chatRequestFilterError{
			status:  http.StatusRequestEntityTooLarge,
			message: "request body too large",
		}
	}
	return body, nil
}

func prepareChatRequestBody(r *http.Request) ([]byte, chatRequest, error) {
	return prepareChatRequestBodyWithTokenLimits(r, defaultOutputTokenLimits())
}

func prepareChatRequestBodyWithTokenLimits(r *http.Request, limits outputTokenLimits) ([]byte, chatRequest, error) {
	body, err := readLimitedChatRequestBody(r)
	if err != nil {
		return nil, chatRequest{}, err
	}
	body, err = normalizeContent(body)
	if err != nil {
		return nil, chatRequest{}, err
	}
	return normalizeChatRequestForAuthAndLimits(body, requestHasAdminAuth(r), limits)
}

func normalizeChatRequest(body []byte) ([]byte, chatRequest, error) {
	return normalizeChatRequestForAuth(body, false)
}

func normalizeChatRequestForAuth(body []byte, adminAuthenticated bool) ([]byte, chatRequest, error) {
	return normalizeChatRequestForAuthAndLimits(body, adminAuthenticated, defaultOutputTokenLimits())
}

func normalizeChatRequestForAuthAndLimits(body []byte, adminAuthenticated bool, limits outputTokenLimits) ([]byte, chatRequest, error) {
	limits = normalizedOutputTokenLimits(limits)
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, chatRequest{}, badChatRequest("parse request: %v", err)
	}
	stripUnsupportedChatRequestParameters(raw)
	if err := validateUnsupportedChatRequestFields(raw); err != nil {
		return nil, chatRequest{}, err
	}
	if err := validateOpenAICompatChatMessages(raw); err != nil {
		return nil, chatRequest{}, err
	}

	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, chatRequest{}, badChatRequest("parse request: %v", err)
	}

	_, hasMaxTokens := raw["max_tokens"]
	_, hasMaxCompletionTokens := raw["max_completion_tokens"]
	switch {
	case hasMaxTokens && hasMaxCompletionTokens:
		req.MaxTokens = capOutputTokens(req.MaxTokens, true, adminAuthenticated, limits)
		req.MaxCompletionTokens = capOutputTokens(req.MaxCompletionTokens, true, adminAuthenticated, limits)
		if req.MaxCompletionTokens < req.MaxTokens {
			req.MaxTokens = req.MaxCompletionTokens
		} else {
			req.MaxCompletionTokens = req.MaxTokens
		}
		raw["max_tokens"] = req.MaxTokens
		raw["max_completion_tokens"] = req.MaxCompletionTokens
	case hasMaxTokens:
		req.MaxTokens = capOutputTokens(req.MaxTokens, true, adminAuthenticated, limits)
		raw["max_tokens"] = req.MaxTokens
	case hasMaxCompletionTokens:
		req.MaxCompletionTokens = capOutputTokens(req.MaxCompletionTokens, true, adminAuthenticated, limits)
		req.MaxTokens = req.MaxCompletionTokens
		raw["max_completion_tokens"] = req.MaxCompletionTokens
	default:
		req.MaxTokens = capOutputTokens(0, false, adminAuthenticated, limits)
		raw["max_tokens"] = req.MaxTokens
	}
	stripMinTokensAboveMax(raw, req.MaxTokens)
	stripTemperatureAboveMax(raw)
	forceValidationLogprobs(raw)
	if _, hasN := raw["n"]; hasN {
		req.N = capChatRequestChoices(req.N)
		raw["n"] = req.N
	}
	stripUnsupportedChatRequestParameters(raw)

	updatedBody, err := json.Marshal(raw)
	if err != nil {
		return nil, chatRequest{}, badChatRequest("marshal request: %v", err)
	}
	return updatedBody, req, nil
}

func applyKimiRequestOverrides(body []byte, req chatRequest, model string) ([]byte, chatRequest, error) {
	if model != kimiK26ModelID {
		return body, req, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, chatRequest{}, badChatRequest("parse request: %v", err)
	}
	// raw["stream"] = true
	// raw["tool_choice"] = "none"
	translateKimiThinkingForVLLM(raw)
	stripUnsupportedChatRequestParameters(raw)
	// req.Stream = true

	updatedBody, err := json.Marshal(raw)
	if err != nil {
		return nil, chatRequest{}, badChatRequest("marshal request: %v", err)
	}
	return updatedBody, req, nil
}

func translateKimiThinkingForVLLM(request map[string]any) {
	enabled, ok := moonshotThinkingEnabled(request["thinking"])
	if !ok {
		return
	}

	chatTemplateKwargs, _ := request["chat_template_kwargs"].(map[string]any)
	if chatTemplateKwargs == nil {
		chatTemplateKwargs = map[string]any{}
		request["chat_template_kwargs"] = chatTemplateKwargs
	}
	if _, exists := chatTemplateKwargs["thinking"]; exists {
		return
	}
	chatTemplateKwargs["thinking"] = enabled
}

func moonshotThinkingEnabled(value any) (bool, bool) {
	thinking, ok := value.(map[string]any)
	if !ok {
		return false, false
	}
	typ, ok := thinking["type"].(string)
	if !ok {
		return false, false
	}
	switch strings.ToLower(typ) {
	case "enabled":
		return true, true
	case "disabled":
		return false, true
	default:
		return false, false
	}
}

func defaultOutputTokenLimits() outputTokenLimits {
	return outputTokenLimits{
		DefaultMaxTokens: DefaultRequestMaxTokens,
		MaxTokensCap:     RequestMaxTokensCap,
	}
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

func capChatRequestChoices(value uint64) uint64 {
	if value > MaxChatRequestChoices {
		return MaxChatRequestChoices
	}
	return value
}

func stripMinTokensAboveMax(request map[string]any, maxTokens uint64) {
	if maxTokens == 0 {
		return
	}
	value, ok := numericJSONValueAsUint64(request["min_tokens"])
	if !ok {
		return
	}
	if value > maxTokens {
		request["min_tokens"] = maxTokens
	}
}

func stripTemperatureAboveMax(request map[string]any) {
	value, ok := request["temperature"].(float64)
	if !ok {
		return
	}
	if value > MaxTemperature {
		request["temperature"] = MaxTemperature
	}
}

func forceValidationLogprobs(request map[string]any) {
	request["logprobs"] = true
	request["top_logprobs"] = 5
}

func numericJSONValueAsUint64(value any) (uint64, bool) {
	switch v := value.(type) {
	case float64:
		if v < 0 || v != float64(uint64(v)) {
			return 0, false
		}
		return uint64(v), true
	case uint64:
		return v, true
	case int:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case int64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case json.Number:
		n, err := v.Int64()
		if err != nil || n < 0 {
			return 0, false
		}
		return uint64(n), true
	default:
		return 0, false
	}
}

func stripUnsupportedChatRequestParameters(request map[string]any) {
	// Keep standard OpenAI/Anthropic request fields and only strip known
	// non-standard or currently unsupported gateway additions.
	delete(request, "structured_outputs")
	delete(request, "prompt_logprobs")
	if _, hasStopIDs := request["stop_token_ids"]; hasStopIDs {
		delete(request, "min_tokens")
	}
	if tools, ok := request["tools"].([]any); ok && len(tools) == 0 {
		delete(request, "tools")
		delete(request, "tool_choice")
	}
}

func validateUnsupportedChatRequestFields(request map[string]any) error {
	if _, exists := request["enforced_tokens"]; exists {
		return badChatRequest("field 'enforced_tokens' is not supported")
	}
	if responseFormat, ok := request["response_format"].(map[string]any); ok {
		if typ, ok := responseFormat["type"].(string); ok && typ == "json_object" {
			return badChatRequest("feature 'json_object' is temporarily unavailable")
		}
	}
	if _, exists := request["guided_regex"]; exists {
		return badChatRequest("feature 'guided_regex' is temporarily unavailable")
	}
	if _, exists := request["guided_grammar"]; exists {
		return badChatRequest("feature 'guided_grammar' is temporarily unavailable")
	}
	if structuredOutputs, ok := request["structured_outputs"].(map[string]any); ok {
		if _, exists := structuredOutputs["regex"]; exists {
			return badChatRequest("feature 'guided_regex' is temporarily unavailable")
		}
	}
	return nil
}

func validateOpenAICompatChatMessages(request map[string]any) error {
	rawMessages, exists := request["messages"]
	if !exists {
		return badChatRequest("messages is required")
	}
	messages, ok := rawMessages.([]any)
	if !ok {
		return badChatRequest("messages must be an array")
	}
	if len(messages) == 0 {
		return badChatRequest("messages must not be empty")
	}

	pendingToolCalls := map[string]struct{}{}
	for i, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			return badChatRequest("messages[%d] must be an object", i)
		}
		role, err := requiredNonEmptyString(message, "role")
		if err != nil {
			return badChatRequest("messages[%d].role: %v", i, err)
		}

		switch role {
		case "developer", "system", "user":
			if err := ensureFieldsAbsent(message, "tool_calls", "tool_call_id", "function_call"); err != nil {
				return badChatRequest("messages[%d]: %v", i, err)
			}
			if err := validateRequiredContent(message); err != nil {
				return badChatRequest("messages[%d].content: %v", i, err)
			}
		case "assistant":
			if err := ensureFieldsAbsent(message, "tool_call_id"); err != nil {
				return badChatRequest("messages[%d]: %v", i, err)
			}
			toolCallIDs, hasToolCalls, err := validateToolCallsField(message, i)
			if err != nil {
				return err
			}
			hasFunctionCall, err := validateFunctionCallField(message, i)
			if err != nil {
				return err
			}
			if err := validateAssistantContent(message, hasToolCalls || hasFunctionCall); err != nil {
				return badChatRequest("messages[%d].content: %v", i, err)
			}
			for _, id := range toolCallIDs {
				pendingToolCalls[id] = struct{}{}
			}
		case "tool":
			if err := ensureFieldsAbsent(message, "tool_calls", "function_call", "name"); err != nil {
				return badChatRequest("messages[%d]: %v", i, err)
			}
			toolCallID, err := requiredNonEmptyString(message, "tool_call_id")
			if err != nil {
				return badChatRequest("messages[%d].tool_call_id: %v", i, err)
			}
			if _, ok := pendingToolCalls[toolCallID]; !ok {
				return badChatRequest("messages[%d].tool_call_id does not match any previous assistant tool_calls", i)
			}
			delete(pendingToolCalls, toolCallID)
			if err := validateRequiredContent(message); err != nil {
				return badChatRequest("messages[%d].content: %v", i, err)
			}
		case "function":
			if err := ensureFieldsAbsent(message, "tool_calls", "tool_call_id", "function_call"); err != nil {
				return badChatRequest("messages[%d]: %v", i, err)
			}
			if _, err := requiredNonEmptyString(message, "name"); err != nil {
				return badChatRequest("messages[%d].name: %v", i, err)
			}
			if err := validateRequiredContent(message); err != nil {
				return badChatRequest("messages[%d].content: %v", i, err)
			}
		default:
			return badChatRequest("messages[%d].role has unsupported value %q", i, role)
		}
	}
	return nil
}

func validateToolCallsField(message map[string]any, messageIndex int) ([]string, bool, error) {
	rawToolCalls, exists := message["tool_calls"]
	if !exists {
		return nil, false, nil
	}
	toolCalls, ok := rawToolCalls.([]any)
	if !ok {
		return nil, true, badChatRequest("messages[%d].tool_calls must be an array", messageIndex)
	}
	if len(toolCalls) == 0 {
		return nil, true, badChatRequest("messages[%d].tool_calls must not be empty", messageIndex)
	}

	seen := map[string]struct{}{}
	ids := make([]string, 0, len(toolCalls))
	for callIndex, rawCall := range toolCalls {
		call, ok := rawCall.(map[string]any)
		if !ok {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d] must be an object", messageIndex, callIndex)
		}
		id, err := requiredNonEmptyString(call, "id")
		if err != nil {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].id: %v", messageIndex, callIndex, err)
		}
		if _, exists := seen[id]; exists {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].id is duplicated", messageIndex, callIndex)
		}
		seen[id] = struct{}{}

		callType, err := requiredNonEmptyString(call, "type")
		if err != nil {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].type: %v", messageIndex, callIndex, err)
		}
		if callType != "function" {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].type must be \"function\"", messageIndex, callIndex)
		}
		function, ok := call["function"].(map[string]any)
		if !ok {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].function must be an object", messageIndex, callIndex)
		}
		if _, err := requiredNonEmptyString(function, "name"); err != nil {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].function.name: %v", messageIndex, callIndex, err)
		}
		if err := optionalStringField(function, "arguments"); err != nil {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].function.arguments: %v", messageIndex, callIndex, err)
		}
		ids = append(ids, id)
	}
	return ids, true, nil
}

func validateFunctionCallField(message map[string]any, messageIndex int) (bool, error) {
	rawFunctionCall, exists := message["function_call"]
	if !exists {
		return false, nil
	}
	functionCall, ok := rawFunctionCall.(map[string]any)
	if !ok {
		return true, badChatRequest("messages[%d].function_call must be an object", messageIndex)
	}
	if _, err := requiredNonEmptyString(functionCall, "name"); err != nil {
		return true, badChatRequest("messages[%d].function_call.name: %v", messageIndex, err)
	}
	if err := optionalStringField(functionCall, "arguments"); err != nil {
		return true, badChatRequest("messages[%d].function_call.arguments: %v", messageIndex, err)
	}
	return true, nil
}

func validateAssistantContent(message map[string]any, canBeEmpty bool) error {
	content, exists := message["content"]
	if !exists || content == nil {
		if canBeEmpty {
			return nil
		}
		return fmt.Errorf("is required unless tool_calls or function_call is provided")
	}
	return validateNonEmptyContent(content)
}

func validateRequiredContent(message map[string]any) error {
	content, exists := message["content"]
	if !exists || content == nil {
		return fmt.Errorf("is required")
	}
	return validateNonEmptyContent(content)
}

func validateNonEmptyContent(content any) error {
	switch value := content.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("must not be empty")
		}
		return nil
	case []any:
		if len(value) == 0 {
			return fmt.Errorf("must not be empty")
		}
		for i, rawPart := range value {
			part, ok := rawPart.(map[string]any)
			if !ok {
				return fmt.Errorf("[%d] must be an object", i)
			}
			text, err := requiredTextContentPart(part, i)
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("[%d].text must not be empty", i)
			}
		}
		return nil
	default:
		return fmt.Errorf("must be a string or an array of typed content parts")
	}
}

func requiredTextContentPart(part map[string]any, partIndex int) (string, error) {
	if len(part) != 2 {
		return "", fmt.Errorf("[%d] must only include type and text", partIndex)
	}
	partType, err := requiredNonEmptyString(part, "type")
	if err != nil {
		return "", fmt.Errorf("[%d].type: %w", partIndex, err)
	}
	if partType != "text" {
		return "", fmt.Errorf("[%d].type has unsupported value %q", partIndex, partType)
	}
	text, err := requiredNonEmptyString(part, "text")
	if err != nil {
		return "", fmt.Errorf("[%d].text: %w", partIndex, err)
	}
	return text, nil
}

func ensureFieldsAbsent(values map[string]any, fields ...string) error {
	for _, field := range fields {
		if _, exists := values[field]; exists {
			return fmt.Errorf("%s is not allowed for this role", field)
		}
	}
	return nil
}

func requiredNonEmptyString(values map[string]any, field string) (string, error) {
	rawValue, exists := values[field]
	if !exists || rawValue == nil {
		return "", fmt.Errorf("is required")
	}
	value, ok := rawValue.(string)
	if !ok {
		return "", fmt.Errorf("must be a string")
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("must not be empty")
	}
	return value, nil
}

func optionalStringField(values map[string]any, field string) error {
	rawValue, exists := values[field]
	if !exists || rawValue == nil {
		return nil
	}
	if _, ok := rawValue.(string); !ok {
		return fmt.Errorf("must be a string")
	}
	return nil
}
