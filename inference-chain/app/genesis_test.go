package app_test

import (
	"slices"
	"testing"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	minttypes "github.com/cosmos/cosmos-sdk/x/mint/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
)

// TestApp_GenesisInit_ModuleAccountsAndPoCValidators verifies four
// properties after a fresh InitGenesis on an app where staking,
// distribution, and wasmd are wrapped in disabledOpsSimModule:
//
//	(a) InitGenesis runs without panic. Implicit: if it panicked,
//	    createTestApp would not return.
//	(b) The five blocked module accounts are populated (app_config.go
//	    blockedModuleAccountAddrs).
//	(c) New validators can be added via the gonka PoC compute path
//	    (StakingKeeper.SetComputeValidators), since vanilla Cosmos token
//	    delegation is disabled in this fork (docs/cosmos_changes.md).
//	(d) The fee collector is a usable ModuleAccount, not a bare
//	    BaseAccount.
//
// Reuses createTestApp from tally_integration_test.go (PR #496).
// Subtests share testApp and ctx from the outer scope; one of them
// mutates state via SetComputeValidators, so failures are isolated per
// subtest but state is not.
func TestApp_GenesisInit_ModuleAccountsAndPoCValidators(t *testing.T) {
	testApp := createTestApp(t)
	ctx := testApp.BaseApp.NewUncachedContext(false, cmtproto.Header{ChainID: TallyTestChainID, Height: 1})

	t.Run("blocked module accounts populated", func(t *testing.T) {
		blocked := []string{
			authtypes.FeeCollectorName,
			distrtypes.ModuleName,
			minttypes.ModuleName,
			stakingtypes.BondedPoolName,
			stakingtypes.NotBondedPoolName,
		}
		for _, name := range blocked {
			t.Run(name, func(t *testing.T) {
				addr := testApp.AccountKeeper.GetModuleAddress(name)
				require.NotNil(t, addr, "GetModuleAddress(%s) should not be nil", name)
				acct := testApp.AccountKeeper.GetAccount(ctx, addr)
				require.NotNil(t, acct, "module account %s should exist after InitGenesis", name)
			})
		}
	})

	t.Run("PoC compute validator can be added", func(t *testing.T) {
		pocPub := ed25519.GenPrivKey().PubKey()
		pocOpAddr := sdk.ValAddress(secp256k1.GenPrivKey().PubKey().Address()).String()
		pocResults := []stakingkeeper.ComputeResult{{
			Power:           100,
			ValidatorPubKey: pocPub,
			OperatorAddress: pocOpAddr,
		}}

		postPoC, err := testApp.StakingKeeper.SetComputeValidators(ctx, pocResults, true /* isTestnet */)
		require.NoError(t, err)
		require.True(t, slices.ContainsFunc(postPoC, func(v stakingtypes.Validator) bool {
			return v.OperatorAddress == pocOpAddr && v.IsBonded()
		}), "PoC compute validator must be present and bonded after SetComputeValidators")
	})

	t.Run("fee collector is ModuleAccount", func(t *testing.T) {
		feeAddr := testApp.AccountKeeper.GetModuleAddress(authtypes.FeeCollectorName)
		feeAcct := testApp.AccountKeeper.GetAccount(ctx, feeAddr)
		_, isModAcct := feeAcct.(sdk.ModuleAccountI)
		require.True(t, isModAcct, "fee collector should implement ModuleAccountI")
	})
}
