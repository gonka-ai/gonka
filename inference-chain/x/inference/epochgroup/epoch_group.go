package epochgroup

import (
	"context"
	"encoding/base64"
	"strconv"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
)

// EpochMember contains all the parameters related to a member in an epoch group
type EpochMember struct {
	Address            string
	Weight             int64
	Pubkey             string
	SeedSignature      string
	Reputation         int64
	Models             []string
	MlNodes            []*types.ModelMLNodes
	ConfirmationWeight int64 // Minimum confirmation weight from confirmation PoC events
}

func NewEpochMemberFromActiveParticipant(p *types.ActiveParticipant, reputation int64, confirmationWeight int64) EpochMember {
	seedSignature := ""
	if p.Seed != nil {
		seedSignature = p.Seed.Signature
	}

	// If confirmation weight is not provided (0), initialize it from inference-serving nodes (POC_SLOT=false)
	// This is the baseline weight that can be verified through confirmation PoC
	if confirmationWeight == 0 {
		confirmationWeight = calculateInferenceServingWeight(p.MlNodes)
	}

	return EpochMember{
		Address:            p.Index,
		Weight:             p.Weight,
		Pubkey:             p.ValidatorKey,
		SeedSignature:      seedSignature,
		Reputation:         reputation,
		Models:             p.Models,
		MlNodes:            p.MlNodes,
		ConfirmationWeight: confirmationWeight,
	}
}

// calculateInferenceServingWeight calculates the total weight of nodes serving inference (POC_SLOT=false)
func calculateInferenceServingWeight(mlNodes []*types.ModelMLNodes) int64 {
	totalWeight := int64(0)

	for _, modelNodes := range mlNodes {
		if modelNodes == nil {
			continue
		}

		for _, node := range modelNodes.MlNodes {
			if node == nil {
				continue
			}

			// POC_SLOT is at index 1 (second timeslot)
			// false => participant in PoC phase, true => continues inference during PoC
			if len(node.TimeslotAllocation) > 1 && !node.TimeslotAllocation[1] {
				totalWeight += node.PocWeight
			}
		}
	}

	return totalWeight
}

func NewEpochMemberFromStakingValidator(
	validator stakingtypes.Validator,
) (*EpochMember, error) {
	accAddr, err := utils.OperatorAddressToAccAddress(validator.OperatorAddress)
	if err != nil {
		return nil, err
	}

	pubKey := validator.ConsensusPubkey.String()

	return &EpochMember{
		Address:       accAddr,
		Weight:        validator.Tokens.Int64(),
		Pubkey:        pubKey,
		SeedSignature: "",
		Reputation:    1,
		Models:        []string{},
	}, nil
}

type EpochGroup struct {
	GroupKeeper        types.GroupMessageKeeper
	ParticipantKeeper  types.ParticipantKeeper
	ModelKeeper        types.ModelKeeper
	HardwareNodeKeeper types.HardwareNodeKeeper
	Authority          string
	Logger             types.InferenceLogger
	GroupDataKeeper    types.EpochGroupDataKeeper
	GroupData          *types.EpochGroupData
	// In-memory map to find sub-groups by model ID
	// This is not serialized in the chain state
	subGroups map[string]*EpochGroup
}

func NewEpochGroup(
	group types.GroupMessageKeeper,
	participant types.ParticipantKeeper,
	modelKeeper types.ModelKeeper,
	hardwareNodeKeeper types.HardwareNodeKeeper,
	authority string,
	logger types.InferenceLogger,
	groupDataKeeper types.EpochGroupDataKeeper,
	groupData *types.EpochGroupData,
) *EpochGroup {
	return &EpochGroup{
		GroupKeeper:        group,
		ParticipantKeeper:  participant,
		ModelKeeper:        modelKeeper,
		HardwareNodeKeeper: hardwareNodeKeeper,
		Authority:          authority,
		Logger:             logger,
		GroupDataKeeper:    groupDataKeeper,
		GroupData:          groupData,
		subGroups:          make(map[string]*EpochGroup),
	}
}

