package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// The proposal records the effective epoch index; the Epoch record itself is not needed.
func TestMsgServer_SubmitUnitOfComputePriceProposal_RecordsEffectiveEpoch(t *testing.T) {
	k, ms, ctx, _ := setupPermissionsHarness(t)
	signer := testutil.Creator
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 7))
	require.NoError(t, k.ActiveParticipantsSet.Set(ctx, collections.Join(uint64(7), sdk.MustAccAddressFromBech32(signer))))

	c := keeper.WithTxParamsCache(ctx.WithGasMeter(storetypes.NewInfiniteGasMeter()).WithBlockHeight(700))
	_, err := ms.SubmitUnitOfComputePriceProposal(c, &types.MsgSubmitUnitOfComputePriceProposal{Creator: signer, Price: 42})
	require.NoError(t, err)
	t.Logf("gas: %d", c.GasMeter().GasConsumed())

	proposal, found := k.GettUnitOfComputePriceProposal(ctx, signer)
	require.True(t, found)
	require.Equal(t, uint64(7), proposal.ProposedAtEpoch)
	require.Equal(t, uint64(700), proposal.ProposedAtBlockHeight)
	require.Equal(t, uint64(42), proposal.Price)
}
