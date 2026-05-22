//go:build sims || simsbench

package app_test

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"cosmossdk.io/log"
	sdkmath "cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"cosmossdk.io/x/feegrant"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	authzkeeper "github.com/cosmos/cosmos-sdk/x/authz/keeper"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	simcli "github.com/cosmos/cosmos-sdk/x/simulation/client/cli"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// Compile-time check that *app.App satisfies simsx.SimulationApp.
var _ simsx.SimulationApp = (*app.App)(nil)

// defaultSimSeeds is the canonical seed triplet shared by
// TestAppStateDeterminism (full sweep) and BenchmarkFullAppSimulation
// (uses [0] as the single-seed default).
var defaultSimSeeds = []int64{1, 32, 123}

func init() {
	simcli.GetSimulatorFlags()
	app.InitSDKConfig()
}

// NewSimApp adapts app.New to the simsx.Run appFactory signature by hiding the
// wasmd opts argument required by gonka's constructor.
func NewSimApp(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	loadLatest bool,
	appOpts servertypes.AppOptions,
	baseAppOptions ...func(*baseapp.BaseApp),
) *app.App {
	bApp, err := app.New(logger, db, traceStore, loadLatest, appOpts, []wasmkeeper.Option{}, baseAppOptions...)
	if err != nil {
		panic(err)
	}
	return bApp
}

// setupStateFactory builds the SimStateFactory consumed by simsx.Run for each
// seed. AccountKeeper satisfies simsx.AccountSourceX (AccountSource +
// ModuleAccountSource); BankKeeper satisfies simsx.BalanceSource.
//
// AppStateFnWithExtendedCb (rather than plain AppStateFn) lets fixBankGenesisState
// patch the randomized rawState before InitGenesis runs.
func setupStateFactory(bApp *app.App) simsx.SimStateFactory {
	return simsx.SimStateFactory{
		Codec: bApp.AppCodec(),
		AppStateFn: simtestutil.AppStateFnWithExtendedCb(
			bApp.AppCodec(),
			bApp.SimulationManager(),
			bApp.DefaultGenesis(),
			func(rawState map[string]json.RawMessage) {
				shrinkUpstreamStakingValidators(bApp, rawState)
				fixBankGenesisState(bApp, rawState)
			},
		),
		BlockedAddr:   app.BlockedAddresses(),
		AccountSource: bApp.AccountKeeper,
		BalanceSource: bApp.BankKeeper,
	}
}

// fixBankGenesisState patches the randomized sim genesis so InitGenesis succeeds:
//
//  1. Add Gonka denom metadata, required by inference module init
//     (app.initializeDenomMetadata reads BankKeeper.GetDenomMetaData(BaseCoin)).
//  2. Fund every sim account with ngonka: upstream
//     simsx hardwires simState.BondDenom = sdk.DefaultBondDenom = "stake"
//     (testutil/simsx/runner.go:286), so the randomized bank GenesisState
//     funds accounts with `stake` but not the `ngonka` BaseCoin that
//     StartInference's escrow flow requires (payment_handler.go).
//     Without this, every StartInference fails at PutPaymentInEscrow with
//     "insufficient spendable funds" and routes through failedStart — so
//     no Inferences ever land in keeper and the rest of the first-wave
//     ops never see paired state. We give each account simAccountNgonka
//     coins of ngonka (~10^15 ≈ 10^6 GNK), well above the per-inference
//     escrow even for the full-test 100K-op run.
//  3. Recompute bank Supply from Balances. The randomized genesis sets
//     Supply based on NumBonded staking validators, but with staking ops
//     disabled the bonded pool is never funded, so the default Supply
//     would fail the supply-vs-balance invariant at InitGenesis. Same
//     recompute keeps Supply in sync after the ngonka top-up.
//
// The denom-metadata + Supply-recompute steps are ported from hleb-albau
// PR gonka-ai/gonka#995; the ngonka top-up is added on top.
func fixBankGenesisState(bApp *app.App, rawState map[string]json.RawMessage) {
	bankStateBz, ok := rawState[banktypes.ModuleName]
	if !ok {
		panic("bank genesis state missing from randomized state")
	}
	var bankState banktypes.GenesisState
	bApp.AppCodec().MustUnmarshalJSON(bankStateBz, &bankState)

	bankState.DenomMetadata = append(bankState.DenomMetadata, banktypes.Metadata{
		Description: "Coins for the Gonka network.",
		Base:        inferencetypes.BaseCoin,
		Display:     inferencetypes.NativeCoin,
		Name:        "Gonka",
		Symbol:      "GNK",
		DenomUnits: []*banktypes.DenomUnit{
			{Denom: inferencetypes.BaseCoin, Exponent: 0, Aliases: []string{"nanogonka"}},
			{Denom: "ugonka", Exponent: 3, Aliases: []string{"microgonka"}},
			{Denom: "mgonka", Exponent: 6, Aliases: []string{"milligonka"}},
			{Denom: inferencetypes.NativeCoin, Exponent: 9},
		},
	})

	// Build the set of module-account addresses so we DON'T top them up
	// with ngonka — staking InitGenesis verifies notBondedPool balance ==
	// declared NotBondedCoins (stake only); adding ngonka there panics
	// «not bonded pool balance is different from not bonded coins»
	// (cosmos-sdk x/staking/keeper/genesis.go:174).
	moduleAddrs := make(map[string]bool, 16)
	for name := range app.GetMaccPerms() {
		moduleAddrs[authtypes.NewModuleAddress(name).String()] = true
	}

	ngonkaTopUp := sdk.NewCoin(inferencetypes.BaseCoin, simAccountNgonka)
	for i := range bankState.Balances {
		if moduleAddrs[bankState.Balances[i].Address] {
			continue
		}
		bankState.Balances[i].Coins = bankState.Balances[i].Coins.Add(ngonkaTopUp)
	}

	var actualSupply sdk.Coins
	for _, balance := range bankState.Balances {
		actualSupply = actualSupply.Add(balance.Coins...)
	}
	bankState.Supply = actualSupply

	rawState[banktypes.ModuleName] = bApp.AppCodec().MustMarshalJSON(&bankState)
}

