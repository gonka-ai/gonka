package devshard

import (
	"errors"
	"net/http"
)

// Recovery tells the engine whether a response for this inference may
// already be stored, written by an earlier execution that lost its finish.
type Recovery uint8

const (
	// RecoveryNone runs the model without reading payload storage.
	RecoveryNone Recovery = iota
	// RecoveryStoredFirst rebuilds the result from a stored response when one
	// exists, and runs the model only when none does. Payload storage keeps
	// the first response, so a second run would commit a hash validators
	// never see.
	RecoveryStoredFirst
	// RecoveryStoredOnly rebuilds the result from a stored response and fails
	// with ErrNoStoredResponse instead of running the model.
	RecoveryStoredOnly
)

// ErrNoStoredResponse is returned under RecoveryStoredOnly when nothing is stored.
var ErrNoStoredResponse = errors.New("no stored response to recover")

// ExecuteRequest contains the data needed to execute an inference.
type ExecuteRequest struct {
	InferenceID uint64
	Model       string
	Prompt      []byte
	PromptHash  []byte
	InputLength uint64
	MaxTokens   uint64
	EscrowID    string // Session escrow ID for namespaced payload storage
	EpochID     uint64 // Epoch when the escrow was pinned on mainnet

	// Recovery is set by the host when an earlier execution may have stored
	// a response for this inference. The zero value runs the model.
	Recovery Recovery

	// ResponseWriter, if set, receives the raw ML node response as it streams.
	// The engine should write inference output here for real-time forwarding.
	ResponseWriter http.ResponseWriter

	LogprobsOptimizationOverride *bool
}

// ExecuteResult contains the outcome of an inference execution.
type ExecuteResult struct {
	ResponseHash          []byte
	ServedHash            []byte
	InputTokens           uint64
	OutputTokens          uint64
	ResponseBody          []byte // raw ML response bytes (always populated when available)
	PartialResponse       bool
	PartialResponseReason string
	PartialResponseWhere  string
}

// ValidateRequest contains the data needed to validate an inference.
type ValidateRequest struct {
	InferenceID  uint64
	Model        string
	PromptHash   []byte
	ResponseHash []byte
	ServedHash   []byte
	InputTokens  uint64
	OutputTokens uint64

	// Fields for remote payload retrieval (devshard validation)
	EscrowID        string // Session escrow ID for building the payload URL path
	EpochID         uint64 // Epoch when the executor stored the payload
	ExecutorAddress string // Executor's validator address for signature verification
}

// ValidateResult contains the outcome of a validation.
// Reason is a short tag for the outcome (e.g. "similarity_pass",
// "similarity_below", "inflated_tokens", "different_length",
// "different_tokens", "invalid_inference", "rejected_payload").
// Details carries kv pairs with the values behind the decision and is
// appended to the publish log so an invalid vote is never opaque.
type ValidateResult struct {
	Valid   bool
	Reason  string
	Details []any
}
