package types

import "context"

type BlsHooks interface {
	AfterThresholdSigningCompleted(ctx context.Context, requestID []byte, currentEpochID uint64) error
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