// simAccountNgonka is the per-sim-account ngonka top-up amount applied by
// fixBankGenesisState. 10^15 ngonka = 10^6 GNK — comfortable buffer
// above the per-inference escrow (~6.3 × 10^5 ngonka in current sim) even
// at the make sim-full-test scale of 500 blocks × 200 ops/block.
var simAccountNgonka = sdkmath.NewInt(1_000_000_000_000_000)

// shrinkUpstreamStakingValidators reduces every upstream-generated staking
// validator's Tokens and matching DelegatorShares/Delegation.Shares to 1
// (in BondDenom), so that EpochGroup.ValidationWeights[].Weight (sourced
// from Tokens via epochgroup.NewEpochMemberFromStakingValidator,
// epoch_group.go:103) does not dominate over sim genesis participants
// (each at simValidatorPower=1_000_000 from x/inference/simulation/
// bootstrap.go).
//
// Background: cosmos-sdk's RandomizedGenState (x/staking/simulation/
// genesis.go) generates 1-299 random Unbonded validators with Tokens=
// InitialStake ≈ 10^12 each. x/inference's InitGenesisEpoch
// (module/genesis.go:186) iterates ALL staking validators via
// GetAllValidators and adds them to EpochGroup.ValidationWeights with
// Weight=Tokens. Without this shrink:
//   - totalNetworkWeight ≈ N × 10^12 (often ~10^13–10^14)
//   - validWeight max = NumSimGenesisParticipants × simValidatorPower
//     = 5 × 10^6
//   - Calculate's 2/3-of-total threshold (chainvalidation.go:327) is
//     unreachable → ComputeNewWeights yields nil active set → no
//     SetComputeValidators → cometbft set drains → SKIP "empty
//     validator set" (cosmos-sdk x/simulation/simulate.go:266).
//
// All validators are retained (so staking.InitGenesis still bonds ≥1 and
// InitChain doesn't panic on empty validator set per
// cosmos-sdk types/module/module.go:524) but their Tokens shrink to 1.
// NotBondedPool balance is adjusted to mirror the new sum, preserving
// the upstream invariant (state_helpers.go:132-139) that
// NotBondedPool == sum(Status==Unbonded).Tokens.
func shrinkUpstreamStakingValidators(bApp *app.App, rawState map[string]json.RawMessage) {
	stakingBz, ok := rawState[stakingtypes.ModuleName]
	if !ok {
		panic("staking genesis state missing from randomized state")
	}
	var stakingState stakingtypes.GenesisState
	bApp.AppCodec().MustUnmarshalJSON(stakingBz, &stakingState)

	originalNotBonded := sdkmath.ZeroInt()
	for _, v := range stakingState.Validators {
		if v.Status == stakingtypes.Unbonded {
			originalNotBonded = originalNotBonded.Add(v.Tokens)
		}
	}

	oneToken := sdkmath.OneInt()
	oneShares := sdkmath.LegacyOneDec()
	for i := range stakingState.Validators {
		operAddr := stakingState.Validators[i].OperatorAddress
		valAddr, err := sdk.ValAddressFromBech32(operAddr)
		if err != nil {
			panic(err)
		}
		accAddr := sdk.AccAddress(valAddr).String()
		// Derivation mirrors x/inference/simulation.SimValidatorKey
		// (genesis.go:17) so sim genesis EpochMember.Pubkey ==
		// staking.Validator.ConsensusPubkey for the same account. Without
		// this alignment, the GON-191 stale-consensus-key filter in our
		// cosmos-sdk fork (x/staking/keeper/compute.go:358) removes sim
		// genesis validators from the bonded set on the first
		// SetComputeValidators call from EnsureSimActiveParticipantsSeeded.
		pubKey := ed25519.GenPrivKeyFromSecret([]byte("gonka-sim-validator:" + accAddr)).PubKey()
		pkAny, err := codectypes.NewAnyWithValue(pubKey)
		if err != nil {
			panic(err)
		}
		stakingState.Validators[i].ConsensusPubkey = pkAny
		stakingState.Validators[i].Tokens = oneToken
		stakingState.Validators[i].DelegatorShares = oneShares
	}
	for i := range stakingState.Delegations {
		stakingState.Delegations[i].Shares = oneShares
	}
	for i := range stakingState.LastValidatorPowers {
		stakingState.LastValidatorPowers[i].Power = 1
	}
	rawState[stakingtypes.ModuleName] = bApp.AppCodec().MustMarshalJSON(&stakingState)

	numUnbonded := sdkmath.ZeroInt()
	for _, v := range stakingState.Validators {
		if v.Status == stakingtypes.Unbonded {
			numUnbonded = numUnbonded.AddRaw(1)
		}
	}
	delta := originalNotBonded.Sub(numUnbonded)
	if delta.IsZero() || delta.IsNegative() {
		return
	}

	bankBz, ok := rawState[banktypes.ModuleName]
	if !ok {
		panic("bank genesis state missing from randomized state")
	}
	var bankState banktypes.GenesisState
	bApp.AppCodec().MustUnmarshalJSON(bankBz, &bankState)

	notBondedAddr := authtypes.NewModuleAddress(stakingtypes.NotBondedPoolName).String()
	bondDenom := stakingState.Params.BondDenom
	for i := range bankState.Balances {
		if bankState.Balances[i].Address == notBondedAddr {
			bankState.Balances[i].Coins = bankState.Balances[i].Coins.Sub(
				sdk.NewCoin(bondDenom, delta))
			break
		}
	}
	rawState[banktypes.ModuleName] = bApp.AppCodec().MustMarshalJSON(&bankState)
}

