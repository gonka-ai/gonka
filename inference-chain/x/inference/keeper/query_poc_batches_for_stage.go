package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/utils"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) PocBatchesForStage(goCtx context.Context, req *types.QueryPocBatchesForStageRequest) (*types.QueryPocBatchesForStageResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	pocBatches, err := k.GetPoCBatchesByStage(ctx, req.BlockHeight)
	if err != nil {
		k.LogError("failed to get PoC batches", types.PoC, "err", err)
		return nil, status.Error(codes.Internal, "failed to get PoC batches")
	}

	pocBatchesWithParticipants := make([]types.PoCBatchesWithParticipants, 0, len(pocBatches))
	for participantIndex, batches := range pocBatches {
		addr, err := sdk.AccAddressFromBech32(participantIndex)
		if err != nil {
			k.LogError("PocBatchesForStage. Invalid address", types.PoC, "address", participantIndex, "err", err)
			continue
		}

		acc := k.AccountKeeper.GetAccount(ctx, addr)
		if acc == nil {
			k.LogError("PocBatchesForStage. Account not found", types.PoC, "address", participantIndex)
			continue
		}

		pubKey := acc.GetPubKey()
		if pubKey == nil {
			k.LogError("PocBatchesForStage. PubKey not found", types.PoC, "address", participantIndex)
			continue
		}

		pocBatchesWithParticipants = append(pocBatchesWithParticipants, types.PoCBatchesWithParticipants{
			Participant: participantIndex,
			PocBatch:    batches,
			PubKey:      utils.PubKeyToString(pubKey),
			HexPubKey:   utils.PubKeyToHexString(pubKey),
		})
	}

	return &types.QueryPocBatchesForStageResponse{
		PocBatch: pocBatchesWithParticipants,
	}, nil
}
