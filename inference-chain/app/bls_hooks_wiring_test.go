package app_test

import (
	"testing"

	"cosmossdk.io/log"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/server"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencemodule "github.com/productscience/inference/x/inference/module"
)

// The BLS failure and completion callbacks only run if depinject installs the
// inference hooks on the BLS keeper. Build the app the way inferenced does and
// check the keeper the app holds, not a hand-wired one.
func TestBlsHooksInstalledByAppWiring(t *testing.T) {
	inferencemodule.IgnoreDuplicateDenomRegistration = true
	config := sdk.GetConfig()
	config.SetBech32PrefixForAccount("gonka", "gonkapub")
	config.SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")
	config.SetBech32PrefixForConsensusNode("gonkavalcons", "gonkavalconspub")

	appOptions := make(simtestutil.AppOptionsMap, 0)
	appOptions[flags.FlagHome] = t.TempDir()
	appOptions[server.FlagInvCheckPeriod] = uint(0)

	var emptyWasmOpts []wasmkeeper.Option
	testApp, err := app.New(
		log.NewNopLogger(), dbm.NewMemDB(), nil, true, appOptions, emptyWasmOpts,
		baseapp.SetChainID("bls-hooks-wiring-test"),
	)
	require.NoError(t, err)

	hooks, ok := testApp.BlsKeeper.Hooks().(blstypes.MultiBlsHooks)
	require.True(t, ok, "unexpected hooks type %T", testApp.BlsKeeper.Hooks())
	require.Len(t, hooks, 1, "the inference module's BLS hooks are not installed")

	wrapper, ok := hooks[0].(blstypes.BlsHooksWrapper)
	require.True(t, ok, "unexpected hook type %T", hooks[0])
	require.IsType(t, inferencemodule.BlsHooks{}, wrapper.BlsHooks)
}
