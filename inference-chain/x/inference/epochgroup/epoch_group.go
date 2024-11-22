package epochgroup

import (
	"context"
	"encoding/base64"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/productscience/inference/x/inference/types"
	"strconv"
	"time"
)

type EpochGroup struct {
	GroupKeeper       types.GroupMessageKeeper
	ParticipantKeeper types.ParticipantKeeper
	Authority         string
	Logger            types.InferenceLogger
	GroupDataKeeper   types.EpochGroupDataKeeper
	GroupData         *types.EpochGroupData
}

func NewEpochGroup(
	group types.GroupMessageKeeper,
	participant types.ParticipantKeeper,
	authority string,
	logger types.InferenceLogger,
	groupDataKeeper types.EpochGroupDataKeeper,
	groupData *types.EpochGroupData,
) *EpochGroup {
	return &EpochGroup{
		GroupKeeper:       group,
		ParticipantKeeper: participant,
		Authority:         authority,
		Logger:            logger,
		GroupDataKeeper:   groupDataKeeper,
		GroupData:         groupData,
	}
}

func (eg *EpochGroup) CreateGroup(ctx context.Context) error {
	votingPeriod := 4 * time.Minute
	minExecutionPeriod := 0 * time.Minute

	groupMsg := &group.MsgCreateGroupWithPolicy{
		Admin:   eg.Authority,
		Members: []group.MemberRequest{},
	}
	policy := group.NewPercentageDecisionPolicy(
		"0.50",
		votingPeriod,
		minExecutionPeriod,
	)
	err := groupMsg.SetDecisionPolicy(policy)
	if err != nil {
		eg.Logger.LogError("Error setting decision policy", "error", err)
		return err
	}

	result, err := eg.GroupKeeper.CreateGroupWithPolicy(ctx, groupMsg)
	if err != nil {
		eg.Logger.LogError("Error creating group", "error", err)
		return err
	}
	eg.GroupData.EpochGroupId = result.GroupId
	eg.GroupData.EpochPolicy = result.GroupPolicyAddress
	eg.GroupDataKeeper.SetEpochGroupData(ctx, *eg.GroupData)

	eg.Logger.LogInfo("Created group", "groupID", result.GroupId, "policyAddress", result.GroupPolicyAddress)
	return nil
}

func (eg *EpochGroup) AddMember(ctx context.Context, address string, weight uint64, pubkey string) error {
	return eg.updateMember(ctx, address, weight, pubkey)
}

func (eg *EpochGroup) MarkChanged(ctx context.Context) error {
	return eg.updateMetadata(ctx, "changed")
}

func (eg *EpochGroup) MarkUnchanged(ctx context.Context) error {
	return eg.updateMetadata(ctx, "unchanged")
}

func (eg *EpochGroup) IsChanged(ctx context.Context) bool {
	info, err := eg.GroupKeeper.GroupInfo(ctx, &group.QueryGroupInfoRequest{
		GroupId: eg.GroupData.EpochGroupId,
	})
	if err != nil {
		eg.Logger.LogError("Error getting group info", "error", err)
		return false
	}
	return info.Info.Metadata == "changed"
}

func (eg *EpochGroup) updateMetadata(ctx context.Context, metadata string) error {
	_, err := eg.GroupKeeper.UpdateGroupMetadata(ctx, &group.MsgUpdateGroupMetadata{
		Admin:    eg.Authority,
		GroupId:  eg.GroupData.EpochGroupId,
		Metadata: metadata,
	})
	return err
}

func (eg *EpochGroup) updateMember(ctx context.Context, address string, weight uint64, pubkey string) error {
	_, err := eg.GroupKeeper.UpdateGroupMembers(ctx, &group.MsgUpdateGroupMembers{
		Admin:   eg.Authority,
		GroupId: eg.GroupData.EpochGroupId,
		MemberUpdates: []group.MemberRequest{
			{
				Address:  address,
				Weight:   strconv.FormatUint(weight, 10),
				Metadata: pubkey,
			},
		},
	})
	if err == nil {
		err = eg.MarkChanged(ctx)
	}
	return err
}

func (eg *EpochGroup) UpdateMember(ctx context.Context, previousVersion *types.Participant, currentVersion *types.Participant) error {
	if previousVersion != nil && previousVersion.Status != currentVersion.Status {
		if currentVersion.Status == types.ParticipantStatus_INVALID {
			// Effectively delete the member
			return eg.updateMember(ctx, currentVersion.Address, 0, "")
		}
	}
	return nil
}

func (eg *EpochGroup) GetComputeResults(ctx context.Context) ([]keeper.ComputeResult, error) {
	members, err := eg.GroupKeeper.GroupMembers(ctx, &group.QueryGroupMembersRequest{
		GroupId: eg.GroupData.EpochGroupId,
	})
	if err != nil {
		eg.Logger.LogError("Error getting group members", "error", err)
		return nil, err
	}

	var computeResults []keeper.ComputeResult

	for _, member := range members.Members {
		pubKeyBytes, err := base64.StdEncoding.DecodeString(member.Member.Metadata)
		if err != nil {
			eg.Logger.LogError("Error decoding pubkey", "error", err)
			continue
		}

		pubKey := ed25519.PubKey{Key: pubKeyBytes}

		computeResults = append(computeResults, keeper.ComputeResult{
			Power:           getWeight(member),
			ValidatorPubKey: &pubKey,
			OperatorAddress: member.Member.Address,
		})
	}

	return computeResults, nil
}
