package types

import "context"

type BlsHooks interface {
	AfterThresholdSigningCompleted(ctx context.Context, requestID []byte, currentEpochID uint64) error
	AfterThresholdSigningFailed(ctx context.Context, requestID []byte, currentEpochID uint64, reason string) error
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

func (h MultiBlsHooks) AfterThresholdSigningFailed(ctx context.Context, requestID []byte, currentEpochID uint64, reason string) error {
	for i := range h {
		if err := h[i].AfterThresholdSigningFailed(ctx, requestID, currentEpochID, reason); err != nil {
			return err
		}
	}
	return nil
}
