package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/utils"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) PocValidationsForStage(goCtx context.Context, req *types.QueryPocValidationsForStageRequest) (*types.QueryPocValidationsForStageResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	pocValidations, err := k.GetPoCValidationByStage(ctx, req.BlockHeight)
	if err != nil {
		k.LogError("failed to get PoC validations", types.PoC, "err", err)
		return nil, status.Error(codes.Internal, "failed to get PoC validations")
	}

	pocValidationsWithParticipants := make([]types.PoCValidationsWithParticipants, 0, len(pocValidations))
	for participantIndex, validations := range pocValidations {
		addr, err := sdk.AccAddressFromBech32(participantIndex)
		if err != nil {
			k.LogError("PocValidationsForStage. Invalid address", types.PoC, "address", participantIndex, "err", err)
			continue
		}

		acc := k.AccountKeeper.GetAccount(ctx, addr)
		if acc == nil {
			k.LogError("PocValidationsForStage. Account not found", types.PoC, "address", participantIndex)
			continue
		}

		pubKey := acc.GetPubKey()
		if pubKey == nil {
			k.LogError("PocValidationsForStage. PubKey not found", types.PoC, "address", participantIndex)
			continue
		}

		pocValidationsWithParticipants = append(pocValidationsWithParticipants, types.PoCValidationsWithParticipants{
			Participant:    participantIndex,
			PocValidation:  validations,
			PubKey:         utils.PubKeyToString(pubKey),
			HexPubKey:      utils.PubKeyToHexString(pubKey),
		})
	}

	return &types.QueryPocValidationsForStageResponse{
		PocValidation: pocValidationsWithParticipants,
	}, nil
}

