package app_test

import (
	"testing"
	"time"

	"cosmossdk.io/collections"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/productscience/inference/app/upgrades/v0_2_18"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Test the actual release handler and registered 15 -> 16 migration, rather
// than calling SeedWarmKeyLog directly. This runs on both legacy and hook SDKs.
func TestWarmKeyLogV7ChainUpgrade(t *testing.T) {
	testApp := createTestApp(t)
	now := time.Now().UTC()
	expiration := now.Add(time.Hour)
	ctx := warmKeyCtx(testApp).WithBlockHeight(500).WithBlockTime(now)
	granter := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
	warm := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
	other := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
	require.NoError(t, testApp.AuthzKeeper.SaveGrant(ctx, warm, granter, authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL), &expiration))
	require.NoError(t, testApp.AuthzKeeper.SaveGrant(ctx, other, granter, authz.NewGenericAuthorization("/cosmos.bank.v1beta1.MsgSend"), nil))
	// Emulate the pre-upgrade store. SDK23 may have recorded the setup write
	// through hooks, but the old chain had no warm-key collections at all.
	require.NoError(t, testApp.InferenceKeeper.WarmKeyLogEntriesMap.Clear(ctx, nil))
	require.NoError(t, testApp.InferenceKeeper.WarmKeyLive.Clear(ctx, nil))
	require.NoError(t, testApp.InferenceKeeper.WarmKeyLogNextID.Clear(ctx, nil))
	from := testApp.ModuleManager.GetVersionMap()
	from[inferencetypes.ModuleName] = 15
	handler := v0_2_18.CreateUpgradeHandler(testApp.ModuleManager, testApp.Configurator(), testApp.InferenceKeeper)
	to, err := handler(ctx, upgradetypes.Plan{Name: v0_2_18.UpgradeName, Height: 500}, from)
	require.NoError(t, err)
	require.Equal(t, uint64(16), to[inferencetypes.ModuleName])
	entries, err := testApp.InferenceKeeper.WarmKeyLogEntries(ctx, granter)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, warm.String(), entries[0].Grantee)
	require.Equal(t, int64(500), entries[0].GrantedAtHeight)
	require.Equal(t, expiration.UnixNano(), entries[0].ExpiresAtUnix)
	require.Equal(t, uint64(1), entries[0].Id)
	liveID, err := testApp.InferenceKeeper.WarmKeyLive.Get(ctx, collections.Join(granter, warm))
	require.NoError(t, err)
	require.Equal(t, uint64(1), liveID)
	_, err = handler(ctx, upgradetypes.Plan{Name: v0_2_18.UpgradeName, Height: 500}, to)
	require.NoError(t, err)
	after, err := testApp.InferenceKeeper.WarmKeyLogEntries(ctx, granter)
	require.NoError(t, err)
	require.Equal(t, entries, after)
}
