package inference

import (
	"context"
	"strconv"
	"testing"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// setupEpochGroupDataFromAP populates root and subgroup EpochGroupData from an
// ActiveParticipants struct so that getEffectiveValidationBaseState (which reads
// from EpochGroupData filtered by SDK group membership) works in unit tests.
func setupEpochGroupDataFromAP(k keeper.Keeper, ctx sdk.Context, ap types.ActiveParticipants) {
	activeModels := map[string]bool{}
	rootWeights := make([]*types.ValidationWeight, 0, len(ap.Participants))
	subWeights := map[string][]*types.ValidationWeight{}
	for _, p := range ap.Participants {
		rootWeights = append(rootWeights, &types.ValidationWeight{
			MemberAddress: p.Index,
			Weight:        p.Weight,
		})
		for _, vp := range p.VotingPowers {
			activeModels[vp.ModelId] = true
			subWeights[vp.ModelId] = append(subWeights[vp.ModelId], &types.ValidationWeight{
				MemberAddress: p.Index,
				Weight:        p.Weight,
				VotingPower:   vp.VotingPower,
			})
		}
	}
	subGroupModels := make([]string, 0, len(activeModels))
	for m := range activeModels {
		subGroupModels = append(subGroupModels, m)
	}
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:        ap.EpochId,
		ModelId:           "",
		SubGroupModels:    subGroupModels,
		ValidationWeights: rootWeights,
	})
	for modelID, weights := range subWeights {
		k.SetEpochGroupData(ctx, types.EpochGroupData{
			EpochIndex:        ap.EpochId,
			ModelId:           modelID,
			ValidationWeights: weights,
		})
	}
}

// stubGroupKeeper is a minimal GroupMessageKeeper that returns all members from
// EpochGroupData.ValidationWeights as live SDK group members. Used in unit tests
// where the full SDK group module is not available.
// Set excludedMembers to simulate mid-epoch removals (member in ValidationWeights
// but absent from SDK group).
type stubGroupKeeper struct {
	keeper          keeper.Keeper
	excludedMembers map[string]bool
}

func (s *stubGroupKeeper) GroupMembers(ctx context.Context, req *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error) {
	// Find the EpochGroupData that has this group ID, return its ValidationWeights as members
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	members := s.findMembersForGroup(sdkCtx, req.GroupId)
	return &group.QueryGroupMembersResponse{Members: members}, nil
}

func (s *stubGroupKeeper) findMembersForGroup(ctx sdk.Context, groupId uint64) []*group.GroupMember {
	// Search across all EpochGroupData for a matching EpochGroupId
	effectiveIdx, found := s.keeper.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil
	}
	for _, modelId := range append([]string{""}, s.subGroupModelsForEpoch(ctx, effectiveIdx)...) {
		data, found := s.keeper.GetEpochGroupData(ctx, effectiveIdx, modelId)
		if !found || data.EpochGroupId != groupId {
			continue
		}
		var members []*group.GroupMember
		for _, vw := range data.ValidationWeights {
			if vw == nil || s.excludedMembers[vw.MemberAddress] {
				continue
			}
			members = append(members, &group.GroupMember{
				GroupId: groupId,
				Member: &group.Member{
					Address: vw.MemberAddress,
					Weight:  strconv.FormatInt(vw.Weight, 10),
				},
			})
		}
		return members
	}
	return nil
}

func (s *stubGroupKeeper) subGroupModelsForEpoch(ctx sdk.Context, epochIndex uint64) []string {
	root, found := s.keeper.GetEpochGroupData(ctx, epochIndex, "")
	if !found {
		return nil
	}
	return root.SubGroupModels
}

func (*stubGroupKeeper) CreateGroup(context.Context, *group.MsgCreateGroup) (*group.MsgCreateGroupResponse, error) {
	return &group.MsgCreateGroupResponse{}, nil
}
func (*stubGroupKeeper) CreateGroupWithPolicy(context.Context, *group.MsgCreateGroupWithPolicy) (*group.MsgCreateGroupWithPolicyResponse, error) {
	return &group.MsgCreateGroupWithPolicyResponse{}, nil
}
func (*stubGroupKeeper) UpdateGroupMembers(context.Context, *group.MsgUpdateGroupMembers) (*group.MsgUpdateGroupMembersResponse, error) {
	return &group.MsgUpdateGroupMembersResponse{}, nil
}
func (*stubGroupKeeper) UpdateGroupMetadata(context.Context, *group.MsgUpdateGroupMetadata) (*group.MsgUpdateGroupMetadataResponse, error) {
	return &group.MsgUpdateGroupMetadataResponse{}, nil
}
func (*stubGroupKeeper) SubmitProposal(context.Context, *group.MsgSubmitProposal) (*group.MsgSubmitProposalResponse, error) {
	return &group.MsgSubmitProposalResponse{}, nil
}
func (*stubGroupKeeper) Vote(context.Context, *group.MsgVote) (*group.MsgVoteResponse, error) {
	return &group.MsgVoteResponse{}, nil
}
func (*stubGroupKeeper) GroupInfo(context.Context, *group.QueryGroupInfoRequest) (*group.QueryGroupInfoResponse, error) {
	return &group.QueryGroupInfoResponse{}, nil
}
func (*stubGroupKeeper) ProposalsByGroupPolicy(context.Context, *group.QueryProposalsByGroupPolicyRequest) (*group.QueryProposalsByGroupPolicyResponse, error) {
	return &group.QueryProposalsByGroupPolicyResponse{}, nil
}