func (eg *EpochGroup) CreateGroup(ctx context.Context) error {
	votingPeriod := 4 * time.Minute
	minExecutionPeriod := 0 * time.Minute

	groupMsg := &group.MsgCreateGroupWithPolicy{
		Admin:         eg.Authority,
		Members:       []group.MemberRequest{},
		GroupMetadata: eg.GroupData.ModelId,
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

func (eg *EpochGroup) AddMember(ctx context.Context, member EpochMember) error {
	if eg.GroupData.IsModelGroup() {
		if !eg.memberSupportsModel(member.Models) {
			eg.Logger.LogInfo("Skipping member", types.EpochGroup, "address", member.Address, "models", member.Models, "groupModel", eg.GroupData.ModelId)
			return nil
		}
	}

	eg.Logger.LogInfo("Adding member", types.EpochGroup, "address", member.Address, "weight", member.Weight, "pubkey", member.Pubkey, "seedSignature", member.SeedSignature, "models", member.Models)
	val, found := eg.GroupDataKeeper.GetEpochGroupData(ctx, eg.GroupData.EpochIndex, eg.GroupData.ModelId)
	if !found {
		eg.Logger.LogError("Epoch group not found", types.EpochGroup, "blockHeight", eg.GroupData.PocStartBlockHeight, "modelId", eg.GroupData.ModelId)
		return types.ErrCurrentEpochGroupNotFound
	}

	eg.updateEpochGroupWithNewMember(ctx, member, val)
	err := eg.updateMember(ctx, member.Address, member.Weight, member.Pubkey)
	if err != nil {
		return err
	}

	if !eg.GroupData.IsModelGroup() && len(member.Models) > 0 {
		eg.addToModelGroups(ctx, member)
	}

	return nil
}

func (eg *EpochGroup) updateEpochGroupWithNewMember(ctx context.Context, member EpochMember, val types.EpochGroupData) {
	eg.GroupData = &val
	if eg.GroupData.MemberSeedSignatures == nil {
		eg.GroupData.MemberSeedSignatures = []*types.SeedSignature{}
	}
	eg.GroupData.MemberSeedSignatures = append(eg.GroupData.MemberSeedSignatures, &types.SeedSignature{
		MemberAddress: member.Address,
		Signature:     member.SeedSignature,
	})

	mlNodes := eg.getMLNodeInfo(member, eg.GroupData.ModelId)

	eg.GroupData.ValidationWeights = append(eg.GroupData.ValidationWeights, &types.ValidationWeight{
		MemberAddress:      member.Address,
		Weight:             int64(member.Weight),
		Reputation:         int32(member.Reputation),
		MlNodes:            mlNodes,
		ConfirmationWeight: member.ConfirmationWeight, // Populated by confirmation PoC weight calculation
	})
	eg.GroupData.TotalWeight += member.Weight

	totalThroughput := int64(0)
	for _, node := range mlNodes {
		totalThroughput += node.Throughput
	}
	eg.GroupData.TotalThroughput += totalThroughput

	eg.GroupDataKeeper.SetEpochGroupData(ctx, *eg.GroupData)
}

func (eg *EpochGroup) getMLNodeInfo(member EpochMember, modelId string) []*types.MLNodeInfo {
	if modelId == "" {
		return nil // Do not store ML nodes in the parent group
	}

	// Find the index of the modelId in member.Models
	modelIndex := -1
	for i, model := range member.Models {
		if model == modelId {
			modelIndex = i
			break
		}
	}

	// Return the MLNodeInfo objects from the corresponding model index array
	if modelIndex >= 0 && modelIndex < len(member.MlNodes) {
		modelMLNodes := member.MlNodes[modelIndex]
		return modelMLNodes.MlNodes
	}

	return nil
}

func (eg *EpochGroup) addToModelGroups(ctx context.Context, member EpochMember) {
	for _, modelId := range member.Models {
		eg.Logger.LogInfo("Adding member to sub-group", types.EpochGroup, "model", modelId, "address", member.Address)

		subGroup, err := eg.getOrCreateSubGroup(ctx, modelId)
		if err != nil {
			eg.Logger.LogError("Error getting sub-group", types.EpochGroup, "error", err, "model", modelId)
			continue
		}

		// Add the member to the sub-group with the same weight, pubkey, etc.
		// We're explicitly passing only this model to prevent further recursion
		subMember := member
		subMember.Models = []string{modelId}

		// Find the model index and copy the corresponding MLNode array
		modelIndex := -1
		for i, model := range member.Models {
			if model == modelId {
				modelIndex = i
				break
			}
		}

		// Copy only the MLNode array for this specific model
		if modelIndex >= 0 && modelIndex < len(member.MlNodes) {
			subMember.MlNodes = []*types.ModelMLNodes{member.MlNodes[modelIndex]}
		} else {
			subMember.MlNodes = []*types.ModelMLNodes{}
		}

		err = subGroup.AddMember(ctx, subMember)
		if err != nil {
			eg.Logger.LogError("Error adding member to sub-group", types.EpochGroup, "error", err, "model", modelId)
		}
	}
}

func (eg *EpochGroup) memberSupportsModel(models []string) bool {
	modelId := eg.GroupData.GetModelId()
	for _, model := range models {
		if modelId == model {
			return true
		}
	}
	return false
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
	if eg.GroupData.ModelId != "" {
		// Skip for subgroups, changed only applies to the parent group
		return nil
	}
	err := eg.updateMetadata(ctx, "changed")
	return err
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

func (eq *EpochGroup) RemoveMember(ctx context.Context, participant *types.Participant) error {
	err := eq.updateMember(ctx, participant.Address, 0, "")
	if err != nil {
		return err
	}
	for _, model := range eq.GroupData.GetSubGroupModels() {
		subGroup, err := eq.GetSubGroup(ctx, model)
		if err != nil {
			eq.Logger.LogError("Error getting sub-group", types.EpochGroup, "error", err, "model", model)
			continue
		}
		err = subGroup.RemoveMember(ctx, participant)
		if err != nil {
			eq.Logger.LogError("Error removing member from sub-group", types.EpochGroup, "error", err, "model", model)
		}
		// for sub groups, continue on and remove as much as we can
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
		// The VALIDATOR key (ed25519), never to be confused with the account key (secp256k1 key)
		pubKey := ed25519.PubKey{Key: pubKeyBytes}

		accAddr, err := sdk.AccAddressFromBech32(member.Member.Address)
		if err != nil {
			eg.Logger.LogError("Error decoding account address", types.EpochGroup, "error", err)
			continue
		}
		valOperatorAddr := sdk.ValAddress(accAddr).String()

		computeResults = append(computeResults, keeper.ComputeResult{
			Power:           getWeight(member),
			ValidatorPubKey: &pubKey,
			OperatorAddress: valOperatorAddr,
		})
	}

	return computeResults, nil
}

func (eg *EpochGroup) GetGroupMembers(ctx context.Context) ([]*group.GroupMember, error) {
	members, err := eg.getAllGroupMembersPaginated(ctx, eg.GroupData.EpochGroupId)
	if err != nil {
		eg.Logger.LogError("Error getting group members", types.EpochGroup, "error", err)
		return nil, err
	}
	return members, nil
}

// getAllGroupMembersPaginated fetches all group members using pagination
func (eg *EpochGroup) getAllGroupMembersPaginated(ctx context.Context, groupId uint64) ([]*group.GroupMember, error) {
	var allMembers []*group.GroupMember
	var nextKey []byte

	for {
		resp, err := eg.GroupKeeper.GroupMembers(ctx, &group.QueryGroupMembersRequest{
			GroupId: groupId,
			Pagination: &query.PageRequest{
				Key:   nextKey,
				Limit: 100,
			},
		})
		if err != nil {
			return nil, err
		}

		allMembers = append(allMembers, resp.Members...)

		if resp.Pagination == nil || len(resp.Pagination.NextKey) == 0 {
			break
		}
		nextKey = resp.Pagination.NextKey
	}

	return allMembers, nil
}

// CreateSubGroup creates a new sub-group for a specific model
func (eg *EpochGroup) CreateSubGroup(ctx context.Context, model *types.Model) (*EpochGroup, error) {
	// Check if this is already a sub-group
	if eg.GroupData.IsModelGroup() {
		return nil, types.ErrCannotCreateSubGroupFromSubGroup
	}

	epochGroup := eg.getGroupFromMemory(model.Id)
	if epochGroup != nil {
		return epochGroup, nil
	}

	epochGroup = eg.getGroupFromState(ctx, model.Id)
	if epochGroup != nil {
		return epochGroup, nil
	}

	return eg.createNewEpochSubGroup(ctx, model)
}

func (eg *EpochGroup) createNewEpochSubGroup(ctx context.Context, model *types.Model) (*EpochGroup, error) {
	subGroupData := &types.EpochGroupData{
		PocStartBlockHeight: eg.GroupData.PocStartBlockHeight,
		ModelId:             model.Id,
		ModelSnapshot:       model,
		EpochGroupId:        eg.GroupData.EpochGroupId,
		EpochIndex:          eg.GroupData.EpochIndex,
	}

	// Create a new EpochGroup for the sub-group
	subGroup := NewEpochGroup(
		eg.GroupKeeper,
		eg.ParticipantKeeper,
		eg.ModelKeeper,
		eg.HardwareNodeKeeper,
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
	eg.GroupData.SubGroupModels = append(eg.GroupData.SubGroupModels, model.Id)
	eg.GroupDataKeeper.SetEpochGroupData(ctx, *eg.GroupData)

	// Add the sub-group to the in-memory map
	eg.subGroups[model.Id] = subGroup

	eg.Logger.LogInfo("Created sub-group", types.EpochGroup, "modelId", model.Id, "groupID", subGroupData.EpochGroupId, "height", eg.GroupData.PocStartBlockHeight)
	return subGroup, nil
}

func (eg *EpochGroup) getGroupFromMemory(modelId string) *EpochGroup {
	if subGroup, ok := eg.subGroups[modelId]; ok {
		eg.Logger.LogInfo("Found existing sub-group in memory", types.EpochGroup, "modelId", modelId, "groupID", subGroup.GroupData.EpochGroupId, "height", subGroup.GroupData.PocStartBlockHeight)
		return subGroup
	}
	return nil
}

func (eg *EpochGroup) getGroupFromState(ctx context.Context, modelId string) *EpochGroup {
	for _, model := range eg.GroupData.GetSubGroupModels() {
		if model == modelId {
			subGroupData, found := eg.GroupDataKeeper.GetEpochGroupData(ctx, eg.GroupData.EpochIndex, modelId)
			if found {
				eg.Logger.LogInfo("Found existing sub-group in state", types.EpochGroup, "modelId", modelId, "groupID", subGroupData.EpochGroupId, "height", eg.GroupData.PocStartBlockHeight)
				subGroup := NewEpochGroup(
					eg.GroupKeeper,
					eg.ParticipantKeeper,
					eg.ModelKeeper,
					eg.HardwareNodeKeeper,
					eg.Authority,
					eg.Logger,
					eg.GroupDataKeeper,
					&subGroupData,
				)
				// Add it to the in-memory map
				eg.subGroups[modelId] = subGroup
				return subGroup
			}
		}
	}
	return nil
}

// GetSubGroup gets a sub-group for a specific model, but does not create it if it doesn't exist.
func (eg *EpochGroup) GetSubGroup(ctx context.Context, modelId string) (*EpochGroup, error) {
	// Check if this is already a sub-group
	if eg.GroupData.GetModelId() != "" {
		return nil, types.ErrCannotGetSubGroupFromSubGroup
	}

	epochGroup := eg.getGroupFromMemory(modelId)
	if epochGroup != nil {
		return epochGroup, nil
	}

	epochGroup = eg.getGroupFromState(ctx, modelId)
	if epochGroup != nil {
		return epochGroup, nil
	}

	return nil, types.ErrEpochGroupDataNotFound
}

// getOrCreateSubGroup gets a sub-group for a specific model, creating it if it doesn't exist
func (eg *EpochGroup) getOrCreateSubGroup(ctx context.Context, modelId string) (*EpochGroup, error) {
	subGroup, err := eg.GetSubGroup(ctx, modelId)
	if err == nil {
		return subGroup, nil
	}

	// If the error is anything other than not found, return the error
	if err != types.ErrEpochGroupDataNotFound {
		return nil, err
	}

	// The subgroup was not found, so we create it
	model, found := eg.ModelKeeper.GetGovernanceModel(ctx, modelId)
	if !found {
		eg.Logger.LogError("Error getting model for sub-group", types.EpochGroup, "error", "model not found", "model", modelId)
		return nil, types.ErrInvalidModel
	}

	return eg.CreateSubGroup(ctx, model)
}
