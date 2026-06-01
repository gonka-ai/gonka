package keeper_test

import (
	"context"
	"strconv"
	"testing"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Prevent strconv unused error
var _ = strconv.IntSize

func createNActiveParticipants(keeper keeper.Keeper, ctx context.Context, n int) []types.ActiveParticipants {
	items := make([]types.ActiveParticipants, n)
	for i := range items {
		items[i].EpochGroupId = uint64(i)
		items[i].EpochId = uint64(i)
		_, _, addr := testdata.KeyTestPubAddr()
		items[i].Participants = []*types.ActiveParticipant{
			{
				Index: addr.String(),
			},
		}
		items[i].PocStartBlockHeight = int64(i * 100)
		items[i].EffectiveBlockHeight = int64(i*100 + 10)
		items[i].CreatedAtBlockHeight = int64(i*100 - 10)
		keeper.SetActiveParticipants(ctx, items[i])
	}
	return items
}

func TestActiveParticipantsGet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNActiveParticipants(keeper, ctx, 10)
	for _, item := range items {
		rst, found := keeper.GetActiveParticipants(ctx, item.EpochId)
		require.True(t, found)
		require.Equal(t,
			nullify.Fill(&item),
			nullify.Fill(&rst),
		)
	}
}

func TestActiveParticipantsGetNotFound(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	_, found := keeper.GetActiveParticipants(ctx, 999)
	require.False(t, found)
}

func TestSetActiveParticipants(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	_, _, addr0 := testdata.KeyTestPubAddr()
	_, _, addr1 := testdata.KeyTestPubAddr()
	_, _, addr2 := testdata.KeyTestPubAddr()

	// Create and set active participants
	participants := types.ActiveParticipants{
		EpochGroupId: 1,
		EpochId:      1,
		Participants: []*types.ActiveParticipant{
			{
				Index:        addr0.String(),
				ValidatorKey: "validator0",
				Weight:       100,
			},
			{
				Index:        addr1.String(),
				ValidatorKey: "validator1",
				Weight:       200,
			},
		},
		PocStartBlockHeight:  100,
		EffectiveBlockHeight: 110,
		CreatedAtBlockHeight: 90,
	}

	err := keeper.SetActiveParticipants(ctx, participants)
	require.NoError(t, err)

	// Verify cache
	has, err := keeper.ActiveParticipantsSet.Has(ctx, collections.Join(uint64(1), addr0))
	require.NoError(t, err)
	require.True(t, has)
	has, err = keeper.ActiveParticipantsSet.Has(ctx, collections.Join(uint64(1), addr1))
	require.NoError(t, err)
	require.True(t, has)

	// Retrieve and verify
	retrieved, found := keeper.GetActiveParticipants(ctx, 1)
	require.True(t, found)
	require.Equal(t, 2, len(retrieved.Participants))

	// Update and verify
	newParticipant := &types.ActiveParticipant{
		Index:        addr2.String(),
		ValidatorKey: "validator2",
		Weight:       300,
	}

	updatedParticipants := types.ActiveParticipants{
		EpochId:              1,
		EpochGroupId:         1,
		Participants:         append(retrieved.Participants, newParticipant),
		PocStartBlockHeight:  retrieved.PocStartBlockHeight,
		EffectiveBlockHeight: retrieved.EffectiveBlockHeight,
		CreatedAtBlockHeight: retrieved.CreatedAtBlockHeight,
	}

	err = keeper.SetActiveParticipants(ctx, updatedParticipants)
	require.NoError(t, err)

	// Verify updated cache
	has, err = keeper.ActiveParticipantsSet.Has(ctx, collections.Join(uint64(1), addr2))
	require.NoError(t, err)
	require.True(t, has)

	retrieved, found = keeper.GetActiveParticipants(ctx, 1)
	require.True(t, found)
	require.Equal(t, 3, len(retrieved.Participants))
}

func ParticipantToActive(p *types.Participant) *types.ActiveParticipant {
	return &types.ActiveParticipant{
		Index:        p.Index,
		Weight:       int64(p.Weight),
		InferenceUrl: p.InferenceUrl,
		ValidatorKey: p.ValidatorKey,
	}
}

func ParticipantsToActive(epochId int64, participants ...types.Participant) types.ActiveParticipants {
	activeParticipants := make([]*types.ActiveParticipant, len(participants))
	for i, p := range participants {
		activeParticipants[i] = ParticipantToActive(&p)
	}
	return types.ActiveParticipants{
		Participants: activeParticipants,
		EpochId:      uint64(epochId),
	}
}