func TestBuildBootstrapDelegationSnapshot_FiltersActiveParticipantsOnly(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "active-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "new-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	params.DelegationParams = &types.DelegationParams{
		DeployWindow: 1,
		WThreshold:   types.DecimalFromFloat(0.5),
		VMin:         2,
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 1, PocStartBlockHeight: 100}))
	ap := types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 100, Models: []string{"active-model"}, VotingPowers: []*types.ModelVotingPower{{ModelId: "active-model", VotingPower: 100}}},
			{Index: testutil.Executor2, Weight: 60, Models: []string{"active-model"}, VotingPowers: []*types.ModelVotingPower{{ModelId: "active-model", VotingPower: 60}}},
			{Index: testutil.Validator, Weight: 40, Models: []string{"active-model"}, VotingPowers: []*types.ModelVotingPower{{ModelId: "active-model", VotingPower: 40}}},
		},
	}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))
	setupEpochGroupDataFromAP(k, ctx, ap)

	require.NoError(t, k.SetPoCDirectIntent(ctx, "new-model", testutil.Executor))
	require.NoError(t, k.SetPoCDirectIntent(ctx, "new-model", testutil.Executor2))
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "new-model",
		Delegator:  testutil.Validator,
		DelegateTo: testutil.Executor,
	}))

	outsider := testutil.Bech32Addr(99)
	require.NoError(t, k.SetPoCDirectIntent(ctx, "new-model", outsider))
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "new-model",
		Delegator:  outsider,
		DelegateTo: testutil.Executor,
	}))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	snapshot, err := am.buildBootstrapDelegationSnapshot(ctx, 197)
	require.NoError(t, err)

	require.Len(t, snapshot.Delegations, 1)
	require.Equal(t, testutil.Validator, snapshot.Delegations[0].Delegator)
	require.Len(t, snapshot.Intents, 2)
	results := snapshot.Preeligibility
	totalNetworkWeight := snapshot.TotalNetworkWeight
	require.Equal(t, int64(200), snapshot.TotalNetworkWeight)
	require.Equal(t, int64(200), totalNetworkWeight)
	require.Len(t, results, 1)
	require.Equal(t, "new-model", results[0].ModelId)
	require.True(t, results[0].PreEligible)
	require.Equal(t, int64(2), results[0].IntentHostCount)
	require.Equal(t, int64(160), results[0].IntentWeight)
	require.Equal(t, int64(200), results[0].ReachableVotingPower)
}

func TestBootstrapCandidateModelIDs_ExcludesModelsAlreadyActiveInCurrentEpoch(t *testing.T) {
	params := &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "active-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "new-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}

	candidates := bootstrapCandidateModelIDs(params, map[string]bool{"active-model": true})
	require.Equal(t, []string{"new-model"}, candidates)
}

func TestGetEffectiveValidationBaseState_EpochZeroReadsFromGroupData(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 0,
		ModelId:    "",
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, Weight: 100},
			{MemberAddress: testutil.Validator2, Weight: 201},
		},
	})

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	state := am.getEffectiveValidationBaseState(ctx)

	require.Equal(t, int64(301), state.totalWeight)
	require.Len(t, state.participants, 2)
	require.Equal(t, map[string]int64{
		testutil.Validator:  100,
		testutil.Validator2: 201,
	}, state.weights)
	require.Equal(t, testutil.Validator, state.participants[0].Index)
	require.Equal(t, int64(100), state.participants[0].Weight)
	require.Equal(t, testutil.Validator2, state.participants[1].Index)
	require.Equal(t, int64(201), state.participants[1].Weight)
	require.Nil(t, state.existingModelVotingPowers)
}

