package types

import "context"

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
	for i := range h {
		if err := h[i].AfterThresholdSigningCompleted(ctx, requestID, currentEpochID); err != nil {
			return err
		}
	}
	return nil
}

func (h MultiBlsHooks) AfterThresholdSigningFailed(ctx context.Context, requestID []byte, currentEpochID uint64, reason string) (bool, error) {
	closeRetry := ThresholdSigningFailedPostActionNone
	for i := range h {
		hookCloseRetry, err := h[i].AfterThresholdSigningFailed(ctx, requestID, currentEpochID, reason)
		if err != nil {
			return closeRetry, err
		}
		if hookCloseRetry {
			closeRetry = ThresholdSigningFailedPostActionCloseRetry
		}
	}
	return closeRetry, nil
}
