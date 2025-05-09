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
	// In-memory map to find sub-groups by model ID
	// This is not serialized in the chain state
	subGroups map[string]*EpochGroup
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
		subGroups:         make(map[string]*EpochGroup),
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

func (eg *EpochGroup) AddMember(ctx context.Context, address string, weight int64, pubkey string, seedSignature string, reputation int64, models []string) error {

	// Check if this is a sub-group
	if eg.GroupData.GetModelId() != "" {
		// This is a sub-group, only add the member if the model matches
		if len(models) == 0 {
			return nil
		}
		modelId := eg.GroupData.GetModelId()
		found := false
		for _, model := range models {
			if modelId == model {
				found = true
				break
			}
		}
		if !found {
			eg.Logger.LogInfo("Skipping member", types.EpochGroup, "address", address, "models", models, "groupModel", modelId)
			return nil
		}
	}
	eg.Logger.LogInfo("Adding member", types.EpochGroup, "address", address, "weight", weight, "pubkey", pubkey, "seedSignature", seedSignature, "models", models)

	val, found := eg.GroupDataKeeper.GetEpochGroupData(ctx, eg.GroupData.PocStartBlockHeight, eg.GroupData.ModelId)
	if !found {
		eg.Logger.LogError("Epoch group not found", types.EpochGroup, "blockHeight", eg.GroupData.PocStartBlockHeight, "modelId", eg.GroupData.ModelId)
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
	err := eg.updateMember(ctx, address, weight, pubkey)
	if err != nil {
		return err
	}

	// If this is the parent group, add the member to sub-groups for each model
	// Only do this if we're not already processing a sub-group (to prevent recursion)
	if eg.GroupData.GetModelId() == "" && len(models) > 0 {
		for _, model := range models {
			eg.Logger.LogInfo("Adding member to sub-group", types.EpochGroup, "model", model, "address", address)
			subGroup, err := eg.GetSubGroup(ctx, model)
			if err != nil {
				eg.Logger.LogError("Error getting sub-group", types.EpochGroup, "error", err, "model", model)
				continue
			}

			// Add the member to the sub-group with the same weight, pubkey, etc.
			// We're explicitly passing only this model to prevent further recursion
			modelArray := []string{model}
			err = subGroup.AddMember(ctx, address, weight, pubkey, seedSignature, reputation, modelArray)
			if err != nil {
				eg.Logger.LogError("Error adding member to sub-group", types.EpochGroup, "error", err, "model", model)
			}
		}
	}

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
	members, err := eg.GetGroupMembers(ctx)
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

func (eg *EpochGroup) GetGroupMembers(ctx context.Context) ([]*group.GroupMember, error) {
	members, err := eg.GroupKeeper.GroupMembers(ctx, &group.QueryGroupMembersRequest{
		GroupId: eg.GroupData.EpochGroupId,
	})
	if err != nil {
		eg.Logger.LogError("Error getting group members", types.EpochGroup, "error", err)
		return nil, err
	}
	return members.Members, nil
}

// CreateSubGroup creates a new sub-group for a specific model
func (eg *EpochGroup) CreateSubGroup(ctx context.Context, modelId string) (*EpochGroup, error) {
	// Check if this is already a sub-group
	if eg.GroupData.GetModelId() != "" {
		return nil, types.ErrCannotCreateSubGroupFromSubGroup
	}

	// Check if we already have a sub-group for this model in memory
	if subGroup, ok := eg.subGroups[modelId]; ok {
		eg.Logger.LogInfo("Found existing sub-group in memory", types.EpochGroup, "modelId", modelId, "groupID", subGroup.GroupData.EpochGroupId, "height", subGroup.GroupData.PocStartBlockHeight)
		return subGroup, nil
	}

	// Check if we already have a sub-group for this model in the chain state
	for _, height := range eg.GroupData.GetSubGroupHeights() {
		subGroupData, found := eg.GroupDataKeeper.GetEpochGroupData(ctx, height, modelId)
		if found {
			eg.Logger.LogInfo("Found existing sub-group in state", types.EpochGroup, "modelId", modelId, "groupID", subGroupData.EpochGroupId, "height", height)
			// Create an EpochGroup for the sub-group
			subGroup := NewEpochGroup(
				eg.GroupKeeper,
				eg.ParticipantKeeper,
				eg.Authority,
				eg.Logger,
				eg.GroupDataKeeper,
				&subGroupData,
			)
			// Add it to the in-memory map
			eg.subGroups[modelId] = subGroup
			return subGroup, nil
		}
	}

	// Create a new EpochGroupData for the sub-group
	// Use a different PocStartBlockHeight to avoid conflicts
	// We'll use the parent's PocStartBlockHeight + the length of the sub-group heights + 1
	// This ensures a unique height for each sub-group
	subGroupHeight := eg.GroupData.PocStartBlockHeight + uint64(len(eg.GroupData.SubGroupHeights)) + 1
	subGroupData := &types.EpochGroupData{
		PocStartBlockHeight: subGroupHeight,
		ModelId:             modelId,
	}

	// Create a new EpochGroup for the sub-group
	subGroup := NewEpochGroup(
		eg.GroupKeeper,
		eg.ParticipantKeeper,
		eg.Authority,
		eg.Logger,
		eg.GroupDataKeeper,
		subGroupData,
	)

	// Create the group in the chain
	err := subGroup.CreateGroup(ctx)
	if err != nil {
		return nil, err
	}

	// Add the sub-group to the parent's list of sub-groups
	eg.GroupData.SubGroupHeights = append(eg.GroupData.SubGroupHeights, subGroupData.PocStartBlockHeight)
	eg.GroupDataKeeper.SetEpochGroupData(ctx, *eg.GroupData)

	// Add the sub-group to the in-memory map
	eg.subGroups[modelId] = subGroup

	eg.Logger.LogInfo("Created sub-group", types.EpochGroup, "modelId", modelId, "groupID", subGroupData.EpochGroupId, "height", subGroupHeight)
	return subGroup, nil
}

// GetSubGroup gets a sub-group for a specific model, creating it if it doesn't exist
func (eg *EpochGroup) GetSubGroup(ctx context.Context, modelId string) (*EpochGroup, error) {
	// Check if this is already a sub-group
	if eg.GroupData.GetModelId() != "" {
		return nil, types.ErrCannotGetSubGroupFromSubGroup
	}

	// Use CreateSubGroup which now handles checking for existing sub-groups
	// and creates a new one only if needed
	return eg.CreateSubGroup(ctx, modelId)
}
