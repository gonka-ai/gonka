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
		eg.Logger.LogError("Error setting decision policy", types.EpochGroup, "error", err)
		return err
	}

	result, err := eg.GroupKeeper.CreateGroupWithPolicy(ctx, groupMsg)
	if err != nil {
		eg.Logger.LogError("Error creating group", types.EpochGroup, "error", err)
		return err
	}
	eg.GroupData.EpochGroupId = result.GroupId
	eg.GroupData.EpochPolicy = result.GroupPolicyAddress
	eg.GroupDataKeeper.SetEpochGroupData(ctx, *eg.GroupData)

	eg.Logger.LogInfo("Created group", types.EpochGroup, "groupID", result.GroupId, "policyAddress", result.GroupPolicyAddress)
	return nil
}

// CreateModelEpochGroups creates nested EpochGroups for each model
func (eg *EpochGroup) CreateModelEpochGroups(ctx context.Context, models []*types.Model) error {
	// Initialize model_epoch_groups if nil
	if eg.GroupData.ModelEpochGroups == nil {
		eg.GroupData.ModelEpochGroups = []*types.ModelEpochGroup{}
	}

	for _, model := range models {
		// Create a new group for this model
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
			eg.Logger.LogError("Error setting decision policy for model group", types.EpochGroup, "error", err, "model", model.Id)
			return err
		}

		result, err := eg.GroupKeeper.CreateGroupWithPolicy(ctx, groupMsg)
		if err != nil {
			eg.Logger.LogError("Error creating group for model", types.EpochGroup, "error", err, "model", model.Id)
			return err
		}

		// Create a new ModelEpochGroup
		modelGroup := &types.ModelEpochGroup{
			ModelId:           model.Id,
			EpochGroupId:      result.GroupId,
			EpochPolicy:       result.GroupPolicyAddress,
			ValidationWeights: []*types.ValidationWeight{},
			TotalWeight:       0,
		}

		// Add to the list of model epoch groups
		eg.GroupData.ModelEpochGroups = append(eg.GroupData.ModelEpochGroups, modelGroup)
		eg.GroupDataKeeper.SetEpochGroupData(ctx, *eg.GroupData)

		eg.Logger.LogInfo("Created model group", types.EpochGroup, "modelID", model.Id, "groupID", result.GroupId, "policyAddress", result.GroupPolicyAddress)
	}

	return nil
}

// GetModelEpochGroup returns the ModelEpochGroup for the specified model
func (eg *EpochGroup) GetModelEpochGroup(modelId string) *types.ModelEpochGroup {
	if eg.GroupData.ModelEpochGroups == nil {
		return nil
	}

	for _, modelGroup := range eg.GroupData.ModelEpochGroups {
		if modelGroup.ModelId == modelId {
			return modelGroup
		}
	}

	return nil
}

func (eg *EpochGroup) AddMember(ctx context.Context, address string, weight int64, pubkey string, seedSignature string, reputation int64) error {
	eg.Logger.LogInfo("Adding member", types.EpochGroup, "address", address, "weight", weight, "pubkey", pubkey, "seedSignature", seedSignature)
	val, found := eg.GroupDataKeeper.GetEpochGroupData(ctx, eg.GroupData.PocStartBlockHeight)
	if !found {
		eg.Logger.LogError("Epoch group not found", types.EpochGroup, "blockHeight", eg.GroupData.PocStartBlockHeight)
		return types.ErrCurrentEpochGroupNotFound
	}
	eg.GroupData = &val
	if eg.GroupData.MemberSeedSignatures == nil {
		eg.GroupData.MemberSeedSignatures = []*types.SeedSignature{}
	}
	eg.GroupData.MemberSeedSignatures = append(eg.GroupData.MemberSeedSignatures, &types.SeedSignature{
		MemberAddress: address,
		Signature:     seedSignature,
	})
	eg.GroupData.ValidationWeights = append(eg.GroupData.ValidationWeights, &types.ValidationWeight{
		MemberAddress: address,
		Weight:        int64(weight),
		Reputation:    int32(reputation),
	})
	eg.GroupData.TotalWeight += weight
	eg.GroupDataKeeper.SetEpochGroupData(ctx, *eg.GroupData)
	return eg.updateMember(ctx, address, weight, pubkey)
}

