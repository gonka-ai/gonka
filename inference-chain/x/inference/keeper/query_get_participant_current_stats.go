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
	for _, member := range currentEpoch.GroupData.ValidationWeights {
		participant, found := currentEpoch.ParticipantKeeper.GetParticipant(ctx, member.MemberAddress)
		if !found {
			k.LogInfo("GetParticipantsFullStats epoch member not found in participants set", types.Participants, "member", member.MemberAddress)
			continue
		}

		// Find validator address for this participant (handles both genesis and runtime validators)
		validatorAddr, err := k.findValidatorAddressForParticipant(ctx, member.MemberAddress, participant.ValidatorKey)
		if err != nil {
			k.LogError("GetParticipantsFullStats: failed to find validator for participant", types.Participants,
				"participant", member.MemberAddress, "error", err.Error())
			continue
		}

		participants[member.MemberAddress] = &types.ParticipantFullStats{
			AccountAddress:          member.MemberAddress,
			OperatorAddress:         validatorAddr,
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

// findValidatorAddressForParticipant attempts to find the validator address for a participant
// Handles both genesis validators (account-derived) and runtime validators (consensus-derived)
func (k Keeper) findValidatorAddressForParticipant(ctx context.Context, participantAddr string, validatorKey string) (string, error) {
	// Get all validators from staking keeper
	validators, err := k.Staking.GetAllValidators(ctx)
	if err != nil {
		k.LogError("Failed to get all validators", types.Participants, "error", err.Error())
		return "", err
	}

	// First try: account-derived validator address (genesis case)
	accAddr, err := sdk.AccAddressFromBech32(participantAddr)
	if err != nil {
		return "", err
	}

	accountBasedValAddr := sdk.ValAddress(accAddr)

	// Look for validator with account-derived address
	for _, validator := range validators {
		if validator.OperatorAddress == accountBasedValAddr.String() {
			k.LogDebug("Found validator using account-derived address", types.Participants,
				"participant", participantAddr, "validatorAddr", accountBasedValAddr.String())
			return accountBasedValAddr.String(), nil
		}
	}

	// Second try: consensus-derived validator address (runtime case)
	if validatorKey != "" {
		// Try to decode as base64-encoded public key
		pubKeyBytes, err := base64.StdEncoding.DecodeString(validatorKey)
		if err == nil {
			// Create Ed25519 public key and get validator address
			pubKey := &ed25519.PubKey{Key: pubKeyBytes}
			consensusBasedValAddr := sdk.ValAddress(pubKey.Address())

			// Look for validator with consensus-derived address
			for _, validator := range validators {
				if validator.OperatorAddress == consensusBasedValAddr.String() {
					k.LogDebug("Found validator using consensus-derived address", types.Participants,
						"participant", participantAddr, "validatorAddr", consensusBasedValAddr.String())
					return consensusBasedValAddr.String(), nil
				}
			}
		}
	}

	// If we couldn't find the validator by either method, return account-based address as fallback
	// This ensures we don't break existing functionality
	k.LogWarn("Could not find validator for participant, using account-derived address as fallback", types.Participants,
		"participant", participantAddr, "validatorAddr", accountBasedValAddr.String())
	return accountBasedValAddr.String(), nil
}
