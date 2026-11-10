package inference

import (
	"slices"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
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

// setupCarryableCurrentEpoch prepares the minimal state so that
// fallbackActiveParticipantsFromCurrentEpoch can carry one participant with one
// model node into upcoming epoch 6. The participant has no hardware record, so
// re-seating keeps its carried assignment (bootstrap path).
func setupCarryableCurrentEpoch(t *testing.T, k keeper.Keeper, ctx sdk.Context, addr string, model string, weight int64) {
	t.Helper()
	const currentEpochIndex = uint64(5)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpochIndex))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:     currentEpochIndex,
		ModelId:        "",
		EpochGroupId:   77,
		SubGroupModels: []string{model},
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: addr, Weight: weight},
		},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   currentEpochIndex,
		ModelId:      model,
		EpochGroupId: 78,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: addr, Weight: weight, MlNodes: []*types.MLNodeInfo{
				{NodeId: "fb-node", PocWeight: weight},
			}},
		},
	})
	require.NoError(t, k.Participants.Set(ctx, mustAccAddr(t, addr), types.Participant{
		Index:        addr,
		Address:      addr,
		Status:       types.ParticipantStatus_ACTIVE,
		ValidatorKey: "valkey-" + addr,
		InferenceUrl: "http://" + addr,
	}))
	require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{Participant: addr, EpochIndex: currentEpochIndex, Signature: "sig-" + addr}))
}

// freshParticipantWithHardware builds a computed participant whose single node
// proved `validatedModel`, with a registered hardware record declaring
// `declaredModel` for that node.
func freshParticipantWithHardware(t *testing.T, k keeper.Keeper, ctx sdk.Context, addr, nodeId, validatedModel, declaredModel string, weight int64) *types.ActiveParticipant {
	t.Helper()
	require.NoError(t, k.SetHardwareNodes(ctx, &types.HardwareNodes{
		Participant: addr,
		HardwareNodes: []*types.HardwareNode{
			{LocalId: nodeId, Models: []string{declaredModel}},
		},
	}))
	return &types.ActiveParticipant{
		Index:  addr,
		Models: []string{validatedModel},
		MlNodes: []*types.ModelMLNodes{
			{MlNodes: []*types.MLNodeInfo{{NodeId: nodeId, PocWeight: weight}}},
		},
	}
}

// The hardware filter removing every fresh assignment must not activate a
// zero-weight epoch: the guard falls back to the current epoch's validators.
func TestSeatAndGuard_AllAssignmentsFilteredFallsBackToCurrentEpoch(t *testing.T) {
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	upcomingEpoch := types.Epoch{Index: 6, PocStartBlockHeight: 600}

	k.SetModel(ctx, &types.Model{ProposedBy: "genesis", Id: "model-a"})
	k.SetModel(ctx, &types.Model{ProposedBy: "genesis", Id: "model-b"})

	carried := testutil.Executor
	setupCarryableCurrentEpoch(t, k, ctx, carried, "model-a", 70)

	// Fresh participant proved model-a, but its inventory now declares only
	// model-b -> every assignment is filtered, seated weight is zero.
	fresh := []*types.ActiveParticipant{
		freshParticipantWithHardware(t, k, ctx, testutil.Executor2, "n1", "model-a", "model-b", 100),
	}

	result, usedFallback := am.seatAndGuardParticipants(ctx, upcomingEpoch, fresh, participantNodeIDs(fresh))

	require.Len(t, result, 1)
	require.True(t, usedFallback)
	require.Equal(t, carried, result[0].Index, "fallback must re-seat the current epoch validator")
	require.Equal(t, []string{"model-a"}, result[0].Models)
	require.Equal(t, int64(70), seatedRawWeight(result[0]))
	require.Nil(t, findByIndex(result, testutil.Executor2), "zero-seated fresh participant must not survive")
}