// TestFullAppSimulation is the simsx entry point for `make sim-smoke-test`
// and `make sim-full-test`. CLI flags (-NumBlocks, -BlockSize, -Seed,
// -Enabled) come from simcli.GetSimulatorFlags() in init().
//
// Requires -Enabled=true. simsx does not gate on it the way legacy
// simulation.SimulateFromSeed did; this test restores that gate so a
// raw `go test -tags sims ./app/...` skips fast.
//
// With user-supplied -Seed=N (Make targets), dispatches to
// simsx.RunWithSeed for a single-seed run. Without it, falls through to
// simsx.Run which fans out the framework's defaultSeeds list (37 seeds
// in this fork) as parallel subtests.
func TestFullAppSimulation(t *testing.T) {
	if !simcli.FlagEnabledValue {
		t.Skip("pass -Enabled=true to run this; e.g. via make sim-smoke-test")
	}
	cfg := simcli.NewConfigFromFlags()
	cfg.ChainID = simsx.SimAppChainID

	if cfg.Seed != simcli.DefaultSeedValue {
		simsx.RunWithSeed(t, cfg, NewSimApp, setupStateFactory, cfg.Seed, nil)
		return
	}
	simsx.Run(t, NewSimApp, setupStateFactory)
}

// TestAppImportExport_Postrun is t.Skip'd because InitGenesis on the
// re-imported state panics at gonka-ai/cosmos-sdk@v0.53.3-ps17
// x/staking/keeper/genesis.go:157-158 ("bonded pool balance is different
// from bonded coins").
//
// The fork's PoC architecture (gonka/docs/cosmos_changes.md) disables
// token bonding: SetComputeValidators creates bonded validators without
// bank transfers, pool.go iterates manually, and delegation.go +
// val_state_change.go skip the transfers. genesis.go:157 was not updated
// to mirror that skip, so its upstream invariant bondedBalance.Equal(
// bondedCoins) no longer holds against live state.
//
// Production sidesteps this by using x/upgrade in-place handlers (see
// app/upgrades/v0_2_12) instead of `inferenced export -> init`. Funding
// the bonded pool here to make the test green would mask the
// inconsistency, so it stays skipped.
//
// Re-enable once gonka-ai/cosmos-sdk genesis.go applies the same
// PoC-validator skip already in delegation.go. Fork fix proposal:
// gonka-ai/gonka#1153. Broader Phase 1 work: gonka-ai/gonka#982.
func TestAppImportExport_Postrun(t *testing.T) {
	t.Skip("blocked on gonka-ai/cosmos-sdk genesis.go:157; see gonka-ai/gonka#1153 for the fork fix proposal")
	simsx.Run(t, NewSimApp, setupStateFactory, checkImportExport)
}

