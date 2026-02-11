package app_test

import (
	"encoding/json"
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/server"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/cosmos/cosmos-sdk/x/gov/types/v1beta1"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

const TallyTestChainID = "tally-test-chain"

// TestTallyBugReproduction reproduces the governance tally bug where:
//   - SetComputeValidators bonds MORE than maxValidators (100)
//   - Numerator (votes counted): only from top 100 via IterateBondedValidatorsByPower
//   - Denominator (total bonded): ALL bonded validators via TotalBondedTokens
//
// This test verifies that the fix (using IterateValidators instead of
// IterateBondedValidatorsByPower) correctly counts votes from ALL bonded validators.
//
// See tally.md for detailed bug analysis.
func TestTallyBugReproduction(t *testing.T) {
	// Setup app
	testApp := createTestApp(t)
	ctx := testApp.BaseApp.NewUncachedContext(false, cmtproto.Header{ChainID: TallyTestChainID, Height: 1000000})

	// Configuration matching mainnet behavior:
	// - maxValidators = 100 (standard cosmos-sdk setting)
	// - SetComputeValidators will bond 110 validators (bypassing the limit)
	const totalValidators = 110
	const powerPerValidator = int64(1000)

	// Create 110 validators via SetComputeValidators
	// This bypasses the maxValidators limit and sets ALL as bonded
	computeResults := make([]stakingkeeper.ComputeResult, totalValidators)
	valAddresses := make([]sdk.ValAddress, totalValidators)
	accAddresses := make([]sdk.AccAddress, totalValidators)

	for i := 0; i < totalValidators; i++ {
		privKey := ed25519.GenPrivKey()
		pubKey := privKey.PubKey()

		operatorPrivKey := secp256k1.GenPrivKey()
		operatorAddr := sdk.AccAddress(operatorPrivKey.PubKey().Address())
		valAddr := sdk.ValAddress(operatorAddr)

		valAddresses[i] = valAddr
		accAddresses[i] = operatorAddr

		computeResults[i] = stakingkeeper.ComputeResult{
			Power:           powerPerValidator,
			ValidatorPubKey: pubKey,
			OperatorAddress: valAddr.String(),
		}
	}

	// SetComputeValidators bonds ALL 110 validators (isTestnet=true to bypass height check)
	validators, err := testApp.StakingKeeper.SetComputeValidators(ctx, computeResults, true)
	require.NoError(t, err)
	require.Len(t, validators, totalValidators+1, "Should have 110 new validators + 1 genesis validator")

	// ============================================
	// VERIFY THE BUG SETUP: Mismatched iterator behavior
	// ============================================

	// 1. Count validators via IterateValidators (should return ALL 111 bonded)
	allBondedCount := 0
	err = testApp.StakingKeeper.IterateValidators(ctx, func(index int64, validator stakingtypes.ValidatorI) (stop bool) {
		if validator.IsBonded() {
			allBondedCount++
		}
		return false
	})
	require.NoError(t, err)
	t.Logf("IterateValidators (bonded): %d validators", allBondedCount)
	require.Equal(t, totalValidators+1, allBondedCount, "All 111 validators should be bonded")

	// 2. Count validators via IterateBondedValidatorsByPower (LIMITED to maxValidators=100)
	top100Count := 0
	err = testApp.StakingKeeper.IterateBondedValidatorsByPower(ctx, func(index int64, validator stakingtypes.ValidatorI) (stop bool) {
		top100Count++
		return false
	})
	require.NoError(t, err)
	t.Logf("IterateBondedValidatorsByPower: %d validators (limited by maxValidators)", top100Count)
	require.Equal(t, 100, top100Count, "IterateBondedValidatorsByPower should be limited to 100")

	// 3. Verify validator #101 (index 100) is bonded but outside IterateBondedValidatorsByPower
	validator101, err := testApp.StakingKeeper.GetValidator(ctx, valAddresses[100])
	require.NoError(t, err)
	require.True(t, validator101.IsBonded(), "Validator #101 should be bonded (via SetComputeValidators)")
	t.Logf("Validator #101: bonded=%v, tokens=%s", validator101.IsBonded(), validator101.GetBondedTokens())

	// 4. Verify TotalBondedTokens includes ALL validators (the denominator)
	totalBonded, err := testApp.StakingKeeper.TotalBondedTokens(ctx)
	require.NoError(t, err)
	t.Logf("TotalBondedTokens (denominator): %s", totalBonded)

	// ============================================
	// TEST THE FIX: Votes from validator #101 should be counted
	// ============================================

	// Setup: Fund voter account and prepare governance
	voter101Addr := accAddresses[100] // Validator #101's operator address
	govModuleAddr := authtypes.NewModuleAddress(govtypes.ModuleName)

	// Create and fund voter account
	if testApp.AccountKeeper.GetAccount(ctx, voter101Addr) == nil {
		acc := testApp.AccountKeeper.NewAccountWithAddress(ctx, voter101Addr)
		testApp.AccountKeeper.SetAccount(ctx, acc)
	}
	fundCoins := sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, math.NewInt(100000000000)))
	err = testApp.BankKeeper.MintCoins(ctx, inferencetypes.TopRewardPoolAccName, fundCoins)
	require.NoError(t, err)
	err = testApp.BankKeeper.SendCoinsFromModuleToAccount(ctx, inferencetypes.TopRewardPoolAccName, voter101Addr, fundCoins)
	require.NoError(t, err)

	// Create a proposal
	proposalContent := v1beta1.NewTextProposal("Test Tally Fix", "Testing that votes from validators 101+ are counted")
	legacyProposalMsg, err := v1.NewLegacyContent(proposalContent, govModuleAddr.String())
	require.NoError(t, err)

	proposal, err := testApp.GovKeeper.SubmitProposal(ctx, []sdk.Msg{legacyProposalMsg}, "", "test", "Test tally fix", voter101Addr, false)
	require.NoError(t, err)
	t.Logf("Created proposal ID: %d", proposal.Id)

	// Move to voting period
	proposal.Status = v1.StatusVotingPeriod
	err = testApp.GovKeeper.SetProposal(ctx, proposal)
	require.NoError(t, err)

	// Validator #101 votes YES (via their operator address which has self-delegation)
	err = testApp.GovKeeper.AddVote(ctx, proposal.Id, voter101Addr, v1.NewNonSplitVoteOption(v1.OptionYes), "")
	require.NoError(t, err)
	t.Logf("Validator #101 (%s) voted YES", voter101Addr)

	// ============================================
	// TALLY: This is where the bug would manifest
	// ============================================
	proposal, err = testApp.GovKeeper.Proposals.Get(ctx, proposal.Id)
	require.NoError(t, err)

	_, _, tallyResults, err := testApp.GovKeeper.Tally(ctx, proposal)
	require.NoError(t, err)

	t.Logf("=== TALLY RESULTS ===")
	t.Logf("  Yes votes (numerator): %s", tallyResults.YesCount)
	t.Logf("  Total bonded (denominator): %s", totalBonded)

	// ============================================
	// CRITICAL ASSERTION: Vote from validator #101 must be counted
	// ============================================
	actualYesVotes, ok := math.NewIntFromString(tallyResults.YesCount)
	require.True(t, ok, "Failed to parse YesCount")

	require.True(t, actualYesVotes.GT(math.ZeroInt()),
		"TALLY BUG: Vote from validator #101 (outside top 100) was NOT counted!\n"+
			"  YesCount=%s (expected > 0)\n"+
			"  This means the fix is not working - getCurrentValidators is still using\n"+
			"  IterateBondedValidatorsByPower (limited to 100) instead of IterateValidators.",
		tallyResults.YesCount)

	t.Logf("SUCCESS: Vote from validator #101 was counted! YesCount=%s", tallyResults.YesCount)
}

