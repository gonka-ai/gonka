package keeper

import (
	"context"
	"encoding/base64"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"golang.org/x/exp/maps"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) GetParticipantCurrentStats(goCtx context.Context, req *types.QueryGetParticipantCurrentStatsRequest) (*types.QueryGetParticipantCurrentStatsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	currentEpoch, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("GetParticipantCurrentStats failure", types.Participants, "error", err)
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &types.QueryGetParticipantCurrentStatsResponse{}
	for _, weight := range currentEpoch.GroupData.ValidationWeights {
		if weight.MemberAddress == req.ParticipantId {
			response.Weight = uint64(weight.Weight)
			response.Reputation = weight.Reputation
		}
	}

	return response, nil
}

func (k Keeper) GetParticipantsFullStats(ctx context.Context, _ *types.QueryParticipantsFullStatsRequest) (*types.QueryParticipantsFullStatsResponse, error) {
	currentEpoch, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("GetParticipantsFullStats failure", types.Participants, "error", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	previous, err := k.GetPreviousEpochGroup(ctx)
	if err != nil {
		k.LogError("GetParticipantsFullStats failure", types.Participants, "error", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	participants := make(map[string]*types.ParticipantFullStats)

	allParticipants := currentEpoch.ParticipantKeeper.GetAllParticipant(ctx)
	address2valAddr := make(map[string]string)
	for _, participant := range allParticipants {
		validatorKey := participant.ValidatorKey

		// Try to parse as bech32 validator address first
		if _, err := sdk.ValAddressFromBech32(validatorKey); err == nil {
			address2valAddr[participant.Address] = validatorKey
		} else {
			// Try to decode as base64-encoded public key and convert to validator address
			pubKeyBytes, err := base64.StdEncoding.DecodeString(validatorKey)
			if err != nil {
				k.LogError("GetParticipantsFullStats failure: invalid ValidatorKey base64 format", types.Participants, "error", err, "validatorKey", validatorKey)
				return nil, status.Error(codes.Internal, err.Error())
			}

			// Create Ed25519 public key and get validator address
			pubKey := &ed25519.PubKey{Key: pubKeyBytes}
			valAddr := sdk.ValAddress(pubKey.Address())
			address2valAddr[participant.Address] = valAddr.String()
		}
	}

	if err != nil {
		k.LogError("GetParticipantsFullStats failure", types.Participants, "error", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	for _, member := range currentEpoch.GroupData.ValidationWeights {
		participant, found := currentEpoch.ParticipantKeeper.GetParticipant(ctx, member.MemberAddress)
		if !found {
			k.LogInfo("GetParticipantsFullStats epoch member not found in participants set", types.Participants, "member", member.MemberAddress)
			continue
		}

		accAddr, _ := sdk.AccAddressFromBech32(member.MemberAddress)
		k.LogInfo(
			"Gleebb2", types.EpochGroup,
			"member.MemberAddress", member.MemberAddress,
			"accAddr", accAddr.String(),
			"valAddr", sdk.ValAddress(accAddr).String(),
			"valAddr2", address2valAddr[member.MemberAddress],
		)

		participants[member.MemberAddress] = &types.ParticipantFullStats{
			AccountAddress:          member.MemberAddress,
			OperatorAddress:         address2valAddr[member.MemberAddress],
			Reputation:              member.Reputation,
			EarnedCoinsCurrentEpoch: participant.CurrentEpochStats.EarnedCoins,
			EpochsCompleted:         participant.EpochsCompleted,
		}
	}

	addresses := maps.Keys(participants)
	summaries := k.GetParticipantsEpochSummaries(ctx, addresses, previous.GroupData.PocStartBlockHeight)
	for _, summary := range summaries {
		stats, ok := participants[summary.ParticipantId]
		if !ok {
			k.LogInfo("GetParticipantsFullStats didn't current stats for participant", types.Participants, "paerticipant", summary.ParticipantId)
			continue
		}
		stats.RewardedCoinsLatestEpoch = summary.RewardedCoins
	}

	return &types.QueryParticipantsFullStatsResponse{
		ParticipantsStats: maps.Values(participants),
	}, nil
}