func TestBuildBootstrapDelegationSnapshot_OnlyVotingPowerModelsTreatedAsActive(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "active-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "new-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	params.DelegationParams = &types.DelegationParams{
		DeployWindow: 1,
		WThreshold:   types.DecimalFromFloat(0.5),
		VMin:         1,
	}
	require.NoError(t, k.SetParams(ctx, params))

	// Participant has voting power for "active-model" but not "new-model".
	// Only models with voting powers are treated as active; SubGroupModels
	// and participant.Models do not count.
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	ap := types.ActiveParticipants{
		EpochGroupId: 1,
		EpochId:      1,
		Participants: []*types.ActiveParticipant{
			{
				Index:  testutil.Validator,
				Weight: 100,
				VotingPowers: []*types.ModelVotingPower{
					{ModelId: "active-model", VotingPower: 100},
				},
			},
			{
				Index:  testutil.Validator2,
				Weight: 100,
			},
		},
	}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))
	setupEpochGroupDataFromAP(k, ctx, ap)

	require.NoError(t, k.SetPoCDirectIntent(ctx, "new-model", testutil.Validator))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	snapshot, err := am.buildBootstrapDelegationSnapshot(ctx, 12)
	require.NoError(t, err)

	require.Len(t, snapshot.Preeligibility, 1)
	require.Equal(t, "new-model", snapshot.Preeligibility[0].ModelId)
}

func TestBuildDelegationSnapshot_IncludesUpcomingCommittersAndExcludesIntents(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "active-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "new-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	const pocStageStart = int64(100)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	ap := types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: pocStageStart,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 100, Models: []string{"active-model"}},
			{Index: testutil.Executor2, Weight: 60, Models: []string{"active-model"}},
		},
	}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))
	setupEpochGroupDataFromAP(k, ctx, ap)

	// Incumbent: delegation + refusal kept, intent dropped.
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "new-model",
		Delegator:  testutil.Executor,
		DelegateTo: testutil.Executor2,
	}))
	require.NoError(t, k.SetPoCRefusal(ctx, "new-model", testutil.Executor2))
	require.NoError(t, k.SetPoCDirectIntent(ctx, "new-model", testutil.Executor))

	// Newcomer: not in N-1, but committed PoC this stage; entries kept.
	newcomer := testutil.Bech32Addr(100)
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "new-model",
		Delegator:  newcomer,
		DelegateTo: testutil.Executor,
	}))
	require.NoError(t, k.SetPoCRefusal(ctx, "active-model", newcomer))
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       newcomer,
		PocStageStartBlockHeight: pocStageStart,
		ModelId:                  "active-model",
	}))

	// Outsider: no N-1 entry, no commit; dropped.
	outsider := testutil.Bech32Addr(101)
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "new-model",
		Delegator:  outsider,
		DelegateTo: testutil.Executor,
	}))
	require.NoError(t, k.SetPoCRefusal(ctx, "new-model", outsider))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	snapshot, err := am.buildDelegationSnapshot(ctx, 197, pocStageStart)
	require.NoError(t, err)

	delegators := make([]string, 0, len(snapshot.Delegations))
	for _, d := range snapshot.Delegations {
		delegators = append(delegators, d.Delegator)
	}
	require.ElementsMatch(t, []string{testutil.Executor, newcomer}, delegators)
	require.NotContains(t, delegators, outsider)

	refusers := make([]string, 0, len(snapshot.Refusals))
	for _, r := range snapshot.Refusals {
		refusers = append(refusers, r.Participant)
	}
	require.ElementsMatch(t, []string{testutil.Executor2, newcomer}, refusers)
	require.NotContains(t, refusers, outsider)
}

