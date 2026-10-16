package inference

import (
	"errors"
	"testing"

	"cosmossdk.io/collections"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func newPreservedCarryoverFixture(t *testing.T) formationRecoveryFixture {
	t.Helper()
	f := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 0, 2)
	// Keep this fixture about carry-over, independently of delegation rewards.
	require.NoError(t, f.keeper.SetDelegationSnapshot(f.ctx, types.DelegationSnapshot{
		SnapshotHeight: f.upcomingEpoch.PocStartBlockHeight,
	}))
	require.NoError(t, f.keeper.SetPreservedNodesSnapshot(f.ctx, types.PreservedNodesSnapshot{
		EpisodeAnchorHeight: f.upcomingEpoch.PocStartBlockHeight,
		ModelPreservedNodes: []*types.ModelPreservedNodes{{
			ModelId: f.modelID,
			Participants: []*types.ParticipantPreservedNodes{{
				ParticipantId: f.veteran,
				NodeIds:       []string{"veteran-node"},
			}},
		}},
	}))
	return f
}

// Mutate eligibility after the snapshot while leaving its preserved nodes and
// historical ValidationWeights intact. Each rejection case isolates one guard.
func TestPreservedCarryoverHonorsCurrentEpochEligibility(t *testing.T) {
	cases := []struct {
		name          string
		status        types.ParticipantStatus
		removed       bool
		excluded      bool
		pastExclusion bool
		missing       bool
		wantCarry     bool
	}{
		{name: "live active", status: types.ParticipantStatus_ACTIVE, wantCarry: true},
		{name: "unconfirmed remains carryable", status: types.ParticipantStatus_UNCONFIRMED, wantCarry: true},
		{name: "invalid after snapshot", status: types.ParticipantStatus_INVALID},
		{name: "inactive after snapshot", status: types.ParticipantStatus_INACTIVE},
		{name: "active but excluded", status: types.ParticipantStatus_ACTIVE, excluded: true},
		{name: "active but removed", status: types.ParticipantStatus_ACTIVE, removed: true},
		{name: "past exclusion does not bar carry", status: types.ParticipantStatus_ACTIVE, pastExclusion: true, wantCarry: true},
		{name: "participant record missing", missing: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPreservedCarryoverFixture(t)
			initial := f.module.PreservedParticipantsFromCurrentEpoch(f.ctx, f.upcomingEpoch)
			require.Len(t, initial, 1, "participant is carryable when the snapshot is taken")
			require.Equal(t, int64(100), initial[0].Weight)
			// Fresh PoC from another participant must not mask a stale carry-over.
			f.addFreshPoC(t, 100)

			participant, found := f.keeper.GetParticipant(f.ctx, f.veteran)
			require.True(t, found)
			participant.Status = tc.status
			require.NoError(t, f.keeper.SetParticipant(f.ctx, participant))
			if tc.missing {
				require.NoError(t, f.keeper.Participants.Remove(f.ctx, mustAccAddr(t, f.veteran)))
			}
			if tc.removed {
				f.groupStub.excludedMembers = map[string]bool{f.veteran: true}
			}
			if tc.excluded || tc.pastExclusion {
				epochIndex := f.currentEpoch.Index
				if tc.pastExclusion {
					epochIndex--
				}
				require.NoError(t, f.keeper.ExcludedParticipantsMap.Set(f.ctx,
					collections.Join(epochIndex, mustAccAddr(t, f.veteran)),
					types.ExcludedParticipant{Address: f.veteran, EpochIndex: epochIndex, Reason: "test exclusion"},
				))
			}

			preserved := f.module.PreservedParticipantsFromCurrentEpoch(f.ctx, f.upcomingEpoch)
			computed := f.module.computeNewWeights(f.ctx, f.upcomingEpoch)
			fresh := findByIndex(computed.participants, f.newcomer)
			require.NotNil(t, fresh)
			require.Equal(t, int64(100), fresh.Weight)
			require.Contains(t, computed.freshNodeIDs[f.newcomer], f.newcomer+"-node")
			if tc.wantCarry {
				require.Len(t, preserved, 1)
				require.Equal(t, f.veteran, preserved[0].Index)
				require.Equal(t, int64(100), preserved[0].Weight)
				carried := findByIndex(computed.participants, f.veteran)
				require.NotNil(t, carried)
				require.Equal(t, int64(100), carried.Weight)
				require.Len(t, computed.participants, 2)
			} else {
				require.Empty(t, preserved)
				require.Nil(t, findByIndex(computed.participants, f.veteran))
				require.Len(t, computed.participants, 1)
			}
		})
	}
}