// Mixed outcome: participants that keep seated weight survive, zero-seated ones
// are removed, and no fallback is triggered.
func TestSeatAndGuard_MixedRemovesZeroSeatedKeepsOthers(t *testing.T) {
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	upcomingEpoch := types.Epoch{Index: 6, PocStartBlockHeight: 600}

	k.SetModel(ctx, &types.Model{ProposedBy: "genesis", Id: "model-a"})
	k.SetModel(ctx, &types.Model{ProposedBy: "genesis", Id: "model-b"})

	survivor := freshParticipantWithHardware(t, k, ctx, testutil.Executor, "s1", "model-a", "model-a", 100)
	zeroed := freshParticipantWithHardware(t, k, ctx, testutil.Executor2, "z1", "model-a", "model-b", 50)
	fresh := []*types.ActiveParticipant{survivor, zeroed}

	result, usedFallback := am.seatAndGuardParticipants(ctx, upcomingEpoch, fresh, participantNodeIDs(fresh))

	require.Len(t, result, 1)
	require.False(t, usedFallback)
	require.Equal(t, testutil.Executor, result[0].Index)
	require.Equal(t, int64(100), seatedRawWeight(result[0]))

	for _, e := range ctx.EventManager().Events() {
		require.NotEqual(t, "empty_epoch_fallback_applied", e.Type, "fallback must not fire on a mixed result")
		require.NotEqual(t, "epoch_error", e.Type)
	}
}

// Pre-existing behavior preserved: an empty ComputeNewWeights result still
// reaches the fallback (now with the seating guard applied to the fallback too).
func TestSeatAndGuard_EmptyComputeFallsBack(t *testing.T) {
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	upcomingEpoch := types.Epoch{Index: 6, PocStartBlockHeight: 600}

	carried := testutil.Executor
	setupCarryableCurrentEpoch(t, k, ctx, carried, "model-a", 40)

	result, usedFallback := am.seatAndGuardParticipants(ctx, upcomingEpoch, nil, nil)

	require.Len(t, result, 1)
	require.True(t, usedFallback)
	require.Equal(t, carried, result[0].Index)
	require.Equal(t, int64(40), seatedRawWeight(result[0]))
}

// When the hardware filter would also wipe the fallback carry, keep the
// current-epoch assignments so the epoch index can still advance.
func TestSeatAndGuard_FallbackHardwareZeroedKeepsCarriedTeam(t *testing.T) {
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	upcomingEpoch := types.Epoch{Index: 6, PocStartBlockHeight: 600}

	k.SetModel(ctx, &types.Model{ProposedBy: "genesis", Id: "model-a"})
	k.SetModel(ctx, &types.Model{ProposedBy: "genesis", Id: "model-b"})

	carried := testutil.Executor
	setupCarryableCurrentEpoch(t, k, ctx, carried, "model-a", 70)
	// The carried participant's inventory also dropped its model.
	require.NoError(t, k.SetHardwareNodes(ctx, &types.HardwareNodes{
		Participant: carried,
		HardwareNodes: []*types.HardwareNode{
			{LocalId: "fb-node", Models: []string{"model-b"}},
		},
	}))

	fresh := []*types.ActiveParticipant{
		freshParticipantWithHardware(t, k, ctx, testutil.Executor2, "n1", "model-a", "model-b", 100),
	}

	result, usedFallback := am.seatAndGuardParticipants(ctx, upcomingEpoch, fresh, participantNodeIDs(fresh))

	require.Len(t, result, 1)
	require.True(t, usedFallback)
	require.Equal(t, carried, result[0].Index)
	require.Equal(t, []string{"model-a"}, result[0].Models)
	require.Equal(t, int64(70), seatedRawWeight(result[0]))
}

func TestSeatAndGuard_PreservedOnlyFallsBackToCurrentEpoch(t *testing.T) {
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	upcomingEpoch := types.Epoch{Index: 6, PocStartBlockHeight: 600}

	carried := testutil.Executor
	setupCarryableCurrentEpoch(t, k, ctx, carried, "model-a", 70)

	preservedOnly := []*types.ActiveParticipant{{
		Index:  testutil.Executor2,
		Models: []string{"model-a"},
		MlNodes: []*types.ModelMLNodes{{
			MlNodes: []*types.MLNodeInfo{{NodeId: "preserved-node", PocWeight: 100}},
		}},
	}}

	result, usedFallback := am.seatAndGuardParticipants(ctx, upcomingEpoch, preservedOnly, nil)

	require.Len(t, result, 1)
	require.True(t, usedFallback)
	require.Equal(t, carried, result[0].Index)
	require.Nil(t, findByIndex(result, testutil.Executor2), "preserved-only subset must not bypass epoch fallback")
}