func TestBuildBootstrapModelPreEligibilityResults_Conditions(t *testing.T) {
	makeParams := func(threshold float64, vmin int64) types.Params {
		return types.Params{
			PocParams: &types.PocParams{
				Models: []*types.PoCModelConfig{
					{ModelId: "candidate", WeightScaleFactor: types.DecimalFromFloat(1)},
				},
			},
			DelegationParams: &types.DelegationParams{
				WThreshold: types.DecimalFromFloat(threshold),
				VMin:       vmin,
			},
		}
	}

	t.Run("fails_weight_threshold_only", func(t *testing.T) {
		calc := buildBootstrapPreEligibilityCalculator(
			map[string]int64{"a": 100, "b": 60, "c": 40},
			200,
			[]string{"candidate"},
			map[string]map[string]string{"candidate": {"b": "a", "c": "a"}},
			map[string]map[string]bool{"candidate": {"a": true}},
			makeParams(0.6, 1),
		)

		result := buildBootstrapPreEligibilityResults(calc, []string{"candidate"})
		require.Len(t, result, 1)
		require.False(t, result[0].PreEligible)
		require.False(t, result[0].MeetsWeightThreshold)
		require.True(t, result[0].MeetsVMin)
		require.True(t, result[0].MeetsReachability)
	})

	t.Run("fails_vmin_only", func(t *testing.T) {
		calc := buildBootstrapPreEligibilityCalculator(
			map[string]int64{"a": 100, "b": 60, "c": 40},
			200,
			[]string{"candidate"},
			map[string]map[string]string{"candidate": {"b": "a", "c": "a"}},
			map[string]map[string]bool{"candidate": {"a": true}},
			makeParams(0.4, 2),
		)

		result := buildBootstrapPreEligibilityResults(calc, []string{"candidate"})
		require.Len(t, result, 1)
		require.False(t, result[0].PreEligible)
		require.True(t, result[0].MeetsWeightThreshold)
		require.False(t, result[0].MeetsVMin)
		require.True(t, result[0].MeetsReachability)
	})

	t.Run("fails_reachability_only", func(t *testing.T) {
		calc := buildBootstrapPreEligibilityCalculator(
			map[string]int64{"a": 50, "b": 10, "c": 40},
			100,
			[]string{"candidate"},
			map[string]map[string]string{},
			map[string]map[string]bool{"candidate": {"a": true, "b": true}},
			makeParams(0.5, 2),
		)

		result := buildBootstrapPreEligibilityResults(calc, []string{"candidate"})
		require.Len(t, result, 1)
		require.False(t, result[0].PreEligible)
		require.True(t, result[0].MeetsWeightThreshold)
		require.True(t, result[0].MeetsVMin)
		require.False(t, result[0].MeetsReachability)
	})
}

func TestCaptureBootstrapDelegationSnapshot_StoresSnapshotAndEmitsEvents(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "active-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "eligible-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "ineligible-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	params.DelegationParams = &types.DelegationParams{
		DeployWindow: 1,
		WThreshold:   types.DecimalFromFloat(0.5),
		VMin:         2,
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	ap := types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 100, Models: []string{"active-model"}, VotingPowers: []*types.ModelVotingPower{{ModelId: "active-model", VotingPower: 100}}},
			{Index: testutil.Executor2, Weight: 60, Models: []string{"active-model"}, VotingPowers: []*types.ModelVotingPower{{ModelId: "active-model", VotingPower: 60}}},
			{Index: testutil.Validator, Weight: 40, Models: []string{"active-model"}, VotingPowers: []*types.ModelVotingPower{{ModelId: "active-model", VotingPower: 40}}},
		},
	}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))
	setupEpochGroupDataFromAP(k, ctx, ap)

	require.NoError(t, k.SetPoCDirectIntent(ctx, "eligible-model", testutil.Executor))
	require.NoError(t, k.SetPoCDirectIntent(ctx, "eligible-model", testutil.Executor2))
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "eligible-model",
		Delegator:  testutil.Validator,
		DelegateTo: testutil.Executor,
	}))

	require.NoError(t, k.SetPoCDirectIntent(ctx, "ineligible-model", testutil.Executor))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	ctx = ctx.WithEventManager(sdk.NewEventManager())
	am.captureBootstrapDelegationSnapshot(ctx, 197)

	snapshot, found := k.GetBootstrapDelegationSnapshot(ctx)
	require.True(t, found)
	require.Equal(t, int64(197), snapshot.SnapshotHeight)
	resultsSlice := snapshot.Preeligibility
	totalNetworkWeight := snapshot.TotalNetworkWeight
	require.Equal(t, int64(200), snapshot.TotalNetworkWeight)
	require.Equal(t, int64(200), totalNetworkWeight)
	require.Len(t, resultsSlice, 2)

	results := map[string]*types.BootstrapModelPreEligibility{}
	for _, result := range resultsSlice {
		results[result.ModelId] = result
	}
	require.True(t, results["eligible-model"].PreEligible)
	require.False(t, results["ineligible-model"].PreEligible)

	events := ctx.EventManager().Events()
	require.Len(t, events, 2)
	require.Equal(t, "bootstrap_model_preeligibility", events[0].Type)
	require.Equal(t, "bootstrap_model_preeligibility", events[1].Type)
}

func TestCaptureDelegationSnapshot_StoresFrozenState(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "active-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "candidate", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	ap := types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 100, Models: []string{"active-model"}},
			{Index: testutil.Validator, Weight: 100, Models: []string{"active-model"}},
		},
	}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))
	setupEpochGroupDataFromAP(k, ctx, ap)

	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "candidate",
		Delegator:  testutil.Validator,
		DelegateTo: testutil.Executor,
	}))
	require.NoError(t, k.SetPoCRefusal(ctx, "candidate", testutil.Executor))
	require.NoError(t, k.SetPoCDirectIntent(ctx, "candidate", testutil.Executor))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	am.captureDelegationSnapshot(ctx, 197, 100)

	snapshot, found := k.GetDelegationSnapshot(ctx)
	require.True(t, found)
	require.Equal(t, int64(197), snapshot.SnapshotHeight)
	require.Len(t, snapshot.Delegations, 1)
	require.Len(t, snapshot.Refusals, 1)
}

