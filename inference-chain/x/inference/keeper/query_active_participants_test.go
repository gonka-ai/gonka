package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/types"
)

func TestActiveParticipantsQueryEffectiveEpoch(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	addrA := sample.AccAddress()
	addrB := sample.AccAddress()
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 5,
		Participants: []*types.ActiveParticipant{
			{Index: addrA, InferenceUrl: "http://a:8080", Weight: 10},
		},
	}))
	// Upcoming PoC cohort exists under latest (6) but must not be returned for epoch_index=0.
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 6,
		Participants: []*types.ActiveParticipant{
			{Index: addrB, InferenceUrl: "http://b:8080", Weight: 20},
		},
	}))

	resp, err := k.ActiveParticipants(ctx, &types.QueryActiveParticipantsRequest{EpochIndex: 0})
	require.NoError(t, err)
	require.Equal(t, uint64(5), resp.EpochIndex)
	require.Len(t, resp.ActiveParticipants.Participants, 1)
	require.Equal(t, addrA, resp.ActiveParticipants.Participants[0].Index)
}

func TestActiveParticipantsQueryExplicitEpoch(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	addrB := sample.AccAddress()
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 6,
		Participants: []*types.ActiveParticipant{
			{Index: addrB, InferenceUrl: "http://b:8080", Weight: 20},
		},
	}))

	resp, err := k.ActiveParticipants(ctx, &types.QueryActiveParticipantsRequest{EpochIndex: 6})
	require.NoError(t, err)
	require.Equal(t, uint64(6), resp.EpochIndex)
	require.Equal(t, addrB, resp.ActiveParticipants.Participants[0].Index)
}

func TestActiveParticipantsQueryNotFound(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))

	_, err := k.ActiveParticipants(ctx, &types.QueryActiveParticipantsRequest{EpochIndex: 0})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestActiveParticipantsQueryInvalidRequest(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	_, err := k.ActiveParticipants(ctx, nil)
	require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))
}
