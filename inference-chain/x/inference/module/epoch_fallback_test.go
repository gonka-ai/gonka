package inference

import (
	"slices"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
)

func mustAccAddr(t *testing.T, addr string) sdk.AccAddress {
	t.Helper()
	accAddr, err := sdk.AccAddressFromBech32(addr)
	require.NoError(t, err)
	return accAddr
}

func findByIndex(participants []*types.ActiveParticipant, index string) *types.ActiveParticipant {
	for _, p := range participants {
		if p.Index == index {
			return p
		}
	}
	return nil
}

func TestFallbackActiveParticipantsFromCurrentEpoch(t *testing.T) {
	k, ctx, groupStub := newMinimalInferenceKeeperWithStub(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	const currentEpochIndex = uint64(5)
	upcomingEpoch := types.Epoch{Index: 6, PocStartBlockHeight: 600}

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpochIndex))

	carried := testutil.Executor    // happy path: live, active, has upcoming seed
	seedReuse := testutil.Executor2 // live, active, only current-epoch seed
	rootOnly := testutil.Creator    // live, active, seed, but no subgroup nodes
	removed := testutil.Validator   // removed from SDK group mid-epoch
	excluded := testutil.Validator2 // has an exclusion record for current epoch
	invalid := testutil.Requester   // participant status INVALID

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:     currentEpochIndex,
		ModelId:        "",
		EpochGroupId:   77,
		SubGroupModels: []string{"model-a"},
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: carried, Weight: 100},
			{MemberAddress: seedReuse, Weight: 50},
			{MemberAddress: rootOnly, Weight: 70},
			{MemberAddress: removed, Weight: 40},
			{MemberAddress: excluded, Weight: 30},
			{MemberAddress: invalid, Weight: 20},
		},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   currentEpochIndex,
		ModelId:      "model-a",
		EpochGroupId: 78,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: carried, Weight: 100, MlNodes: []*types.MLNodeInfo{
				{NodeId: "n1", PocWeight: 60, Throughput: 10, TimeslotAllocation: []bool{true, true}},
				{NodeId: "n2", PocWeight: 40},
			}},
			{MemberAddress: seedReuse, Weight: 50, MlNodes: []*types.MLNodeInfo{
				{NodeId: "m1", PocWeight: 50},
			}},
			{MemberAddress: removed, Weight: 40, MlNodes: []*types.MLNodeInfo{
				{NodeId: "r1", PocWeight: 40},
			}},
			{MemberAddress: excluded, Weight: 30, MlNodes: []*types.MLNodeInfo{
				{NodeId: "e1", PocWeight: 30},
			}},
			{MemberAddress: invalid, Weight: 20, MlNodes: []*types.MLNodeInfo{
				{NodeId: "i1", PocWeight: 20},
			}},
		},
	})

	// Mid-epoch removal: participant is still in ValidationWeights but no
	// longer a live SDK group member.
	groupStub.excludedMembers = map[string]bool{removed: true}

	setParticipant := func(addr string, status types.ParticipantStatus) {
		require.NoError(t, k.Participants.Set(ctx, mustAccAddr(t, addr), types.Participant{
			Index:        addr,
			Address:      addr,
			Status:       status,
			ValidatorKey: "valkey-" + addr,
			InferenceUrl: "http://" + addr,
		}))
	}
	for _, addr := range []string{carried, seedReuse, rootOnly, removed, excluded} {
		setParticipant(addr, types.ParticipantStatus_ACTIVE)
	}
	setParticipant(invalid, types.ParticipantStatus_INVALID)

	// Exclusion record for the current epoch.
	require.NoError(t, k.ExcludedParticipantsMap.Set(ctx,
		collections.Join(currentEpochIndex, mustAccAddr(t, excluded)),
		types.ExcludedParticipant{Address: excluded, EpochIndex: currentEpochIndex, Reason: "downtime"},
	))

	// Seeds: carried has both, and the upcoming one must win. seedReuse and
	// rootOnly only have the current epoch's seed. Everyone else has seeds too,
	// so their absence from the result is attributable to the intended filter.
	require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{Participant: carried, EpochIndex: 6, Signature: "sig-new"}))
	for _, addr := range []string{carried, seedReuse, rootOnly, removed, excluded, invalid} {
		require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{Participant: addr, EpochIndex: currentEpochIndex, Signature: "sig-old-" + addr}))
	}

	result := am.fallbackActiveParticipantsFromCurrentEpoch(ctx, upcomingEpoch)

	require.Len(t, result, 3)
	require.True(t, slices.IsSortedFunc(result, func(a, b *types.ActiveParticipant) int {
		if a.Index < b.Index {
			return -1
		}
		if a.Index > b.Index {
			return 1
		}
		return 0
	}))
	require.Nil(t, findByIndex(result, removed))
	require.Nil(t, findByIndex(result, excluded))
	require.Nil(t, findByIndex(result, invalid))

	carriedAP := findByIndex(result, carried)
	require.NotNil(t, carriedAP)
	require.Equal(t, "valkey-"+carried, carriedAP.ValidatorKey)
	require.Equal(t, "http://"+carried, carriedAP.InferenceUrl)
	require.Equal(t, []string{"model-a"}, carriedAP.Models)
	require.Equal(t, int64(100), carriedAP.Weight)
	require.NotNil(t, carriedAP.Seed)
	require.Equal(t, "sig-new", carriedAP.Seed.Signature)
	require.Len(t, carriedAP.MlNodes, 1)
	require.Len(t, carriedAP.MlNodes[0].MlNodes, 2)
	for _, node := range carriedAP.MlNodes[0].MlNodes {
		// Fresh copies: no scheduling state carried over.
		require.Empty(t, node.TimeslotAllocation)
	}

	seedReuseAP := findByIndex(result, seedReuse)
	require.NotNil(t, seedReuseAP)
	require.Equal(t, int64(50), seedReuseAP.Weight)
	require.NotNil(t, seedReuseAP.Seed)
	require.Equal(t, "sig-old-"+seedReuse, seedReuseAP.Seed.Signature)

	rootOnlyAP := findByIndex(result, rootOnly)
	require.NotNil(t, rootOnlyAP)
	require.Empty(t, rootOnlyAP.Models)
	// No subgroup nodes recovered: falls back to the root consensus weight.
	require.Equal(t, int64(70), rootOnlyAP.Weight)
}

