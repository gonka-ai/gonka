package keeper_test

import (
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestRepairEpochValidationState_RewritesAndDeletes(t *testing.T) {
	k, ctx := auditKeeper(t)
	addr := sdk.MustAccAddressFromBech32(testutil.Executor)
	upper := strings.ToUpper(testutil.Executor)

	require.NoError(t, k.Participants.Set(ctx, addr, types.Participant{
		Index:   upper,
		Address: upper,
		Status:  types.ParticipantStatus_ACTIVE,
	}))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      7,
		Participants: []*types.ActiveParticipant{{Index: upper, CapWeight: 3}},
	}))

	seedAuditCommit(t, k, ctx, testutil.Executor, "m-rewrite", 10, []*types.MLNodeWeight{
		{NodeId: "gone", Weight: 0},
		{NodeId: "kept", Weight: 10},
	})
	seedAuditCommit(t, k, ctx, testutil.Executor, "m-empty", 10, []*types.MLNodeWeight{
		{NodeId: "", Weight: 10},
	})
	seedAuditCommit(t, k, ctx, testutil.Executor, "m-dup", 10, []*types.MLNodeWeight{
		{NodeId: "same", Weight: 4},
		{NodeId: "same", Weight: 6},
	})
	longID := strings.Repeat("n", types.MaxMLNodeWeightNodeIDBytes+1)
	seedAuditCommit(t, k, ctx, testutil.Executor, "m-long", 10, []*types.MLNodeWeight{
		{NodeId: longID, Weight: 10},
	})

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 3,
		ModelId:    "eg-clean",
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: testutil.Executor,
			Weight:        99,
			MlNodes: []*types.MLNodeInfo{
				{NodeId: "z", PocWeight: 0},
				{NodeId: "a", PocWeight: 4},
			},
		}},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 3,
		ModelId:    "eg-clear",
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: testutil.Executor,
			Weight:        50,
			MlNodes: []*types.MLNodeInfo{
				{NodeId: "a", PocWeight: 2},
				{NodeId: "a", PocWeight: 3},
			},
		}},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 3,
		ModelId:    "eg-long",
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: testutil.Executor,
			Weight:        77,
			MlNodes: []*types.MLNodeInfo{
				{NodeId: longID, PocWeight: 8},
			},
		}},
	})
	upperValidator := strings.ToUpper(testutil.Validator)
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "m",
		Delegator:  upper,
		DelegateTo: upperValidator,
	}))
	require.NoError(t, k.SetPoCRefusal(ctx, "m", upper))
	require.NoError(t, k.SetPoCDirectIntent(ctx, "m", upper))
	require.NoError(t, k.SetDelegationSnapshot(ctx, types.DelegationSnapshot{
		SnapshotHeight: 1,
		Delegations: []*types.PoCDelegation{{
			ModelId:    "m",
			Delegator:  upper,
			DelegateTo: upperValidator,
		}},
		Refusals: []*types.PoCRefusal{{ModelId: "m", Participant: upper}},
	}))
	require.NoError(t, k.SetBootstrapDelegationSnapshot(ctx, types.BootstrapDelegationSnapshot{
		SnapshotHeight: 1,
		Intents:        []*types.PoCDirectIntent{{ModelId: "m", Participant: upper}},
	}))
	require.NoError(t, k.SetHardwareNodes(ctx, &types.HardwareNodes{
		Participant: testutil.Executor,
		HardwareNodes: []*types.HardwareNode{
			{LocalId: "hw-1"},
			{LocalId: "hw-2"},
		},
	}))

	require.NoError(t, k.RepairEpochValidationState(ctx))

	participant, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, testutil.Executor, participant.Index)
	require.Equal(t, testutil.Executor, participant.Address)

	active, found := k.GetActiveParticipants(ctx, 7)
	require.True(t, found)
	require.Equal(t, testutil.Executor, active.Participants[0].Index)

	rewritten := auditDistribution(t, k, ctx, "m-rewrite")
	require.True(t, rewritten.Found)
	require.Equal(t, []*types.MLNodeWeight{{NodeId: "kept", Weight: 10}}, rewritten.Weights)
	require.Equal(t, uint32(10), auditCommitCount(t, k, ctx, "m-rewrite"))

	require.False(t, auditDistribution(t, k, ctx, "m-empty").Found)
	require.Equal(t, uint32(10), auditCommitCount(t, k, ctx, "m-empty"))
	require.False(t, auditDistribution(t, k, ctx, "m-dup").Found)
	require.Equal(t, uint32(10), auditCommitCount(t, k, ctx, "m-dup"))
	require.False(t, auditDistribution(t, k, ctx, "m-long").Found)
	require.Equal(t, uint32(10), auditCommitCount(t, k, ctx, "m-long"))
	longGroup, found := k.GetEpochGroupData(ctx, 3, "eg-long")
	require.True(t, found)
	require.Equal(t, int64(77), longGroup.ValidationWeights[0].Weight)
	require.Empty(t, longGroup.ValidationWeights[0].MlNodes)

	delegation, found := k.GetPoCDelegation(ctx, "m", testutil.Executor)
	require.True(t, found)
	require.Equal(t, testutil.Executor, delegation.Delegator)
	require.Equal(t, testutil.Validator, delegation.DelegateTo)
	_, found = k.GetPoCDelegation(ctx, "m", upper)
	require.False(t, found)
	require.True(t, k.HasPoCRefusal(ctx, "m", testutil.Executor))
	require.False(t, k.HasPoCRefusal(ctx, "m", upper))
	require.True(t, k.HasPoCDirectIntent(ctx, "m", testutil.Executor))
	require.False(t, k.HasPoCDirectIntent(ctx, "m", upper))
	snapshot, found := k.GetDelegationSnapshot(ctx)
	require.True(t, found)
	require.Equal(t, testutil.Executor, snapshot.Delegations[0].Delegator)
	require.Equal(t, testutil.Validator, snapshot.Delegations[0].DelegateTo)
	require.Equal(t, testutil.Executor, snapshot.Refusals[0].Participant)
	bootstrap, found := k.GetBootstrapDelegationSnapshot(ctx)
	require.True(t, found)
	require.Equal(t, testutil.Executor, bootstrap.Intents[0].Participant)

	cleaned, found := k.GetEpochGroupData(ctx, 3, "eg-clean")
	require.True(t, found)
	require.Equal(t, int64(99), cleaned.ValidationWeights[0].Weight)
	require.Equal(t, []*types.MLNodeInfo{{NodeId: "a", PocWeight: 4}}, cleaned.ValidationWeights[0].MlNodes)

	cleared, found := k.GetEpochGroupData(ctx, 3, "eg-clear")
	require.True(t, found)
	require.Equal(t, int64(50), cleared.ValidationWeights[0].Weight)
	require.Empty(t, cleared.ValidationWeights[0].MlNodes)

	hardware, found := k.GetHardwareNodes(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, []string{"hw-1", "hw-2"}, []string{
		hardware.HardwareNodes[0].LocalId,
		hardware.HardwareNodes[1].LocalId,
	})

	require.NoError(t, k.RepairEpochValidationState(ctx))
	again := auditDistribution(t, k, ctx, "m-rewrite")
	require.Equal(t, rewritten.Weights, again.Weights)
	require.False(t, auditDistribution(t, k, ctx, "m-empty").Found)
	participant, found = k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, testutil.Executor, participant.Index)
	cleanedAgain, found := k.GetEpochGroupData(ctx, 3, "eg-clean")
	require.True(t, found)
	require.Equal(t, cleaned.ValidationWeights[0].MlNodes, cleanedAgain.ValidationWeights[0].MlNodes)
}

