package keeper_test

import (
	"errors"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestCheckPoCV2StoreCommitRecheckOverlap_MissingKeyIsNotAnError(t *testing.T) {
	k, goCtx, _ := keepertest.InferenceKeeperReturningMocks(t)
	ctx := sdk.UnwrapSDKContext(goCtx).WithIsReCheckTx(true)

	err := keeper.CheckPoCV2StoreCommitRecheckOverlapForTesting(k, ctx, &types.MsgPoCV2StoreCommit{
		Creator:                  testutil.Creator,
		PocStageStartBlockHeight: 100,
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:  "test-model",
			Count:    10,
			RootHash: make([]byte, 32),
		}},
	})
	require.NoError(t, err)
}

func TestCheckPoCV2StoreCommitRecheckOverlap_StoreError(t *testing.T) {
	k, goCtx, _ := keepertest.InferenceKeeperReturningMocks(t)
	ctx := sdk.UnwrapSDKContext(goCtx).WithIsReCheckTx(true)

	addr := sdk.MustAccAddressFromBech32(testutil.Creator)
	require.NoError(t, keeper.SetPoCV2StoreCommitRawBytesForTesting(
		k, ctx, 100, addr, "test-model", []byte{0xff, 0x00, 0x01},
	))

	err := keeper.CheckPoCV2StoreCommitRecheckOverlapForTesting(k, ctx, &types.MsgPoCV2StoreCommit{
		Creator:                  testutil.Creator,
		PocStageStartBlockHeight: 100,
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:  "test-model",
			Count:    10,
			RootHash: make([]byte, 32),
		}},
	})
	require.Error(t, err)
	require.False(t, errors.Is(err, collections.ErrNotFound))
	require.False(t, errors.Is(err, types.ErrIllegalState))
}