func TestFallbackActiveParticipantsSkipsParticipantWithoutSeed(t *testing.T) {
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	const currentEpochIndex = uint64(5)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpochIndex))

	addr := testutil.Executor
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   currentEpochIndex,
		ModelId:      "",
		EpochGroupId: 77,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: addr, Weight: 100},
		},
	})
	require.NoError(t, k.Participants.Set(ctx, mustAccAddr(t, addr), types.Participant{
		Index:        addr,
		Address:      addr,
		Status:       types.ParticipantStatus_ACTIVE,
		ValidatorKey: "valkey",
	}))

	result := am.fallbackActiveParticipantsFromCurrentEpoch(ctx, types.Epoch{Index: 6, PocStartBlockHeight: 600})
	require.Empty(t, result)
}

func TestFallbackActiveParticipantsGuards(t *testing.T) {
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	// Not applicable for the first epoch.
	require.Empty(t, am.fallbackActiveParticipantsFromCurrentEpoch(ctx, types.Epoch{Index: 1}))

	// Current epoch group must directly precede the upcoming epoch.
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   5,
		ModelId:      "",
		EpochGroupId: 77,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Executor, Weight: 100},
		},
	})
	require.Empty(t, am.fallbackActiveParticipantsFromCurrentEpoch(ctx, types.Epoch{Index: 10}))
}

func TestHasPositiveComputePower(t *testing.T) {
	require.False(t, hasPositiveComputePower(nil))
	require.False(t, hasPositiveComputePower([]stakingkeeper.ComputeResult{}))
	require.False(t, hasPositiveComputePower([]stakingkeeper.ComputeResult{{Power: 0}, {Power: -5}}))
	require.True(t, hasPositiveComputePower([]stakingkeeper.ComputeResult{{Power: 0}, {Power: 1}}))
}