func TestSeatAndGuard_FreshFilteredPreservedSurvivesFallsBack(t *testing.T) {
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	upcomingEpoch := types.Epoch{Index: 6, PocStartBlockHeight: 600}

	k.SetModel(ctx, &types.Model{ProposedBy: "genesis", Id: "model-a"})
	k.SetModel(ctx, &types.Model{ProposedBy: "genesis", Id: "model-b"})

	carried := testutil.Executor
	setupCarryableCurrentEpoch(t, k, ctx, carried, "model-a", 70)

	preserved := &types.ActiveParticipant{
		Index:  testutil.Executor2,
		Models: []string{"model-a"},
		MlNodes: []*types.ModelMLNodes{{
			MlNodes: []*types.MLNodeInfo{{NodeId: "preserved-node", PocWeight: 40}},
		}},
	}
	fresh := freshParticipantWithHardware(
		t,
		k,
		ctx,
		testutil.Validator,
		"fresh-node",
		"model-a",
		"model-b",
		100,
	)
	computed := []*types.ActiveParticipant{preserved, fresh}

	result, usedFallback := am.seatAndGuardParticipants(
		ctx,
		upcomingEpoch,
		computed,
		participantNodeIDs([]*types.ActiveParticipant{fresh}),
	)

	require.Len(t, result, 1)
	require.True(t, usedFallback)
	require.Equal(t, carried, result[0].Index)
	require.Nil(t, findByIndex(result, testutil.Executor2), "surviving preserved node must not mask filtered fresh PoC")
	require.Nil(t, findByIndex(result, testutil.Validator))
}

func TestApplyZeroTrustFallback(t *testing.T) {
	t.Run("restores positive real weights", func(t *testing.T) {
		k, ctx := newMinimalInferenceKeeper(t)
		am := NewAppModule(nil, k, nil, nil, nil, nil)
		participants := []*types.ActiveParticipant{
			{Index: "a", Weight: 100},
			nil,
			{Index: "b", Weight: 50},
		}

		require.True(t, am.applyZeroTrustFallback(ctx, 6, participants))
		require.Equal(t, int64(100), participants[0].CapWeight)
		require.Equal(t, int64(50), participants[2].CapWeight)

		var emitted bool
		for _, event := range ctx.EventManager().Events() {
			if event.Type == "zero_trust_fallback_applied" {
				emitted = true
			}
		}
		require.True(t, emitted)
	})

	t.Run("does not override a positive trust vector", func(t *testing.T) {
		k, ctx := newMinimalInferenceKeeper(t)
		am := NewAppModule(nil, k, nil, nil, nil, nil)
		participants := []*types.ActiveParticipant{
			{Index: "a", Weight: 100, CapWeight: 20},
			{Index: "b", Weight: 50},
		}

		require.False(t, am.applyZeroTrustFallback(ctx, 6, participants))
		require.Equal(t, int64(20), participants[0].CapWeight)
		require.Zero(t, participants[1].CapWeight)
	})

	t.Run("does not fabricate trust without real weight", func(t *testing.T) {
		k, ctx := newMinimalInferenceKeeper(t)
		am := NewAppModule(nil, k, nil, nil, nil, nil)
		participants := []*types.ActiveParticipant{
			{Index: "a"},
			{Index: "b", Weight: -5},
		}

		require.False(t, am.applyZeroTrustFallback(ctx, 6, participants))
		require.Zero(t, participants[0].CapWeight)
		require.Zero(t, participants[1].CapWeight)
	})
}

func TestHasPositiveComputePower(t *testing.T) {
	require.False(t, hasPositiveComputePower(nil))
	require.False(t, hasPositiveComputePower([]stakingkeeper.ComputeResult{}))
	require.False(t, hasPositiveComputePower([]stakingkeeper.ComputeResult{{Power: 0}, {Power: -5}}))
	require.True(t, hasPositiveComputePower([]stakingkeeper.ComputeResult{{Power: 0}, {Power: 1}}))
}