// AddMemberToModelGroups adds a member to the appropriate model EpochGroups based on the models they support
func (eg *EpochGroup) AddMemberToModelGroups(ctx context.Context, address string, weight int64, pubkey string, models []string) error {
	if eg.GroupData.ModelEpochGroups == nil || len(eg.GroupData.ModelEpochGroups) == 0 {
		eg.Logger.LogInfo("No model epoch groups found, skipping model group member addition", types.EpochGroup)
		return nil
	}

	for _, modelId := range models {
		modelGroup := eg.GetModelEpochGroup(modelId)
		if modelGroup == nil {
			eg.Logger.LogInfo("Model epoch group not found, skipping", types.EpochGroup, "model", modelId)
			continue
		}

		// Add member to the validation weights for this model
		modelGroup.ValidationWeights = append(modelGroup.ValidationWeights, &types.ValidationWeight{
			MemberAddress: address,
			Weight:        int64(weight),
			Reputation:    0, // Start with 0 reputation in the model group
		})
		modelGroup.TotalWeight += weight

		// Update the group members
		_, err := eg.GroupKeeper.UpdateGroupMembers(ctx, &group.MsgUpdateGroupMembers{
			Admin:   eg.Authority,
			GroupId: modelGroup.EpochGroupId,
			MemberUpdates: []group.MemberRequest{
				{
					Address:  address,
					Weight:   strconv.FormatInt(weight, 10),
					Metadata: pubkey,
				},
			},
		})
		if err != nil {
			eg.Logger.LogError("Error updating model group members", types.EpochGroup, "error", err, "model", modelId)
			return err
		}

		eg.Logger.LogInfo("Added member to model group", types.EpochGroup, "address", address, "model", modelId)
	}

	// Save the updated group data
	eg.GroupDataKeeper.SetEpochGroupData(ctx, *eg.GroupData)
	return nil
}

type VotingData struct {
	TotalWeight int64
	Members     map[string]int64
}

func (eg *EpochGroup) GetValidationWeights() (VotingData, error) {
	var totalWeight int64
	var votingMembers = make(map[string]int64)
	for _, member := range eg.GroupData.ValidationWeights {
		weight := member.Weight
		totalWeight += weight
		votingMembers[member.MemberAddress] = weight
	}

	return VotingData{
		TotalWeight: totalWeight,
		Members:     votingMembers,
	}, nil
}

func (eg *EpochGroup) MarkChanged(ctx context.Context) error {
	return eg.updateMetadata(ctx, "changed")
}

func (eg *EpochGroup) MarkUnchanged(ctx context.Context) error {
	return eg.updateMetadata(ctx, "unchanged")
}

func (eg *EpochGroup) IsChanged(ctx context.Context) bool {
	if eg.GroupData.EpochGroupId == 0 {
		return false
	}
	info, err := eg.GroupKeeper.GroupInfo(ctx, &group.QueryGroupInfoRequest{
		GroupId: eg.GroupData.EpochGroupId,
	})
	if err != nil {
		eg.Logger.LogError("Error getting group info", types.EpochGroup, "error", err)
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

func (eg *EpochGroup) updateMember(ctx context.Context, address string, weight int64, pubkey string) error {
	_, err := eg.GroupKeeper.UpdateGroupMembers(ctx, &group.MsgUpdateGroupMembers{
		Admin:   eg.Authority,
		GroupId: eg.GroupData.EpochGroupId,
		MemberUpdates: []group.MemberRequest{
			{
				Address:  address,
				Weight:   strconv.FormatInt(weight, 10),
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
	members, err := eg.getGroupMembers(ctx)
	if err != nil {
		return nil, err
	}

	var computeResults []keeper.ComputeResult

	for _, member := range members {
		pubKeyBytes, err := base64.StdEncoding.DecodeString(member.Member.Metadata)
		if err != nil {
			eg.Logger.LogError("Error decoding pubkey", types.EpochGroup, "error", err)
			continue
		}
		// The VALIDATOR key, never to be confused with the account key (which is a sekp256k1 key)
		pubKey := ed25519.PubKey{Key: pubKeyBytes}

		computeResults = append(computeResults, keeper.ComputeResult{
			Power:           getWeight(member),
			ValidatorPubKey: &pubKey,
			OperatorAddress: member.Member.Address,
		})
	}

	return computeResults, nil
}

func (eg *EpochGroup) getGroupMembers(ctx context.Context) ([]*group.GroupMember, error) {
	members, err := eg.GroupKeeper.GroupMembers(ctx, &group.QueryGroupMembersRequest{
		GroupId: eg.GroupData.EpochGroupId,
	})
	if err != nil {
		eg.Logger.LogError("Error getting group members", types.EpochGroup, "error", err)
		return nil, err
	}
	return members.Members, nil
}
