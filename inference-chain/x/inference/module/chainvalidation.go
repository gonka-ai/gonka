package inference

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/productscience/inference/x/inference/types"
	"log"
	"strconv"
)

func (am AppModule) SendNewValidatorWeightsToStaking(ctx context.Context, blockHeight int64) {
	allPower := am.keeper.AllPower(ctx)
	am.LogInfo("Amount of power entries found.", "n", len(allPower))

	var activeParticipants []*types.ActiveParticipant
	var computeResults []keeper.ComputeResult
	for _, p := range allPower {
		participant, ok := am.keeper.GetParticipant(ctx, p.ParticipantAddress)
		if !ok {
			am.LogError("Error getting participant", "address", p.ParticipantAddress)
			continue
		}

		if participant.ValidatorKey == "" {
			am.LogError("Participant hasn't provided their validator key.", "participant", p.ParticipantAddress)
			continue
		}
		pubKeyBytes, err := base64.StdEncoding.DecodeString(participant.ValidatorKey)
		if err != nil {
			am.LogError("Error decoding pubkey", "error", err)
			continue
		}

		pubKey := ed25519.PubKey{Key: pubKeyBytes}

		r := keeper.ComputeResult{
			Power:           p.Power,
			ValidatorPubKey: &pubKey,
			OperatorAddress: p.ParticipantAddress,
		}
		am.LogInfo("Setting compute validator.", "computeResult", r)
		computeResults = append(computeResults, r)

		activeParticipant := &types.ActiveParticipant{
			Index:        p.ParticipantAddress,
			ValidatorKey: participant.ValidatorKey,
			Weight:       p.Power,
			InferenceUrl: participant.InferenceUrl,
			Models:       participant.Models,
		}
		activeParticipants = append(activeParticipants, activeParticipant)
	}

	am.keeper.RemoveAllPower(ctx)

	if len(computeResults) == 0 {
		am.LogWarn("No compute validators to set. Keeping validators and active participants the same.")
		return
	}

	_, err := am.keeper.Staking.SetComputeValidators(ctx, computeResults)
	if err != nil {
		msg := fmt.Sprintf("Error setting compute validators: %v", err)
		am.LogError("Error setting compute validators.", "err", err)
		log.Fatalf(msg)
	}

	activeParticipants := make([]*types.ActiveParticipant, len(computeResults))
	groupMembers := make([]group.MemberRequest, len(computeResults))
	for i, r := range computeResults {
		activeParticipants[i] = &types.ActiveParticipant{
			Index:  r.OperatorAddress,
			Weight: r.Power,
		}
		groupMembers[i] = group.MemberRequest{
			Address:  r.OperatorAddress,
			Weight:   strconv.FormatInt(r.Power, 10),
			Metadata: "",
		}
	}

	am.keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
		Participants:         activeParticipants,
		CreatedAtBlockHeight: blockHeight,
	})
	moduleAddress := am.accountKeeper.GetModuleAddress(types.ModuleName)
	result, err := am.groupMsgServer.CreateGroup(ctx, &group.MsgCreateGroup{
		Admin:    moduleAddress.String(),
		Members:  groupMembers,
		Metadata: "",
	})
	if err != nil {
		am.LogError("Error creating group", "error", err)
		return
	}

	am.LogInfo("Created group", "groupID", result.GroupId)
}

func ParticipantsToGroupMembers(ctx context.Context, participants types.ActiveParticipants) ([]group.MemberRequest, error) {
	var members []group.MemberRequest
	for _, p := range participants.Participants {
		member := group.MemberRequest{
			Address: p.Index,
			Weight:  strconv.FormatInt(p.Weight, 10),
		}
		members = append(members, member)
	}
	return members, nil
}
