package keeper_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestDelegationRewardTransferSnapshotForEpoch(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	require.NoError(t, k.SetDelegationRewardTransferSnapshot(ctx, types.DelegationRewardTransferSnapshot{
		EpochIndex: 7,
		Transfers: []*types.DelegationRewardTransfer{
			{
				ModelId: "model-a",
				From:    "alice",
				To:      "bob",
				Share:   types.DecimalFromFloat(0.05),
			},
		},
		Penalties: []*types.DelegationRewardPenalty{
			{
				Participant:     "carol",
				PenaltyFraction: types.DecimalFromFloat(0.2),
			},
		},
	}))
	transfers, err := k.GetDelegationRewardTransfersForEpoch(ctx, 7)
	require.NoError(t, err)
	require.Len(t, transfers, 1)
	require.Equal(t, "alice", transfers[0].From)
	require.Equal(t, "bob", transfers[0].To)

	penalties, err := k.GetDelegationRewardPenaltiesForEpoch(ctx, 7)
	require.NoError(t, err)
	require.Len(t, penalties, 1)
	require.Equal(t, "carol", penalties[0].Participant)

	transfers, err = k.GetDelegationRewardTransfersForEpoch(ctx, 8)
	require.NoError(t, err)
	require.Empty(t, transfers)

	penalties, err = k.GetDelegationRewardPenaltiesForEpoch(ctx, 8)
	require.NoError(t, err)
	require.Empty(t, penalties)
}

func TestDelegationRewardTransferSnapshotOverwritesPreviousEpoch(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	require.NoError(t, k.SetDelegationRewardTransferSnapshot(ctx, types.DelegationRewardTransferSnapshot{
		EpochIndex: 7,
		Transfers:  []*types.DelegationRewardTransfer{{ModelId: "m", From: "a", To: "b", Share: types.DecimalFromFloat(0.1)}},
	}))
	require.NoError(t, k.SetDelegationRewardTransferSnapshot(ctx, types.DelegationRewardTransferSnapshot{
		EpochIndex: 8,
		Transfers:  []*types.DelegationRewardTransfer{{ModelId: "m", From: "c", To: "d", Share: types.DecimalFromFloat(0.2)}},
	}))

	transfers7, err := k.GetDelegationRewardTransfersForEpoch(ctx, 7)
	require.NoError(t, err)
	require.Empty(t, transfers7)

	transfers8, err := k.GetDelegationRewardTransfersForEpoch(ctx, 8)
	require.NoError(t, err)
	require.Len(t, transfers8, 1)
	require.Equal(t, "c", transfers8[0].From)
}

func BenchmarkDelegationRewardTransferSnapshot1000Participants10Models(b *testing.B) {
	k, ctx := keepertest.InferenceKeeper(b)
	const (
		epoch        = uint64(7)
		participants = 1000
		models       = 10
	)

	transfers := make([]*types.DelegationRewardTransfer, 0, participants*models)
	for model := 0; model < models; model++ {
		for participant := 0; participant < participants; participant++ {
			transfers = append(transfers, &types.DelegationRewardTransfer{
				ModelId: fmt.Sprintf("model-%02d", model),
				From:    fmt.Sprintf("participant-%04d", participant),
				To:      fmt.Sprintf("delegate-%04d", participant),
				Share:   types.DecimalFromFloat(0.05),
			})
		}
	}
	require.NoError(b, k.SetDelegationRewardTransferSnapshot(ctx, types.DelegationRewardTransferSnapshot{
		EpochIndex: epoch,
		Transfers:  transfers,
	}))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := k.GetDelegationRewardTransfersForEpoch(ctx, epoch)
		if err != nil {
			b.Fatal(err)
		}
		if len(got) != participants*models {
			b.Fatalf("expected %d transfers, got %d", participants*models, len(got))
		}
	}
}
