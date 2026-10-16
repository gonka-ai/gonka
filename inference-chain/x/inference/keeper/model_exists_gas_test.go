package keeper_test

import (
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
)

// mainnetShapedModel mirrors a governance model record on mainnet (~310 bytes encoded).
func mainnetShapedModel(id string) *types.Model {
	return &types.Model{
		ProposedBy:             "gonka10d07y265gmmuvt4z0w9aw880jnsr700j2h5m33",
		Id:                     id,
		UnitsOfComputePerToken: 10000,
		HfRepo:                 "MiniMaxAI/MiniMax-M2.7",
		HfCommit:               "d494266a4affc0d2995ba1fa35c8481cbd84294b",
		ModelArgs: []string{"--enable-auto-tool-choice", "--max-model-len", "180000", "--kv-cache-dtype", "fp8",
			"--tool-call-parser", "minimax_m2", "--reasoning-parser", "minimax_m2_append_think"},
		VRam:                10000,
		ThroughputPerNonce:  5000,
		ValidationThreshold: &types.Decimal{Value: 922, Exponent: -3},
	}
}

// Existence checks must not pay per-byte read gas for the model record.
func TestPoCV2StoreCommit_GasIndependentOfModelSize(t *testing.T) {
	gasFor := func(model *types.Model) storetypes.Gas {
		k, ctx, ms := setupPoCV2StoreCommitTest(t, 110, nil)
		k.SetModel(ctx, model)
		ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		_, err := ms.PoCV2StoreCommit(ctx, &types.MsgPoCV2StoreCommit{
			Creator:                  testutil.Executor,
			PocStageStartBlockHeight: 100,
			Entries:                  []*types.PoCV2CommitEntry{makePoCV2CommitEntry(testPoCModelID, 3, 1)},
		})
		require.NoError(t, err)
		return ctx.GasMeter().GasConsumed()
	}
	small := gasFor(&types.Model{Id: testPoCModelID})
	big := gasFor(mainnetShapedModel(testPoCModelID))
	t.Logf("StoreCommit gas: bare model %d, mainnet-shaped model %d", small, big)
	require.Equal(t, small, big)
}

func TestSetPoCDelegation_GasIndependentOfRecordSize(t *testing.T) {
	gasFor := func(model *types.Model, delegate types.Participant) storetypes.Gas {
		k, ctx, ms := setupPoCV2StoreCommitTest(t, 110, nil)
		k.SetModel(ctx, model)
		require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(testutil.Validator), delegate))
		ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		_, err := ms.SetPoCDelegation(ctx, &types.MsgSetPoCDelegation{
			Sender:     testutil.Executor,
			ModelId:    testPoCModelID,
			DelegateTo: testutil.Validator,
		})
		require.NoError(t, err)
		return ctx.GasMeter().GasConsumed()
	}
	bare := types.Participant{Index: testutil.Validator, Address: testutil.Validator}
	full := bare
	full.InferenceUrl = "http://node.example.com:8000"
	full.ValidatorKey = "AAAAC3NzaC1lZDI1NTE5AAAAIGb4Xq2dJk8o1m7n3S0PqRz2yK5mVtW9cE1fA4hB6uLx"
	full.CurrentEpochStats = &types.CurrentEpochStats{InferenceCount: 77_000, MissedRequests: 1200, EarnedCoins: 1 << 40}
	small := gasFor(&types.Model{Id: testPoCModelID}, bare)
	big := gasFor(mainnetShapedModel(testPoCModelID), full)
	t.Logf("SetPoCDelegation gas: bare records %d, filled records %d", small, big)
	require.Equal(t, small, big)
}