func checkImportExport(tb testing.TB, ti simsx.TestInstance[*app.App], _ []simtypes.Account) {
	tb.Helper()
	bApp := ti.App

	tb.Logf("exporting genesis...")
	exported, err := bApp.ExportAppStateAndValidators(false, []string{}, []string{})
	require.NoError(tb, err)

	tb.Logf("importing genesis into fresh app...")
	newTI := simsx.NewSimulationAppInstance(tb, ti.Cfg, NewSimApp)
	newApp := newTI.App
	defer func() { _ = newApp.Close() }()

	var genesisState app.GenesisState
	require.NoError(tb, json.Unmarshal(exported.AppState, &genesisState))

	header := cmtproto.Header{Height: bApp.LastBlockHeight()}
	ctxA := bApp.NewContextLegacy(true, header)
	ctxB := newApp.NewContextLegacy(true, header)
	if _, err := newApp.ModuleManager.InitGenesis(ctxB, newApp.AppCodec(), genesisState); err != nil {
		// Defensive: even with disabledOpsSimModule on staking we keep the
		// upstream-known skip path for the rare case of an empty validator set.
		if strings.Contains(err.Error(), "validator set is empty after InitGenesis") {
			tb.Skip("import-export comparison skipped: validator set empty")
		}
		tb.Fatalf("InitGenesis on newApp: %v", err)
	}
	require.NoError(tb, newApp.StoreConsensusParams(ctxB, exported.ConsensusParams))

	tb.Logf("comparing stores...")
	skipPrefixes := importExportSkipPrefixes()
	storeKeys := bApp.GetStoreKeys()
	require.NotEmpty(tb, storeKeys)
	for _, appKeyA := range storeKeys {
		if _, ok := appKeyA.(*storetypes.KVStoreKey); !ok {
			continue
		}
		keyName := appKeyA.Name()
		appKeyB := newApp.GetKey(keyName)
		storeA := ctxA.KVStore(appKeyA)
		storeB := ctxB.KVStore(appKeyB)
		failedKVAs, failedKVBs := simtestutil.DiffKVStores(storeA, storeB, skipPrefixes[keyName])
		require.Equalf(tb, len(failedKVAs), len(failedKVBs), "unequal failure sets for %s", keyName)
		if len(failedKVAs) != 0 {
			tb.Fatalf("KV diff for %s:\n%s", keyName,
				simtestutil.GetSimulationLog(keyName, bApp.SimulationManager().StoreDecoders, failedKVAs, failedKVBs))
		}
	}
}

// importExportSkipPrefixes mirrors upstream cosmos-sdk simapp's skip set:
// transient queues populated by runtime hooks, not by InitGenesis.
func importExportSkipPrefixes() map[string][][]byte {
	return map[string][][]byte{
		stakingtypes.StoreKey: {
			stakingtypes.UnbondingQueueKey, stakingtypes.RedelegationQueueKey, stakingtypes.ValidatorQueueKey,
			stakingtypes.HistoricalInfoKey, stakingtypes.UnbondingIDKey, stakingtypes.UnbondingIndexKey,
			stakingtypes.UnbondingTypeKey, stakingtypes.ValidatorUpdatesKey,
		},
		authzkeeper.StoreKey:   {authzkeeper.GrantQueuePrefix},
		feegrant.StoreKey:      {feegrant.FeeAllowanceQueueKeyPrefix},
		slashingtypes.StoreKey: {slashingtypes.ValidatorMissedBlockBitmapKeyPrefix},
	}
}