func TestComputeStoreCommitVotingPowers_UsesExistingVotingPowersAndBootstrapDelegationForNewModels(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "existing-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "new-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	params.DelegationParams = &types.DelegationParams{
		WThreshold: types.DecimalFromFloat(0.5),
		VMin:       1,
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	ap := types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{
				Index:        testutil.Executor,
				Weight:       100,
				Models:       []string{"existing-model"},
				VotingPowers: []*types.ModelVotingPower{{ModelId: "existing-model", VotingPower: 70}},
			},
			{
				Index:        testutil.Executor2,
				Weight:       60,
				Models:       []string{"existing-model"},
				VotingPowers: []*types.ModelVotingPower{{ModelId: "existing-model", VotingPower: 30}},
			},
			{
				Index:  testutil.Validator,
				Weight: 40,
			},
		},
	}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))
	setupEpochGroupDataFromAP(k, ctx, ap)

	require.NoError(t, k.SetBootstrapDelegationSnapshot(ctx, types.BootstrapDelegationSnapshot{
		SnapshotHeight: 111,
		Delegations: []*types.PoCDelegation{
			{
				ModelId:    "new-model",
				Delegator:  testutil.Validator,
				DelegateTo: testutil.Executor,
			},
		},
		Intents: []*types.PoCDirectIntent{
			{
				ModelId:     "new-model",
				Participant: testutil.Executor,
			},
		},
		TotalNetworkWeight: 200,
		Preeligibility: []*types.BootstrapModelPreEligibility{
			{
				ModelId:           "new-model",
				PreEligible:       true,
				MeetsVMin:         true,
				MeetsReachability: true,
			},
		},
	}))

	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		ModelId:                  "new-model",
		Count:                    1,
	}))
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor2,
		PocStageStartBlockHeight: 180,
		ModelId:                  "existing-model",
		Count:                    1,
	}))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	modelWeights, totalWeight := am.computeStoreCommitVotingPowers(ctx, am.getEffectiveValidationBaseState(ctx), 180, "test")
	require.Equal(t, int64(200), totalWeight)

	got := map[string]map[string]int64{}
	for _, modelWeight := range modelWeights {
		got[modelWeight.ModelId] = types.VotingPowerSliceToMap(modelWeight.VotingPowers)
	}

	require.Equal(t, map[string]int64{
		testutil.Executor:  70,
		testutil.Executor2: 30,
	}, got["existing-model"])
	require.Equal(t, map[string]int64{
		testutil.Executor: 140,
	}, got["new-model"])
}

func TestComputeStoreCommitVotingPowers_EpochZeroBypassesBootstrapPreEligibility(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "new-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	params.DelegationParams = &types.DelegationParams{
		WThreshold: types.DecimalFromFloat(0.4),
		VMin:       1,
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 0,
		ModelId:    "",
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Executor, Weight: 100},
			{MemberAddress: testutil.Validator, Weight: 40},
		},
	})

	require.NoError(t, k.SetBootstrapDelegationSnapshot(ctx, types.BootstrapDelegationSnapshot{
		SnapshotHeight: 111,
		Delegations: []*types.PoCDelegation{
			{
				ModelId:    "new-model",
				Delegator:  testutil.Validator,
				DelegateTo: testutil.Executor,
			},
		},
		TotalNetworkWeight: 140,
		Preeligibility: []*types.BootstrapModelPreEligibility{
			{
				ModelId:              "new-model",
				PreEligible:          false,
				MeetsWeightThreshold: true,
				MeetsVMin:            true,
				MeetsReachability:    false,
			},
		},
	}))

	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		ModelId:                  "new-model",
		Count:                    1,
	}))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	modelWeights, totalWeight := am.computeStoreCommitVotingPowers(ctx, am.getEffectiveValidationBaseState(ctx), 180, "test")
	require.Equal(t, int64(140), totalWeight)
	require.Len(t, modelWeights, 1)
	require.Equal(t, "new-model", modelWeights[0].ModelId)

	got := types.VotingPowerSliceToMap(modelWeights[0].VotingPowers)
	require.Equal(t, map[string]int64{
		testutil.Executor: 140,
	}, got)
}

