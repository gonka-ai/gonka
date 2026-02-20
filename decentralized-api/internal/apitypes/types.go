// Package apitypes contains shared types used across internal packages to avoid import cycles.
package apitypes

// PayloadResponse is returned by payload retrieval endpoints.
type PayloadResponse struct {
	InferenceId       string `json:"inference_id"`
	PromptPayload     []byte `json:"prompt_payload"`
	ResponsePayload   []byte `json:"response_payload"`
	ExecutorSignature string `json:"executor_signature"`
}

// ChatRequest represents the request stored by the TA in prompt storage.
type ChatRequest struct {
	Body              []byte        `json:"body"`
	ContentType       string        `json:"content_type"`
	OpenAiRequest     OpenAiRequest `json:"open_ai_request"`
	AuthKey           string        `json:"auth_key"`
	Seed              string        `json:"seed"`
	InferenceId       string        `json:"inference_id"`
	RequesterAddress  string        `json:"requester_address"`
	TransferAddress   string        `json:"transfer_address"`
	Timestamp         int64         `json:"timestamp"`
	TransferSignature string        `json:"transfer_signature"`
	PromptHash        string        `json:"prompt_hash"`
}

// OpenAiRequest is the parsed OpenAI-compatible request body.
type OpenAiRequest struct {
	Model               string    `json:"model"`
	Seed                int32     `json:"seed"`
	MaxTokens           int32     `json:"max_tokens"`
	MaxCompletionTokens int32     `json:"max_completion_tokens"`
	Messages            []Message `json:"messages"`
}

// Message represents a single chat message.
type Message struct {
	Content string `json:"content"`
}
