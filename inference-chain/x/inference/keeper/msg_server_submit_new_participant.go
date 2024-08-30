package keeper

import (
	"context"
	"cosmossdk.io/log"
	"encoding/base64"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitNewParticipant(goCtx context.Context, msg *types.MsgSubmitNewParticipant) (*types.MsgSubmitNewParticipantResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	logger := k.Logger().With("op", "submitNew")
	newParticipant := types.Participant{
		Index:                msg.Creator,
		Address:              msg.Creator,
		Reputation:           1,
		Weight:               int32(ctx.BlockHeight()),
		JoinTime:             ctx.BlockTime().UnixMilli(),
		JoinHeight:           ctx.BlockHeight(),
		LastInferenceTime:    0,
		InferenceUrl:         msg.Url,
		Models:               msg.Models,
		Status:               types.ParticipantStatus_RAMPING,
		PromptTokenCount:     make(map[string]uint64),
		CompletionTokenCount: make(map[string]uint64),
		ValidatorKey:         msg.ValidatorKey,
	}
	for _, model := range msg.Models {
		newParticipant.PromptTokenCount[model] = 0
		newParticipant.CompletionTokenCount[model] = 0
	}
	k.SetParticipant(ctx, newParticipant)
	response, err := k.updateComputeResults(ctx, logger)
	if err != nil {
		return response, err
	}

	return &types.MsgSubmitNewParticipantResponse{}, nil
}

func (k msgServer) updateComputeResults(ctx sdk.Context, logger log.Logger) (*types.MsgSubmitNewParticipantResponse, error) {
	allParticipants := k.GetAllParticipant(ctx)
	newPowerMap := make([]keeper.ComputeResult, 0)
	for _, participant := range allParticipants {
		if participant.ValidatorKey == "" {
			continue
		}
		pubKeyBytes, err := base64.StdEncoding.DecodeString(participant.ValidatorKey)
		if err != nil {
			logger.Error("Error decoding pubkey", "error", err)
			continue
		}
		pubKey := ed25519.PubKey{Key: pubKeyBytes}

		result := keeper.ComputeResult{
			Power:           int64(participant.Weight),
			ValidatorPubKey: &pubKey,
			OperatorAddress: participant.Index,
		}
		newPowerMap = append(newPowerMap, result)
	}
	_, err := k.staking.SetComputeValidators(ctx, newPowerMap)
	if err != nil {
		k.Logger().Error("Error setting compute validators", "error", err)
		return nil, err
	}
	return nil, nil
}
