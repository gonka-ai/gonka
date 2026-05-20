package devshard

import (
	"fmt"

	"devshard/types"
)

type FinishReason = types.FinishReason

const (
	FinishReason_FINISH_REASON_UNSPECIFIED                = types.FinishReason_FINISH_REASON_UNSPECIFIED
	FinishReason_FINISH_REASON_COMPLETED                  = types.FinishReason_FINISH_REASON_COMPLETED
	FinishReason_FINISH_REASON_DEVSHARD_REQUESTS_DISABLED = types.FinishReason_FINISH_REASON_DEVSHARD_REQUESTS_DISABLED
	FinishReason_FINISH_REASON_POC_ABORTED                = types.FinishReason_FINISH_REASON_POC_ABORTED
	FinishReason_FINISH_REASON_NO_PRESERVE_NODES          = types.FinishReason_FINISH_REASON_NO_PRESERVE_NODES
)

type UnavailableFinishError struct {
	Reason FinishReason
	Err    error
}

func NewUnavailableFinishError(reason FinishReason, err error) error {
	return &UnavailableFinishError{Reason: reason, Err: err}
}

func (e *UnavailableFinishError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return e.Reason.String()
	}
	return fmt.Sprintf("%s: %v", e.Reason.String(), e.Err)
}

func (e *UnavailableFinishError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