func TestComputeStoreCommitVotingPowers_UsesFrozenBootstrapDelegationsEvenWhenPreEligibilityIsFalse(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "new-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	params.DelegationParams = &types.DelegationParams{
		WThreshold: types.DecimalFromFloat(0.4),
		VMin:       1,
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	ap := types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 100},
			{Index: testutil.Validator, Weight: 40},
		},
	}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))
	setupEpochGroupDataFromAP(k, ctx, ap)

	require.NoError(t, k.SetBootstrapDelegationSnapshot(ctx, types.BootstrapDelegationSnapshot{
		SnapshotHeight: 111,
		Delegations: []*types.PoCDelegation{
			{
				ModelId:    "new-model",
				Delegator:  testutil.Validator,
				DelegateTo: testutil.Executor,
			},
		},
		Intents: []*types.PoCDirectIntent{
			{
				ModelId:     "new-model",
				Participant: testutil.Executor,
			},
		},
		TotalNetworkWeight: 140,
		Preeligibility: []*types.BootstrapModelPreEligibility{
			{
				ModelId:              "new-model",
				PreEligible:          false,
				MeetsWeightThreshold: true,
				MeetsVMin:            true,
				MeetsReachability:    false,
			},
		},
	}))

	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		ModelId:                  "new-model",
		Count:                    1,
	}))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	modelWeights, totalWeight := am.computeStoreCommitVotingPowers(ctx, am.getEffectiveValidationBaseState(ctx), 180, "test")
	require.Equal(t, int64(140), totalWeight)
	require.Len(t, modelWeights, 1)

	got := types.VotingPowerSliceToMap(modelWeights[0].VotingPowers)
	require.Equal(t, "new-model", modelWeights[0].ModelId)
	require.Equal(t, map[string]int64{
		testutil.Executor: 140,
	}, got)
}

func TestComputeStoreCommitVotingPowers_DoesNotFallbackToLiveDelegations(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "new-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	ap := types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 100},
			{Index: testutil.Validator, Weight: 40},
		},
	}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))
	setupEpochGroupDataFromAP(k, ctx, ap)

	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "new-model",
		Delegator:  testutil.Validator,
		DelegateTo: testutil.Executor,
	}))
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		ModelId:                  "new-model",
		Count:                    1,
	}))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	modelWeights, _ := am.computeStoreCommitVotingPowers(ctx, am.getEffectiveValidationBaseState(ctx), 180, "test")
	require.Len(t, modelWeights, 1)

	got := types.VotingPowerSliceToMap(modelWeights[0].VotingPowers)
	require.Equal(t, map[string]int64{
		testutil.Executor: 100,
	}, got)
}

func TestComputeStoreCommitVotingPowers_BootstrapModelUsesDirectCommittersAndFrozenDelegations(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "new-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	params.DelegationParams = &types.DelegationParams{
		WThreshold: types.DecimalFromFloat(0.9),
		VMin:       2,
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	ap := types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 60},
			{Index: testutil.Validator, Weight: 40},
			{Index: testutil.Executor2, Weight: 20},
		},
	}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))
	setupEpochGroupDataFromAP(k, ctx, ap)

	require.NoError(t, k.SetBootstrapDelegationSnapshot(ctx, types.BootstrapDelegationSnapshot{
		SnapshotHeight: 111,
		Delegations: []*types.PoCDelegation{
			{
				ModelId:    "new-model",
				Delegator:  testutil.Executor2,
				DelegateTo: testutil.Executor,
			},
		},
		TotalNetworkWeight: 120,
		Preeligibility: []*types.BootstrapModelPreEligibility{
			{
				ModelId:              "new-model",
				PreEligible:          false,
				MeetsWeightThreshold: false,
				MeetsVMin:            false,
				MeetsReachability:    false,
			},
		},
	}))

	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		ModelId:                  "new-model",
		Count:                    1,
	}))
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Validator,
		PocStageStartBlockHeight: 180,
		ModelId:                  "new-model",
		Count:                    1,
	}))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	modelWeights, totalWeight := am.computeStoreCommitVotingPowers(ctx, am.getEffectiveValidationBaseState(ctx), 180, "test")
	require.Equal(t, int64(120), totalWeight)
	require.Len(t, modelWeights, 1)

	got := types.VotingPowerSliceToMap(modelWeights[0].VotingPowers)
	require.Equal(t, map[string]int64{
		testutil.Executor:  80,
		testutil.Validator: 40,
	}, got)
}

