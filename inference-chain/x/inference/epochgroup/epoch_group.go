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
	ModelKeeper       types.ModelKeeper
	GroupData         *types.EpochGroupData
}

func NewEpochGroup(
	group types.GroupMessageKeeper,
	participant types.ParticipantKeeper,
	authority string,
	logger types.InferenceLogger,
	groupDataKeeper types.EpochGroupDataKeeper,
	modelKeeper types.ModelKeeper,
	groupData *types.EpochGroupData,
) *EpochGroup {
	return &EpochGroup{
		GroupKeeper:       group,
		ParticipantKeeper: participant,
		Authority:         authority,
		Logger:            logger,
		GroupDataKeeper:   groupDataKeeper,
		ModelKeeper:       modelKeeper,
		GroupData:         groupData,
	}
}

func (eg *EpochGroup) CreateGroup(ctx context.Context) error {
	votingPeriod := 4 * time.Minute
	minExecutionPeriod := 0 * time.Minute

	// Create the main EpochGroup
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

	// Initialize the model_epoch_groups field if it's nil
	if eg.GroupData.ModelEpochGroups == nil {
		eg.GroupData.ModelEpochGroups = []*types.ModelEpochGroup{}
	}

	// Get all models
	models, err := eg.ModelKeeper.GetAllModels(ctx)
	if err != nil {
		eg.Logger.LogError("Error getting models", types.EpochGroup, "error", err)
		// Continue with the main group even if we can't get models
	} else {
		// Create a nested EpochGroup for each model
		for _, model := range models {
			err = eg.CreateModelEpochGroup(ctx, model.Id)
			if err != nil {
				eg.Logger.LogError("Error creating model epoch group", types.EpochGroup, "model", model.Id, "error", err)
				// Continue with other models even if one fails
			}
		}
	}

	eg.GroupDataKeeper.SetEpochGroupData(ctx, *eg.GroupData)

	eg.Logger.LogInfo("Created group", types.EpochGroup, "groupID", result.GroupId, "policyAddress", result.GroupPolicyAddress)
	return nil
}

// CreateModelEpochGroup creates a nested EpochGroup for a specific model
func (eg *EpochGroup) CreateModelEpochGroup(ctx context.Context, modelId string) error {
	votingPeriod := 4 * time.Minute
	minExecutionPeriod := 0 * time.Minute

	// Create a new group for the model
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
		eg.Logger.LogError("Error setting decision policy for model", types.EpochGroup, "model", modelId, "error", err)
		return err
	}

	result, err := eg.GroupKeeper.CreateGroupWithPolicy(ctx, groupMsg)
	if err != nil {
		eg.Logger.LogError("Error creating group for model", types.EpochGroup, "model", modelId, "error", err)
		return err
	}

	// Create a new ModelEpochGroup
	modelEpochGroup := &types.ModelEpochGroup{
		ModelId:           modelId,
		EpochGroupId:      result.GroupId,
		EpochPolicy:       result.GroupPolicyAddress,
		ValidationWeights: []*types.ValidationWeight{},
		TotalWeight:       0,
	}

	// Add the ModelEpochGroup to the main EpochGroup
	eg.GroupData.ModelEpochGroups = append(eg.GroupData.ModelEpochGroups, modelEpochGroup)

	eg.Logger.LogInfo("Created model epoch group", types.EpochGroup, "model", modelId, "groupID", result.GroupId, "policyAddress", result.GroupPolicyAddress)
	return nil
}

func (eg *EpochGroup) AddMember(ctx context.Context, p *types.ActiveParticipant, reputation int64) error {
	address := p.Index
	weight := p.Weight
	pubkey := p.ValidatorKey
	seedSignature := ""
	if p.Seed != nil {
		seedSignature = p.Seed.Signature
	}

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

	// Add member to model-specific EpochGroups based on the models they support
	if len(p.Models) > 0 {
		for _, modelId := range p.Models {
			// Find the ModelEpochGroup for this model
			var modelEpochGroup *types.ModelEpochGroup
			for _, meg := range eg.GroupData.ModelEpochGroups {
				if meg.ModelId == modelId {
					modelEpochGroup = meg
					break
				}
			}

			// If the ModelEpochGroup doesn't exist, create it
			if modelEpochGroup == nil {
				err := eg.CreateModelEpochGroup(ctx, modelId)
				if err != nil {
					eg.Logger.LogError("Error creating model epoch group", types.EpochGroup, "model", modelId, "error", err)
					continue
				}
				// Get the newly created ModelEpochGroup
				for _, meg := range eg.GroupData.ModelEpochGroups {
					if meg.ModelId == modelId {
						modelEpochGroup = meg
						break
					}
				}
			}

			// Add the member to the ModelEpochGroup
			if modelEpochGroup != nil {
				modelEpochGroup.ValidationWeights = append(modelEpochGroup.ValidationWeights, &types.ValidationWeight{
					MemberAddress: address,
					Weight:        int64(weight),
					Reputation:    int32(reputation),
				})
				modelEpochGroup.TotalWeight += weight

				// Update the member in the model-specific group
				err := eg.updateModelMember(ctx, modelEpochGroup.EpochGroupId, address, weight, pubkey)
				if err != nil {
					eg.Logger.LogError("Error updating model member", types.EpochGroup, "model", modelId, "address", address, "error", err)
				}
			}
		}
	}

	eg.GroupDataKeeper.SetEpochGroupData(ctx, *eg.GroupData)
	return eg.updateMember(ctx, address, weight, pubkey)
}

// updateModelMember updates a member in a model-specific group
func (eg *EpochGroup) updateModelMember(ctx context.Context, groupId uint64, address string, weight int64, pubkey string) error {
	_, err := eg.GroupKeeper.UpdateGroupMembers(ctx, &group.MsgUpdateGroupMembers{
		Admin:   eg.Authority,
		GroupId: groupId,
		MemberUpdates: []group.MemberRequest{
			{
				Address:  address,
				Weight:   strconv.FormatInt(weight, 10),
				Metadata: pubkey,
			},
		},
	})
	return err
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

// GetModelValidationWeights returns the validation weights for a specific model
func (eg *EpochGroup) GetModelValidationWeights(modelId string) (VotingData, error) {
	var totalWeight int64
	var votingMembers = make(map[string]int64)

	// Find the ModelEpochGroup for this model
	var modelEpochGroup *types.ModelEpochGroup
	for _, meg := range eg.GroupData.ModelEpochGroups {
		if meg.ModelId == modelId {
			modelEpochGroup = meg
			break
		}
	}

	// If the ModelEpochGroup doesn't exist, return empty VotingData
	if modelEpochGroup == nil {
		return VotingData{
			TotalWeight: 0,
			Members:     votingMembers,
		}, nil
	}

	// Get the validation weights from the ModelEpochGroup
	for _, member := range modelEpochGroup.ValidationWeights {
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
