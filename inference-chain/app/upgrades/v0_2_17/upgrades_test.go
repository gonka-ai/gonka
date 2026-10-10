package v0_2_17

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	ibcexported "github.com/cosmos/ibc-go/v8/modules/core/exported"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/stretchr/testify/require"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.17", UpgradeName)
}

type recordingClientKeeper struct{ params []clienttypes.Params }

func (r *recordingClientKeeper) SetParams(_ sdk.Context, p clienttypes.Params) {
	r.params = append(r.params, p)
}

func TestDisableLocalhostClientAllowsOnlyTendermint(t *testing.T) {
	_, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	r := &recordingClientKeeper{}
	disableLocalhostClient(ctx, r)
	require.Len(t, r.params, 1)
	require.True(t, r.params[0].IsAllowedClient(ibcexported.Tendermint))
	require.False(t, r.params[0].IsAllowedClient(ibcexported.Localhost))
}
