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
	"time"
)

func (am AppModule) SendNewValidatorWeightsToStaking(ctx context.Context, blockHeight int64) {
	allPower := am.keeper.AllPower(ctx)
	am.LogInfo("Amount of power entries found.", "n", len(allPower))

	lastGroupId := am.keeper.GetEpochGroupId(ctx)
	if lastGroupId != 0 {
		recentMembers, err := am.groupMsgServer.GroupMembers(ctx, &group.QueryGroupMembersRequest{
			GroupId: lastGroupId,
		})
		if err != nil {
			am.LogError("Error getting group members", "error", err)
		} else {
			for _, m := range recentMembers.Members {
				am.LogInfo("Group member found.", "address", m.Member.Address, "weight", m.Member.Weight)
			}
		}
	}

	var activeParticipants []*types.ActiveParticipant
	var computeResults []keeper.ComputeResult
	for _, p := range allPower {
		participant, ok := am.keeper.GetParticipant(ctx, p.ParticipantAddress)
		if !ok {
			am.LogError("Error getting participant", "address", p.ParticipantAddress)
			continue
		}
		if p.Power < 1 {
			am.LogWarn("Participant has no power.", "participant", p.ParticipantAddress)
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

	//activeParticipants := make([]*types.ActiveParticipant, len(computeResults))
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
	err = am.createEpochGroup(ctx, groupMembers)
	if err != nil {
		am.LogError("Error creating epoch group", "error", err)
	}
}

func (am AppModule) createEpochGroup(ctx context.Context, groupMembers []group.MemberRequest) error {
	votingPeriod := 4 * time.Minute
	minExecutionPeriod := 0 * time.Minute

	groupMsg := &group.MsgCreateGroupWithPolicy{
		Admin:   am.keeper.GetAuthority(),
		Members: groupMembers,
	}
	policy := group.NewPercentageDecisionPolicy(
		"0.51",
		votingPeriod,
		minExecutionPeriod,
	)
	err := groupMsg.SetDecisionPolicy(policy)
	if err != nil {
		am.LogError("Error setting decision policy", "error", err)
		return err
	}

	result, err := am.groupMsgServer.CreateGroupWithPolicy(ctx, groupMsg)
	if err != nil {
		am.LogError("Error creating group", "error", err)
		return err
	}
	am.keeper.SetEpochGroupId(ctx, result.GroupId)
	am.keeper.SetEpochPolicy(ctx, result.GroupPolicyAddress)

	am.LogInfo("Created group", "groupID", result.GroupId, "policyAddress", result.GroupPolicyAddress)
	return nil
}
