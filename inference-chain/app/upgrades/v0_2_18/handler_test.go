package v0_2_18

import (
	"testing"

	"cosmossdk.io/log/v2"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	store "github.com/cosmos/cosmos-sdk/store/v2"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	upgradetypes "github.com/cosmos/cosmos-sdk/x/upgrade/types"
	channeltypes "github.com/cosmos/ibc-go/v11/modules/core/04-channel/types"
	"github.com/stretchr/testify/require"
)

// productionVersionMap is the x/upgrade version map from the Gonka mainnet
// archive at height 7110407 on 2026-11-17. That binary is pre-v0.2.18.
// capability is present at version 1; this binary no longer registers it.
func productionVersionMap() module.VersionMap {
	return module.VersionMap{
		"06-solomachine":     0,
		"07-tendermint":      0,
		"auth":               5,
		"authz":              2,
		"bank":               4,
		"bls":                1,
		"bookkeeper":         1,
		"capability":         1,
		"circuit":            1,
		"collateral":         1,
		"consensus":          1,
		"crisis":             2,
		"distribution":       4,
		"evidence":           1,
		"feegrant":           2,
		"feeibc":             2,
		"genesistransfer":    1,
		"genutil":            1,
		"gov":                5,
		"group":              2,
		"ibc":                6,
		"inference":          15,
		"interchainaccounts": 3,
		"mint":               2,
		"nft":                1,
		"params":             1,
		"restrictions":       1,
		"runtime":            0,
		"slashing":           5,
		"staking":            6,
		"streamvesting":      1,
		"transfer":           5,
		"upgrade":            2,
		"vesting":            1,
		"wasm":               4,
	}
}

func TestHandlerOnProductionVersionMap(t *testing.T) {
	db := dbm.NewMemDB()
	ms := store.NewCommitMultiStore(db, log.NewNopLogger())
	ctx := sdk.NewContext(ms, cmtproto.Header{Height: 7110407}, false, log.NewNopLogger())
	cdc := codec.NewProtoCodec(codectypes.NewInterfaceRegistry())
	cfg := module.NewConfigurator(cdc, nil, nil)
	// The new binary has no capability module. An empty manager still runs
	// RunMigrations, which only visits registered modules.
	manager := module.NewManager()
	require.NotContains(t, manager.Modules, "capability")

	for _, name := range []string{"with capability entry", "without capability entry"} {
		t.Run(name, func(t *testing.T) {
			from := productionVersionMap()
			if name == "without capability entry" {
				delete(from, "capability")
			}
			channels := &fakeChannelKeeper{channels: []channeltypes.IdentifiedChannel{
				channelEnd("transfer", "channel-0", "ics20-1"),
				channelEnd("icahost", "channel-7", `{"fee_version":"ics29-1","app_version":"ics27-1"}`),
			}}
			handler := CreateUpgradeHandler(manager, cfg, discardLogger{}, channels, &fakeBank{}, &fakeDistr{})

			to, err := handler(ctx, upgradetypes.Plan{Name: UpgradeName}, from)
			require.NoError(t, err)
			require.NotContains(t, to, "capability")
			require.NotContains(t, to, "feeibc")
			_, plainKept := channels.saved["transfer/channel-0"]
			require.False(t, plainKept)
			require.Equal(t, "ics27-1", channels.saved["icahost/channel-7"].Version)
		})
	}
}
