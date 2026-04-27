package types

import (
	"context"
	"errors"
	"fmt"
)

const (
	ThresholdSigningFailedPostActionNone       = false
	ThresholdSigningFailedPostActionCloseRetry = true
)

type BlsHooks interface {
	AfterThresholdSigningCompleted(ctx context.Context, requestID []byte, currentEpochID uint64) error
	AfterThresholdSigningFailed(ctx context.Context, requestID []byte, currentEpochID uint64, reason string) (bool, error)
}

type BlsHooksWrapper struct{ BlsHooks }

func (BlsHooksWrapper) IsOnePerModuleType() {}

var _ BlsHooks = MultiBlsHooks{}

type MultiBlsHooks []BlsHooks

func NewMultiBlsHooks(hooks ...BlsHooks) MultiBlsHooks {
	return hooks
}

func (h MultiBlsHooks) AfterThresholdSigningCompleted(ctx context.Context, requestID []byte, currentEpochID uint64) error {
	var errs []error
	for i := range h {
		if err := h[i].AfterThresholdSigningCompleted(ctx, requestID, currentEpochID); err != nil {
			errs = append(errs, fmt.Errorf("after threshold signing completed hook[%d] %T: %w", i, h[i], err))
		}
	}
	return errors.Join(errs...)
}

func (h MultiBlsHooks) AfterThresholdSigningFailed(ctx context.Context, requestID []byte, currentEpochID uint64, reason string) (bool, error) {
	closeRetry := ThresholdSigningFailedPostActionNone
	var errs []error
	for i := range h {
		hookCloseRetry, err := h[i].AfterThresholdSigningFailed(ctx, requestID, currentEpochID, reason)
		if hookCloseRetry {
			closeRetry = ThresholdSigningFailedPostActionCloseRetry
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("after threshold signing failed hook[%d] %T: %w", i, h[i], err))
		}
	}
	return closeRetry, errors.Join(errs...)
}