func TestRepairEpochValidationState_AbortsBeforeWrites(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, k keeper.Keeper, ctx sdk.Context)
	}{
		{
			name: "participant index does not decode",
			plant: func(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
				addr := sdk.MustAccAddressFromBech32(testutil.Executor)
				require.NoError(t, k.Participants.Set(ctx, addr, types.Participant{
					Index:   "not-an-address",
					Address: "not-an-address",
				}))
			},
		},
		{
			name: "participant index decodes to a different account",
			plant: func(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
				addr := sdk.MustAccAddressFromBech32(testutil.Executor)
				require.NoError(t, k.Participants.Set(ctx, addr, types.Participant{
					Index:   testutil.Validator,
					Address: testutil.Validator,
				}))
			},
		},
		{
			name: "active set contains one account twice",
			plant: func(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
				require.NoError(t, keeper.PlantActiveParticipantsForTest(k, ctx, types.ActiveParticipants{
					EpochId: 4,
					Participants: []*types.ActiveParticipant{
						{Index: testutil.Executor, CapWeight: 5},
						{Index: strings.ToUpper(testutil.Executor), CapWeight: 1},
					},
				}))
			},
		},
		{
			name: "active index does not decode",
			plant: func(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
				require.NoError(t, keeper.PlantActiveParticipantsForTest(k, ctx, types.ActiveParticipants{
					EpochId:      4,
					Participants: []*types.ActiveParticipant{{Index: "not-an-address"}},
				}))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx := auditKeeper(t)
			seedAuditCommit(t, k, ctx, testutil.Executor, "m-rewrite", 10, []*types.MLNodeWeight{
				{NodeId: "gone", Weight: 0},
				{NodeId: "kept", Weight: 10},
			})
			tc.plant(t, k, ctx)

			require.Error(t, k.RepairEpochValidationState(ctx))

			stored := auditDistribution(t, k, ctx, "m-rewrite")
			require.True(t, stored.Found)
			require.Equal(t, uint32(0), stored.Weights[0].Weight)
			require.Equal(t, "gone", stored.Weights[0].NodeId)
		})
	}
}