func TestBuildDelegationWeightCalculator_UsesValidationSnapshotForNextEpochVotingPowers(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	ap := types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 100},
			{Index: testutil.Validator, Weight: 40},
			{Index: testutil.Executor2, Weight: 20},
		},
	}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))
	setupEpochGroupDataFromAP(k, ctx, ap)

	require.NoError(t, k.SetDelegationSnapshot(ctx, types.DelegationSnapshot{
		SnapshotHeight: 111,
		Delegations: []*types.PoCDelegation{
			{
				ModelId:    "model-a",
				Delegator:  testutil.Validator,
				DelegateTo: testutil.Executor,
			},
		},
	}))
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "model-a",
		Delegator:  testutil.Validator,
		DelegateTo: testutil.Executor2,
	}))

	activeParticipants := []*types.ActiveParticipant{
		{
			Index:  testutil.Executor,
			Models: []string{"model-a"},
			MlNodes: []*types.ModelMLNodes{{
				MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 10}},
			}},
			Weight: 100,
		},
	}

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	dwc := am.buildDelegationWeightCalculator(ctx, activeParticipants, map[string]sdkmath.LegacyDec{"model-a": sdkmath.LegacyOneDec()}, params)
	modes := dwc.ResolveGroupParticipation("model-a")
	require.Equal(t, ModeDelegate, modes[testutil.Validator])
	require.Equal(t, ModeNone, modes[testutil.Executor2])

	vp := dwc.ComputeGroupVotingPowers("model-a", modes, map[string]int64{
		testutil.Executor:  100,
		testutil.Validator: 40,
		testutil.Executor2: 20,
	})
	require.Equal(t, int64(140), vp[testutil.Executor])
}

func TestProjectedReachableVotingPower(t *testing.T) {
	calc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"candidate": {
				Members:          []string{"a", "b"},
				MemberPocWeights: map[string]int64{},
				ConsensusKoeff:   sdkmath.LegacyOneDec(),
			},
		},
		ConsensusWeights: map[string]int64{
			"a": 50,
			"b": 20,
			"c": 30,
		},
		TotalNetworkWeight: 100,
		Delegations: map[string]map[string]string{
			"candidate": {
				"c": "a",
			},
		},
	}

	require.Equal(t, int64(100), calc.ProjectedReachableVotingPower("candidate"))
	require.True(t, calc.MeetsReachabilityThreshold("candidate"))
}

func TestGetEffectiveValidationBaseState_ExcludesRemovedMembers(t *testing.T) {
	k, ctx, stub := newMinimalInferenceKeeperWithStub(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	// Set effective epoch
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:     1,
		ModelId:        "",
		EpochGroupId:   1,
		SubGroupModels: []string{"model-a"},
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: "alice", Weight: 100},
			{MemberAddress: "bob", Weight: 200},
			{MemberAddress: "charlie", Weight: 300},
		},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   1,
		ModelId:      "model-a",
		EpochGroupId: 2,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: "alice", Weight: 100, VotingPower: 150},
			{MemberAddress: "bob", Weight: 200, VotingPower: 250},
			{MemberAddress: "charlie", Weight: 300, VotingPower: 300},
		},
	})
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))

	// Simulate bob removed mid-epoch from SDK group
	stub.excludedMembers = map[string]bool{"bob": true}

	state := am.getEffectiveValidationBaseState(ctx)

	// Bob should be excluded from consensus weights
	require.Equal(t, int64(100), state.weights["alice"])
	require.Equal(t, int64(300), state.weights["charlie"])
	require.Equal(t, int64(0), state.weights["bob"])
	require.Equal(t, int64(400), state.totalWeight) // 100 + 300

	// Bob should be excluded from model voting powers
	for _, mvp := range state.existingModelVotingPowers {
		if mvp.ModelId == "model-a" {
			vps := types.VotingPowerSliceToMap(mvp.VotingPowers)
			require.Equal(t, int64(150), vps["alice"])
			require.Equal(t, int64(300), vps["charlie"])
			_, hasBob := vps["bob"]
			require.False(t, hasBob)
		}
	}

	// Clean up
	stub.excludedMembers = nil
}

// --- Voting power cap tests ---

// nopCapLogger satisfies votingPowerCapLogger without touching any real logger.
type nopCapLogger struct{}

func (nopCapLogger) LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{}) {}
func (nopCapLogger) LogWarn(msg string, subSystem types.SubSystem, keyvals ...interface{}) {}

func sumVP(m map[string]int64) int64 {
	var s int64
	for _, v := range m {
		s += v
	}
	return s
}

func TestCapPerModelVotingPowers_NoCapNeeded(t *testing.T) {
	// Nobody exceeds the cap, so the map is untouched.
	vp := map[string]int64{
		"a": 100,
		"b": 100,
		"c": 100,
	}
	capPct := sdkmath.LegacyNewDecWithPrec(50, 2) // 50%

	capPerModelVotingPowers(vp, capPct, "model-test", nopCapLogger{})

	require.Equal(t, int64(100), vp["a"])
	require.Equal(t, int64(100), vp["b"])
	require.Equal(t, int64(100), vp["c"])
}