// TestAppSimulationAfterImport_Postrun is t.Skip'd. Two things must be
// done before re-enabling, in order:
//
//  1. gonka-ai/cosmos-sdk genesis.go:157 bonded-pool fix per
//     gonka-ai/gonka#1153 (InitChain on the imported state currently
//     panics on the same fork asymmetry as TestAppImportExport_Postrun).
//  2. Wire SimulateFromSeedX into checkSimulationAfterImport. The current
//     body only verifies InitChain succeeds; the test name promises a
//     second simulation step that is not yet implemented (TODO in the
//     body).
//
// Removing t.Skip with only (1) cleared would make the test pass while
// never exercising the second simulation step. Both must land first.
func TestAppSimulationAfterImport_Postrun(t *testing.T) {
	t.Skip("blocked on (1) gonka-ai/cosmos-sdk genesis.go:157 fix per gonka-ai/gonka#1153, and (2) wiring SimulateFromSeedX for the after-import simulation step")
	simsx.Run(t, NewSimApp, setupStateFactory, checkSimulationAfterImport)
}

func checkSimulationAfterImport(tb testing.TB, ti simsx.TestInstance[*app.App], _ []simtypes.Account) {
	tb.Helper()
	bApp := ti.App

	tb.Logf("exporting genesis (forZeroHeight=true)...")
	exported, err := bApp.ExportAppStateAndValidators(true, []string{}, []string{})
	require.NoError(tb, err)

	tb.Logf("importing genesis into fresh app via InitChain...")
	newTI := simsx.NewSimulationAppInstance(tb, ti.Cfg, NewSimApp)
	newApp := newTI.App
	defer func() { _ = newApp.Close() }()

	_, err = newApp.InitChain(&abci.RequestInitChain{
		AppStateBytes: exported.AppState,
		ChainId:       ti.Cfg.ChainID,
	})
	require.NoError(tb, err)

	// TODO: once import unblocks, run SimulateFromSeedX on newApp. Needs
	// either a copy of simsx's unexported prepareWeightedOps or a minimal
	// ops registry.
	tb.Log("import succeeded; second-simulation step deferred (see TODO)")
}

// TestAppStateDeterminism asserts that running the same seed N times
// produces an identical AppHash. Uses simsx.RunWithSeed directly because
// simsx.Run fans out distinct seeds in parallel; determinism wants the
// same seed sequentially. PoC-state determinism beyond AppHash
// (EffectiveIndex, epoch-state collections) is Phase 3 territory per the
// issue body.
func TestAppStateDeterminism(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}
	if !simcli.FlagEnabledValue {
		t.Skip("pass -Enabled=true to run this; simsx does not gate on it")
	}
	cfg := simcli.NewConfigFromFlags()
	cfg.ChainID = simsx.SimAppChainID

	// Block counts inherit from simcli flags.
	// Honor user-supplied -Seed=N for single-seed reproduction of a failure.
	seeds := defaultSimSeeds
	if cfg.Seed != simcli.DefaultSeedValue {
		seeds = []int64{cfg.Seed}
	}
	const attempts = 3

	for _, seed := range seeds {
		// Pre-allocated indexed slots so a failure points at the specific
		// attempt rather than a generic count mismatch. RunWithSeed invokes
		// capture synchronously, so hashes[attempt] is populated before the
		// next iteration.
		hashes := make([][]byte, attempts)
		for attempt := range attempts {
			capture := func(tb testing.TB, ti simsx.TestInstance[*app.App], _ []simtypes.Account) {
				hashes[attempt] = ti.App.LastCommitID().Hash
			}
			simsx.RunWithSeed(t, cfg, NewSimApp, setupStateFactory, seed, nil, capture)
			require.NotEmptyf(t, hashes[attempt],
				"capture callback was not invoked for seed %d attempt %d", seed, attempt)
		}
		for i := 1; i < attempts; i++ {
			require.Equalf(t, hashes[0], hashes[i],
				"non-determinism in seed %d: attempt 0 vs %d", seed, i)
		}
	}
}