func auditKeeper(t *testing.T) (keeper.Keeper, sdk.Context) {
	t.Helper()
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	return k, sdk.UnwrapSDKContext(ctx)
}

func seedAuditCommit(t *testing.T, k keeper.Keeper, ctx sdk.Context, participant, model string, count uint32, weights []*types.MLNodeWeight) {
	t.Helper()
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       participant,
		PocStageStartBlockHeight: 100,
		Count:                    count,
		RootHash:                 make([]byte, 32),
		ModelId:                  model,
	}))
	require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
		ParticipantAddress:       participant,
		PocStageStartBlockHeight: 100,
		ModelId:                  model,
		Weights:                  weights,
	}))
}

func auditDistribution(t *testing.T, k keeper.Keeper, ctx sdk.Context, model string) *types.QueryMLNodeWeightDistributionResponse {
	t.Helper()
	resp, err := k.MLNodeWeightDistribution(ctx, &types.QueryMLNodeWeightDistributionRequest{
		PocStageStartBlockHeight: 100,
		ParticipantAddress:       testutil.Executor,
		ModelId:                  model,
	})
	require.NoError(t, err)
	return resp
}

func auditCommitCount(t *testing.T, k keeper.Keeper, ctx sdk.Context, model string) uint32 {
	t.Helper()
	resp, err := k.PoCV2StoreCommit(ctx, &types.QueryPoCV2StoreCommitRequest{
		PocStageStartBlockHeight: 100,
		ParticipantAddress:       testutil.Executor,
		ModelId:                  model,
	})
	require.NoError(t, err)
	require.True(t, resp.Found)
	return resp.Count
}

