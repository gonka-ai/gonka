package inference

import (
	"context"

	"github.com/productscience/inference/x/inference/keeper"
)

type BlsHooks struct {
	k keeper.Keeper
}

func NewBlsHooks(k keeper.Keeper) BlsHooks {
	return BlsHooks{k: k}
}

func (h BlsHooks) AfterThresholdSigningCompleted(ctx context.Context, requestID []byte, _ uint64) error {
	return h.k.CleanupBridgePendingRefundByBlsRequestID(ctx, requestID)
}