func TestCapPerModelVotingPowers_ClipsWhaleAndBurnsExcess(t *testing.T) {
	// Whale holds 80% of the group; cap is 50%. The whale's excess is burned,
	// not redistributed. Small participants are unchanged.
	vp := map[string]int64{
		"whale":  800, // 80% of original total
		"small1": 100, // 10%
		"small2": 100, // 10%
	}
	originalTotal := sumVP(vp)                    // 1000
	capPct := sdkmath.LegacyNewDecWithPrec(50, 2) // 50% cap
	capVP := capPct.MulInt64(originalTotal).TruncateInt64()

	capPerModelVotingPowers(vp, capPct, "model-test", nopCapLogger{})

	require.Equal(t, capVP, vp["whale"], "whale should be clipped to capVP")
	require.Equal(t, int64(100), vp["small1"], "small1 must be unchanged (no redistribution)")
	require.Equal(t, int64(100), vp["small2"], "small2 must be unchanged (no redistribution)")
	// Total shrank by the burned amount.
	require.Equal(t, originalTotal-(800-capVP), sumVP(vp),
		"post-cap total should equal originalTotal minus the burned excess")
}

func TestCapPerModelVotingPowers_MultipleHostsOverCap(t *testing.T) {
	// Two hosts share dominance: each holds 40% of the group and the cap is
	// 30%. Both must be clipped independently to the cap computed against
	// the ORIGINAL total (not the shrinking total). A regression to
	// iterative or post-clip-recomputed behavior would produce a different
	// capVP on the second host.
	vp := map[string]int64{
		"a": 400,
		"b": 400,
		"c": 200,
	}
	originalTotal := sumVP(vp)                    // 1000
	capPct := sdkmath.LegacyNewDecWithPrec(30, 2) // 30%
	capVP := capPct.MulInt64(originalTotal).TruncateInt64()

	capPerModelVotingPowers(vp, capPct, "model-test", nopCapLogger{})

	require.Equal(t, capVP, vp["a"], "a must be clipped to the original-total cap")
	require.Equal(t, capVP, vp["b"], "b must be clipped to the original-total cap")
	require.Equal(t, int64(200), vp["c"], "c is below the cap and must be unchanged")
}

func TestCapPerModelVotingPowers_TinyGroupClipsCleanly(t *testing.T) {
	// Two-host extreme: whale at 90% in a 1000-VP group, 30% cap. Whale is
	// clipped to 300, small stays at 100, 600 is burned. The 'converges in
	// multiple iterations' problem from the redistribution implementation
	// doesn't arise at all under burn — it's a single clip.
	vp := map[string]int64{
		"whale": 900,
		"small": 100,
	}
	originalTotal := sumVP(vp)
	capPct := sdkmath.LegacyNewDecWithPrec(30, 2) // 30% cap
	capVP := capPct.MulInt64(originalTotal).TruncateInt64()

	capPerModelVotingPowers(vp, capPct, "model-test", nopCapLogger{})

	require.Equal(t, capVP, vp["whale"], "whale should be clipped to capVP")
	require.Equal(t, int64(100), vp["small"], "small must be unchanged")
	require.Equal(t, int64(100)+capVP, sumVP(vp), "post-cap total = small + capVP")
}

func TestCapPerModelVotingPowers_ZeroCapIsDisabled(t *testing.T) {
	vp := map[string]int64{
		"whale": 900,
		"small": 100,
	}
	capPct := sdkmath.LegacyZeroDec()
	before := make(map[string]int64, len(vp))
	for k, v := range vp {
		before[k] = v
	}

	// capPct=0 should never be called by production code, but defensively
	// verify the inner helper no-ops when cap is 0. It iterates but capVP=0
	// so no host is over-cap and the loop exits immediately.
	capPerModelVotingPowers(vp, capPct, "model-test", nopCapLogger{})

	for k, v := range before {
		require.Equal(t, v, vp[k], "map unchanged when cap is zero")
	}
}

func TestCapPerModelVotingPowers_SingleHostNoOp(t *testing.T) {
	// Single-host groups are left alone: with only one participant, capping
	// them would just burn VP for no protective benefit.
	vp := map[string]int64{"solo": 1000}
	capPct := sdkmath.LegacyNewDecWithPrec(10, 2) // 10%
	capPerModelVotingPowers(vp, capPct, "model-test", nopCapLogger{})
	require.Equal(t, int64(1000), vp["solo"])
}

