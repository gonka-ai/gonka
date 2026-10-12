package devshard

import (
	"context"
	"errors"
)

// ErrValidationAlreadyLeased means a validation lease row already exists.
var ErrValidationAlreadyLeased = errors.New("validation lease already exists")

// ErrValidationLeaseAbandoned is returned when this instance must not submit or
// complete a lease: local acquire TTL exceeded, or the pending lease is no
// longer owned (stolen / completed).
var ErrValidationLeaseAbandoned = errors.New("validation lease abandoned")

// ErrValidationDeferred keeps work retryable when its model has no credit.
var ErrValidationDeferred = errors.New("validation deferred: no credit")

// ValidationAvailability lets scheduling skip exhausted models before work starts.
type ValidationAvailability interface{ CanValidate(model string) bool }

func CanValidate(v ValidationEngine, model string) bool {
	gate, ok := v.(ValidationAvailability)
	return !ok || gate.CanValidate(model)
}

// ErrValidationSkipped signals that a validation attempt was deliberately
// abandoned without producing a MsgValidation or MsgValidationVote.
// The canonical trigger is the executor returning 404 for the payload
// (the payload has already been pruned). Callers should treat this as a
// quiet no-op rather than a validation failure.
var ErrValidationSkipped = errors.New("devshard validation skipped")

// InferenceEngine executes inference on an ML node.
// Implemented by dapi using existing broker + completionapi.
type InferenceEngine interface {
	Execute(ctx context.Context, req ExecuteRequest) (*ExecuteResult, error)
}

// ValidationEngine re-executes inference and compares logits.
// Implemented by dapi using existing broker + completionapi.
type ValidationEngine interface {
	Validate(ctx context.Context, req ValidateRequest) (*ValidateResult, error)
}

// ValidationCompletionRecorder can be implemented by validation engines that
// need to gate async MsgValidation submission and persist lease completion.
type ValidationCompletionRecorder interface {
	// AllowValidationSubmit must be called before publishing MsgValidation.
	// ErrValidationLeaseAbandoned means skip submit and do not mark submitted.
	AllowValidationSubmit(ctx context.Context, escrowID string, inferenceID uint64) error
	MarkValidationSubmitted(ctx context.Context, escrowID string, inferenceID uint64) error
	// CheckValidationLease checks only local epoch and TTL; safe under the host lock.
	CheckValidationLease(escrowID string, inferenceID uint64) error
	ForgetValidation(escrowID string, inferenceID uint64)
}

// ValidationEpochAvailability lets scheduling stop work outside the escrow epoch.
type ValidationEpochAvailability interface {
	CanValidateEpoch(epochID uint64) bool
}

func CanValidateEpoch(v ValidationEngine, epochID uint64) bool {
	gate, ok := v.(ValidationEpochAvailability)
	return !ok || gate.CanValidateEpoch(epochID)
}

var ErrValidationEpochUnavailable = errors.New("validation epoch is not current")