func TestRepairEpochValidationState_ClearsConflictingDelegationSpellings(t *testing.T) {
	k, ctx := auditKeeper(t)
	upper := strings.ToUpper(testutil.Executor)
	otherUpper := strings.ToUpper(testutil.Executor2)

	seedAuditCommit(t, k, ctx, testutil.Executor, "m-rewrite", 10, []*types.MLNodeWeight{
		{NodeId: "gone", Weight: 0},
		{NodeId: "kept", Weight: 10},
	})
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId: "conflict", Delegator: testutil.Executor, DelegateTo: testutil.Validator,
	}))
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId: "conflict", Delegator: upper, DelegateTo: testutil.Executor2,
	}))
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId: "kept", Delegator: otherUpper, DelegateTo: testutil.Validator,
	}))
	require.NoError(t, k.SetPoCRefusal(ctx, "conflict", testutil.Executor))
	require.NoError(t, k.SetPoCRefusal(ctx, "conflict", upper))
	require.NoError(t, k.SetPoCRefusal(ctx, "kept", otherUpper))
	require.NoError(t, k.SetPoCDirectIntent(ctx, "conflict", testutil.Executor))
	require.NoError(t, k.SetPoCDirectIntent(ctx, "conflict", upper))
	require.NoError(t, k.SetPoCDirectIntent(ctx, "kept", otherUpper))
	require.NoError(t, k.SetDelegationSnapshot(ctx, types.DelegationSnapshot{
		SnapshotHeight: 1,
		Delegations: []*types.PoCDelegation{
			{ModelId: "conflict", Delegator: testutil.Executor, DelegateTo: testutil.Validator},
			{ModelId: "conflict", Delegator: upper, DelegateTo: testutil.Executor2},
			{ModelId: "kept", Delegator: otherUpper, DelegateTo: testutil.Validator},
		},
		Refusals: []*types.PoCRefusal{
			{ModelId: "conflict", Participant: testutil.Executor},
			{ModelId: "conflict", Participant: upper},
			{ModelId: "kept", Participant: otherUpper},
		},
	}))
	require.NoError(t, k.SetBootstrapDelegationSnapshot(ctx, types.BootstrapDelegationSnapshot{
		SnapshotHeight: 1,
		Intents: []*types.PoCDirectIntent{
			{ModelId: "conflict", Participant: testutil.Executor},
			{ModelId: "conflict", Participant: upper},
			{ModelId: "kept", Participant: otherUpper},
		},
	}))

	require.NoError(t, k.RepairEpochValidationState(ctx))

	_, found := k.GetPoCDelegation(ctx, "conflict", testutil.Executor)
	require.False(t, found)
	_, found = k.GetPoCDelegation(ctx, "conflict", upper)
	require.False(t, found)
	kept, found := k.GetPoCDelegation(ctx, "kept", testutil.Executor2)
	require.True(t, found)
	require.Equal(t, testutil.Executor2, kept.Delegator)
	require.Equal(t, testutil.Validator, kept.DelegateTo)
	require.False(t, k.HasPoCRefusal(ctx, "conflict", testutil.Executor))
	require.False(t, k.HasPoCRefusal(ctx, "conflict", upper))
	require.True(t, k.HasPoCRefusal(ctx, "kept", testutil.Executor2))
	require.False(t, k.HasPoCDirectIntent(ctx, "conflict", testutil.Executor))
	require.False(t, k.HasPoCDirectIntent(ctx, "conflict", upper))
	require.True(t, k.HasPoCDirectIntent(ctx, "kept", testutil.Executor2))

	snapshot, found := k.GetDelegationSnapshot(ctx)
	require.True(t, found)
	require.Equal(t, []*types.PoCDelegation{{
		ModelId: "kept", Delegator: testutil.Executor2, DelegateTo: testutil.Validator,
	}}, snapshot.Delegations)
	require.Equal(t, []*types.PoCRefusal{{
		ModelId: "kept", Participant: testutil.Executor2,
	}}, snapshot.Refusals)
	bootstrap, found := k.GetBootstrapDelegationSnapshot(ctx)
	require.True(t, found)
	require.Equal(t, []*types.PoCDirectIntent{{
		ModelId: "kept", Participant: testutil.Executor2,
	}}, bootstrap.Intents)

	rewritten := auditDistribution(t, k, ctx, "m-rewrite")
	require.True(t, rewritten.Found)
	require.Equal(t, []*types.MLNodeWeight{{NodeId: "kept", Weight: 10}}, rewritten.Weights)
}