func TestPreservedCarryoverRequiresLiveCurrentEpoch(t *testing.T) {
	t.Run("first epoch", func(t *testing.T) {
		f := newPreservedCarryoverFixture(t)
		require.Empty(t, f.module.PreservedParticipantsFromCurrentEpoch(f.ctx, types.Epoch{Index: 1}))
		require.Zero(t, f.groupStub.memberReadsByGroup[77])
	})
	t.Run("epoch mismatch", func(t *testing.T) {
		f := newPreservedCarryoverFixture(t)
		require.Empty(t, f.module.PreservedParticipantsFromCurrentEpoch(f.ctx, types.Epoch{Index: 3}))
	})
	t.Run("membership read fails", func(t *testing.T) {
		f := newPreservedCarryoverFixture(t)
		f.groupStub.membersErr = errors.New("group membership unavailable")
		require.Empty(t, f.module.PreservedParticipantsFromCurrentEpoch(f.ctx, f.upcomingEpoch))
	})
	t.Run("no snapshot needs no membership read", func(t *testing.T) {
		f := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 0, 2)
		f.groupStub.membersErr = errors.New("group membership must not be queried")
		require.Empty(t, f.module.PreservedParticipantsFromCurrentEpoch(f.ctx, f.upcomingEpoch))
		require.Zero(t, f.groupStub.memberReadsByGroup[77])
	})
}

// The same participant may prove fresh nodes for the upcoming epoch. Only its
// old preserved weight is dropped; the inference-serving node must not sneak
// back in through the fresh distribution either.
func TestPreservedCarryoverExclusionKeepsFreshPoC(t *testing.T) {
	f := newPreservedCarryoverFixture(t)
	f.addFreshPoC(t, 100)
	participant, found := f.keeper.GetParticipant(f.ctx, f.veteran)
	require.True(t, found)
	participant.Status = types.ParticipantStatus_INVALID
	require.NoError(t, f.keeper.SetParticipant(f.ctx, participant))
	f.groupStub.excludedMembers = map[string]bool{f.veteran: true}
	require.NoError(t, f.keeper.ExcludedParticipantsMap.Set(f.ctx,
		collections.Join(f.currentEpoch.Index, mustAccAddr(t, f.veteran)),
		types.ExcludedParticipant{Address: f.veteran, EpochIndex: f.currentEpoch.Index, Reason: "invalid"},
	))
	require.NoError(t, f.keeper.SetPoCV2StoreCommit(f.ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       f.veteran,
		PocStageStartBlockHeight: f.upcomingEpoch.PocStartBlockHeight,
		Count:                    117,
		ModelId:                  f.modelID,
	}))
	require.NoError(t, f.keeper.SetMLNodeWeightDistribution(f.ctx, types.MLNodeWeightDistribution{
		ParticipantAddress:       f.veteran,
		PocStageStartBlockHeight: f.upcomingEpoch.PocStartBlockHeight,
		ModelId:                  f.modelID,
		Weights: []*types.MLNodeWeight{
			{NodeId: "veteran-node", Weight: 100},
			{NodeId: "fresh-veteran-node", Weight: 17},
		},
	}))
	require.NoError(t, f.keeper.SetPocValidationV2(f.ctx, types.PoCValidationV2{
		ParticipantAddress:          f.veteran,
		ValidatorParticipantAddress: f.veteran,
		PocStageStartBlockHeight:    f.upcomingEpoch.PocStartBlockHeight,
		ValidatedWeight:             117,
		ModelId:                     f.modelID,
	}))

	require.Empty(t, f.module.PreservedParticipantsFromCurrentEpoch(f.ctx, f.upcomingEpoch))
	computed := f.module.computeNewWeights(f.ctx, f.upcomingEpoch)
	proved := findByIndex(computed.participants, f.veteran)
	require.NotNil(t, proved, "current-epoch exclusion must not bar fresh PoC")
	require.Equal(t, int64(17), proved.Weight)
	require.Equal(t, []string{f.modelID}, proved.Models)
	require.Len(t, proved.MlNodes, 1)
	require.Len(t, proved.MlNodes[0].MlNodes, 1)
	require.Equal(t, "fresh-veteran-node", proved.MlNodes[0].MlNodes[0].NodeId)
	require.Len(t, computed.freshNodeIDs[f.veteran], 1)
	require.Contains(t, computed.freshNodeIDs[f.veteran], "fresh-veteran-node")
}

