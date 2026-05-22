package devshard

import (
	"context"
	"errors"
)

// ErrValidationAlreadyLeased is returned when another devshardd instance already
// holds the validation lease for an inference.
var ErrValidationAlreadyLeased = errors.New("validation leased by another instance")

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
// need to persist successful async MsgValidation submission.
type ValidationCompletionRecorder interface {
	MarkValidationSubmitted(ctx context.Context, escrowID string, inferenceID uint64) error
}