// createTestApp creates an app with one genesis validator (required for chain init)
// and maxValidators=100 (standard setting).
func createTestApp(t *testing.T) *app.App {
	t.Helper()

	// Configure SDK for gonka addresses
	config := sdk.GetConfig()
	config.SetBech32PrefixForAccount("gonka", "gonkapub")
	config.SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")
	config.SetBech32PrefixForConsensusNode("gonkavalcons", "gonkavalconspub")

	db := dbm.NewMemDB()
	logger := log.NewNopLogger()

	appOptions := make(simtestutil.AppOptionsMap, 0)
	appOptions[flags.FlagHome] = t.TempDir()
	appOptions[server.FlagInvCheckPeriod] = uint(0)

	var emptyWasmOpts []wasmkeeper.Option

	testApp, err := app.New(
		logger, db, nil, true, appOptions, emptyWasmOpts,
		baseapp.SetChainID(TallyTestChainID),
	)
	require.NoError(t, err)

	// Initialize genesis with one validator (required by cosmos-sdk)
	genesisState := testApp.DefaultGenesis()

	// Create genesis validator
	genesisPrivKey := ed25519.GenPrivKey()
	genesisPubKey := genesisPrivKey.PubKey()
	genesisOperatorPrivKey := secp256k1.GenPrivKey()
	genesisOperatorAddr := sdk.AccAddress(genesisOperatorPrivKey.PubKey().Address())
	genesisValAddr := sdk.ValAddress(genesisOperatorAddr)
	genesisValidatorTokens := math.NewInt(10000000000)

	pkAny, err := codectypes.NewAnyWithValue(genesisPubKey)
	require.NoError(t, err)

	genesisValidator := stakingtypes.Validator{
		OperatorAddress:   genesisValAddr.String(),
		ConsensusPubkey:   pkAny,
		Jailed:            false,
		Status:            stakingtypes.Bonded,
		Tokens:            genesisValidatorTokens,
		DelegatorShares:   math.LegacyNewDecFromInt(genesisValidatorTokens),
		Description:       stakingtypes.Description{Moniker: "genesis-validator"},
		UnbondingHeight:   0,
		UnbondingTime:     time.Time{},
		Commission:        stakingtypes.NewCommission(math.LegacyZeroDec(), math.LegacyOneDec(), math.LegacyZeroDec()),
		MinSelfDelegation: math.OneInt(),
	}

	genesisDelegation := stakingtypes.Delegation{
		DelegatorAddress: genesisOperatorAddr.String(),
		ValidatorAddress: genesisValAddr.String(),
		Shares:           math.LegacyNewDecFromInt(genesisValidatorTokens),
	}

	// Bank genesis: fund bonded pool with genesis validator tokens
	bondedPoolAddr := authtypes.NewModuleAddress(stakingtypes.BondedPoolName)
	bankGenesis := banktypes.DefaultGenesisState()
	bankGenesis.DenomMetadata = []banktypes.Metadata{{
		Base: inferencetypes.BaseCoin, Display: inferencetypes.NativeCoin,
		Name: "Gonka", Symbol: "GONKA",
		DenomUnits: []*banktypes.DenomUnit{
			{Denom: inferencetypes.BaseCoin, Exponent: 0},
			{Denom: inferencetypes.NativeCoin, Exponent: 9},
		},
	}}
	bankGenesis.Balances = []banktypes.Balance{{
		Address: bondedPoolAddr.String(),
		Coins:   sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, genesisValidatorTokens)),
	}}
	genesisState[banktypes.ModuleName] = testApp.AppCodec().MustMarshalJSON(bankGenesis)

	// Staking genesis: maxValidators=100, one genesis validator
	stakingGenesis := stakingtypes.DefaultGenesisState()
	stakingGenesis.Params.BondDenom = inferencetypes.BaseCoin
	stakingGenesis.Params.MaxValidators = 100 // Standard limit - the bug depends on this!
	stakingGenesis.Validators = []stakingtypes.Validator{genesisValidator}
	stakingGenesis.Delegations = []stakingtypes.Delegation{genesisDelegation}
	genesisState[stakingtypes.ModuleName] = testApp.AppCodec().MustMarshalJSON(stakingGenesis)

	// Gov genesis
	govGenesis := v1.DefaultGenesisState()
	govGenesis.Params.MinDeposit = sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, math.NewInt(10000000)))
	govGenesis.Params.Quorum = "0.334"
	genesisState[govtypes.ModuleName] = testApp.AppCodec().MustMarshalJSON(govGenesis)

	stateBytes, err := json.Marshal(genesisState)
	require.NoError(t, err)

	_, err = testApp.InitChain(&abci.RequestInitChain{
		ChainId:         TallyTestChainID,
		AppStateBytes:   stateBytes,
		ConsensusParams: simtestutil.DefaultConsensusParams,
	})
	require.NoError(t, err)

	_, err = testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 1})
	require.NoError(t, err)

	_, err = testApp.Commit()
	require.NoError(t, err)

	return testApp
}