// Drive the mixed preserved/fresh set through formation and activation. A
// preserved-only test would fall back and could miss the unguarded carry path.
func TestEpochFormationDoesNotReactivateExcludedPreservedParticipant(t *testing.T) {
	for _, status := range []types.ParticipantStatus{types.ParticipantStatus_INVALID, types.ParticipantStatus_INACTIVE} {
		t.Run(status.String(), func(t *testing.T) {
			f := newPreservedCarryoverFixture(t)
			f.addFreshPoC(t, 100)
			require.Len(t, f.module.PreservedParticipantsFromCurrentEpoch(f.ctx, f.upcomingEpoch), 1)
			participant, found := f.keeper.GetParticipant(f.ctx, f.veteran)
			require.True(t, found)
			participant.Status = status
			require.NoError(t, f.keeper.SetParticipant(f.ctx, participant))
			require.NoError(t, f.keeper.ExcludedParticipantsMap.Set(f.ctx,
				collections.Join(f.currentEpoch.Index, mustAccAddr(t, f.veteran)),
				types.ExcludedParticipant{Address: f.veteran, EpochIndex: f.currentEpoch.Index, Reason: "test exclusion"},
			))
			currentGroup, err := f.keeper.GetCurrentEpochGroup(f.ctx)
			require.NoError(t, err)
			require.NoError(t, currentGroup.RemoveMember(f.ctx, &participant))

			require.NoError(t, f.module.onEndOfPoCValidationStage(f.ctx, 250, 1_000))
			stored, found := f.keeper.GetActiveParticipants(f.ctx, f.upcomingEpoch.Index)
			require.True(t, found)
			require.Len(t, stored.Participants, 1)
			require.Equal(t, f.newcomer, stored.Participants[0].Index)
			require.Positive(t, stored.Participants[0].Weight)
			root, found := f.keeper.GetEpochGroupData(f.ctx, f.upcomingEpoch.Index, "")
			require.True(t, found)
			require.Len(t, root.ValidationWeights, 1)
			require.Equal(t, f.newcomer, root.ValidationWeights[0].MemberAddress)
			_, usedFallback := fallbackEventReason(f.ctx)
			require.False(t, usedFallback, "fresh PoC must exercise the normal formation path")

			f.module.moveUpcomingToEffectiveGroup(f.ctx, 300, 1)
			participant, found = f.keeper.GetParticipant(f.ctx, f.veteran)
			require.True(t, found)
			require.Equal(t, status, participant.Status, "excluded preserved weight must not reactivate its owner")
			newcomer, found := f.keeper.GetParticipant(f.ctx, f.newcomer)
			require.True(t, found)
			require.Equal(t, types.ParticipantStatus_ACTIVE, newcomer.Status)
		})
	}
}
