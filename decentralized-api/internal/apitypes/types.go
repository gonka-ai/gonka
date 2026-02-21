// Package apitypes contains shared types used across internal packages to avoid import cycles.
package apitypes

import (
	"github.com/productscience/inference/api/inference/inference"
)

// PayloadResponse is returned by payload retrieval endpoints.
type PayloadResponse struct {
	InferenceId       string `json:"inference_id"`
	PromptPayload     []byte `json:"prompt_payload"`
	ResponsePayload   []byte `json:"response_payload"`
	ExecutorSignature string `json:"executor_signature"`
}

// ChatRequest represents the request stored by the TA in prompt storage.
type ChatRequest struct {
	Body              []byte                  `json:"body"`
	ContentType       string                  `json:"content_type"`
	OpenAiRequest     OpenAiRequest           `json:"open_ai_request"` // kept for compatibility
	AuthKey           string                  `json:"auth_key"` // signature signing inference request
	Seed              string                  `json:"seed"`
	InferenceId       string                  `json:"inference_id"`
	RequesterAddress  string                  `json:"requester_address"` // address of participant, who signed inference request
	TransferAddress   string                  `json:"transfer_address"`
	Timestamp         int64                   `json:"timestamp"` // timestamp of the request
	TransferSignature string                  `json:"transfer_signature"` // signature of the transfer address
	PromptHash        string                  `json:"prompt_hash"`
	VotingResult      *inference.VotingResult `json:"voting_result"` // outcome of vote, if there was one
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
