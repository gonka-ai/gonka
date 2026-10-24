package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"common/completionapi"
	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

// ContextLimitCheck is the model's governance --max-model-len (0 when unknown) and a call to the
// validator's own node that tokenizes a chat request without generating (vLLM /tokenize).
type ContextLimitCheck struct {
	Limit    uint64
	Tokenize func(ctx context.Context, body []byte) (*http.Response, error)
}

// ContextLimitVerdict is how a request's prompt plus max_tokens compares with the governance limit.
type ContextLimitVerdict int

const (
	// ContextLimitUncounted: the limit is unknown or the tokenize failed.
	ContextLimitUncounted ContextLimitVerdict = iota
	ContextLimitFits
	ContextLimitExceeded
)

// Check counts the request's prompt on the validator's node without a prefill and adds its max_tokens.
// A host running exactly the governance limit refuses an exceeded request before generating, whatever
// limit the validator's own node was started with. /tokenize renders through the same preprocess_chat as
// chat/completions, so both "exceeded" and "fits" are final; the validator's engine limit is not consulted.
func (c ContextLimitCheck) Check(ctx context.Context, inferenceID string, promptPayload []byte) ContextLimitVerdict {
	if c.Limit == 0 || c.Tokenize == nil {
		return ContextLimitUncounted
	}
	maxTokens, err := completionapi.EffectiveMaxTokens(promptPayload)
	if err != nil {
		return ContextLimitUncounted
	}
	promptTokens, err := tokenizePrompt(ctx, promptPayload, c.Tokenize)
	if err != nil {
		logging.Warn("context limit: tokenize failed", types.Validation, "inferenceId", inferenceID, "error", err)
		return ContextLimitUncounted
	}
	if promptTokens+maxTokens <= c.Limit {
		return ContextLimitFits
	}
	logging.Info("request exceeds the governance context limit", types.Validation,
		"inferenceId", inferenceID, "promptTokens", promptTokens, "maxTokens", maxTokens, "contextLimit", c.Limit)
	return ContextLimitExceeded
}

// tokenizeFields are the TokenizeChatRequest fields that shape the rendered prompt.
var tokenizeFields = []string{"model", "messages", "tools", "chat_template", "chat_template_kwargs",
	"add_generation_prompt", "continue_final_message", "add_special_tokens", "mm_processor_kwargs", "media_io_kwargs"}

// tokenizePrompt asks the node for the prompt's token count. reasoning_effort and documents reach the chat
// template the way the chat path passes them (build_chat_params), since /tokenize does not read them.
func tokenizePrompt(
	ctx context.Context,
	promptPayload []byte,
	tokenize func(ctx context.Context, body []byte) (*http.Response, error),
) (uint64, error) {
	var request map[string]interface{}
	if err := json.Unmarshal(promptPayload, &request); err != nil {
		return 0, err
	}
	// The chat path renames reasoning_content to reasoning before rendering (_normalize_messages_before).
	if messages, ok := request["messages"].([]interface{}); ok {
		for _, raw := range messages {
			message, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			reasoning, ok := message["reasoning_content"]
			delete(message, "reasoning_content")
			if ok && reasoning != nil && message["reasoning"] == nil {
				message["reasoning"] = reasoning
			}
		}
	}
	tokenizeRequest := make(map[string]interface{}, len(tokenizeFields))
	for _, field := range tokenizeFields {
		if value, ok := request[field]; ok && value != nil {
			tokenizeRequest[field] = value
		}
	}
	kwargs, _ := request["chat_template_kwargs"].(map[string]interface{})
	merged := make(map[string]interface{}, len(kwargs)+3)
	for k, v := range kwargs {
		merged[k] = v
	}
	if effort, ok := request["reasoning_effort"]; ok && effort != nil {
		merged["reasoning_effort"] = effort
		if _, set := kwargs["enable_thinking"]; !set {
			merged["enable_thinking"] = effort != "none"
		}
	}
	if documents, ok := request["documents"]; ok && documents != nil {
		merged["documents"] = documents
	}
	if len(merged) > 0 {
		tokenizeRequest["chat_template_kwargs"] = merged
	}
	body, err := json.Marshal(tokenizeRequest)
	if err != nil {
		return 0, err
	}
	resp, err := tokenize(ctx, body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(completionapi.NewCappedResponseReader(resp.Body))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("validator node returned status %d", resp.StatusCode)
	}
	var counted struct {
		Count *uint64 `json:"count"`
	}
	if err := json.Unmarshal(payload, &counted); err != nil {
		return 0, err
	}
	if counted.Count == nil || *counted.Count == 0 {
		return 0, errors.New("tokenize response carried no count")
	}
	return *counted.Count, nil
}
