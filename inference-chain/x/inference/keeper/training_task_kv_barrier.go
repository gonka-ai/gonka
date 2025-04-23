package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type BarrierKey struct {
	BarrierId   string
	TaskId      uint64
	Participant string
	NodeId      string
	Epoch       int32
}

func (k Keeper) GetTrainingBarrier(ctx sdk.Context, key types.TrainingTaskBarrierKey) (*types.TrainingTaskBarrier, bool) {
	var barrier types.TrainingTaskBarrier
	return GetValue(&k, ctx, &barrier, []byte{}, key.ToByteKey())
}

func (k Keeper) SetTrainingBarrier(ctx sdk.Context, barrier *types.TrainingTaskBarrier) {
	key := types.TrainingTaskBarrierKey{
		BarrierId:   barrier.BarrierId,
		TaskId:      barrier.TaskId,
		Participant: barrier.Participant,
		NodeId:      barrier.NodeId,
		Epoch:       barrier.Epoch,
	}
	SetValue(k, ctx, barrier, []byte{}, key.ToByteKey())
}
