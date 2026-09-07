package app_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
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
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	chainapp "github.com/productscience/inference/app"
	inferencemodule "github.com/productscience/inference/x/inference/module"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

const (
	wasmQueryTestChainID = "wasm-grpc-query-allowlist"
	queryClaimEpoch      = uint64(20)
	queryCurrentEpoch    = queryClaimEpoch + 1
	queryWorkCoins       = int64(200_000_000)
	queryRewardCoins     = int64(500_000_000)
)

var configureWasmQueryGlobalsOnce sync.Once

type wasmQueryHarness struct {
	app          *chainapp.App
	ctx          sdk.Context
	hostAddr     sdk.AccAddress
	contractAddr sdk.AccAddress
}

func configureWasmQueryTestGlobals() {
	configureWasmQueryGlobalsOnce.Do(func() {
		// App genesis registers the native denom in a process-global SDK registry.
		// The app package already uses the same Gonka prefixes in integration tests.
		// This test intentionally does not use t.Parallel because those SDK settings
		// are process-global.
		inferencemodule.IgnoreDuplicateDenomRegistration = true

		config := sdk.GetConfig()
		config.SetBech32PrefixForAccount("gonka", "gonkapub")
		config.SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")
		config.SetBech32PrefixForConsensusNode("gonkavalcons", "gonkavalconspub")
	})
}

func deterministicQueryKey(fill byte) *secp256k1.PrivKey {
	return &secp256k1.PrivKey{Key: bytes.Repeat([]byte{fill}, 32)}
}

func probeFixtureRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "resolve Wasm query test source path")
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "contracts", "p0-probe"))
}

func loadVerifiedQueryProbe(t *testing.T) []byte {
	t.Helper()
	fixtureRoot := probeFixtureRoot(t)
	manifestPath := filepath.Join(fixtureRoot, "artifacts", "checksums.txt")
	manifest, err := os.ReadFile(manifestPath)
	require.NoErrorf(t, err, "probe checksum manifest missing at %s; restore with `make -C %s build`", manifestPath, fixtureRoot)

	manifestChecksums := make(map[string]string)
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		fields := strings.Fields(line)
		require.Lenf(t, fields, 2, "invalid probe checksum line %d in %s", lineNumber+1, manifestPath)
		checksum, relativePath := fields[0], filepath.ToSlash(fields[1])
		decoded, decodeErr := hex.DecodeString(checksum)
		require.NoErrorf(t, decodeErr, "invalid SHA-256 encoding on line %d in %s", lineNumber+1, manifestPath)
		require.Lenf(t, decoded, sha256.Size, "checksum on line %d in %s is not SHA-256", lineNumber+1, manifestPath)
		_, duplicate := manifestChecksums[relativePath]
		require.Falsef(t, duplicate, "duplicate probe checksum path %q in %s", relativePath, manifestPath)
		manifestChecksums[relativePath] = checksum
	}

	expectedPaths := []string{
		"Cargo.lock",
		"Cargo.toml",
		"Makefile",
		"README.md",
		"build.sh",
		"artifacts/p0_probe.wasm",
	}
	err = filepath.WalkDir(filepath.Join(fixtureRoot, "src"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".rs" {
			return nil
		}
		relativePath, relErr := filepath.Rel(fixtureRoot, path)
		if relErr != nil {
			return relErr
		}
		expectedPaths = append(expectedPaths, filepath.ToSlash(relativePath))
		return nil
	})
	require.NoErrorf(t, err, "enumerate probe sources under %s", fixtureRoot)
	sort.Strings(expectedPaths)

	manifestPaths := make([]string, 0, len(manifestChecksums))
	for relativePath := range manifestChecksums {
		manifestPaths = append(manifestPaths, relativePath)
	}
	sort.Strings(manifestPaths)
	require.Equalf(t, expectedPaths, manifestPaths, "probe manifest must contain exactly the canonical fixture inputs; rebuild with `make -C %s build`", fixtureRoot)

	for _, relativePath := range expectedPaths {
		filePath := filepath.Join(fixtureRoot, filepath.FromSlash(relativePath))
		contents, readErr := os.ReadFile(filePath)
		require.NoErrorf(t, readErr, "probe fixture input missing at %s; rebuild with `make -C %s build`", filePath, fixtureRoot)
		actual := fmt.Sprintf("%x", sha256.Sum256(contents))
		require.Equalf(t, manifestChecksums[relativePath], actual, "probe fixture is stale or corrupt at %s; rebuild with `make -C %s build`", filePath, fixtureRoot)
	}

	wasmPath := filepath.Join(fixtureRoot, "artifacts", "p0_probe.wasm")
	wasmCode, err := os.ReadFile(wasmPath)
	require.NoErrorf(t, err, "Wasm probe missing at %s; restore with `make -C %s build`", wasmPath, fixtureRoot)
	require.GreaterOrEqual(t, len(wasmCode), 8, "Wasm probe at %s is truncated", wasmPath)
	require.Equal(t, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}, wasmCode[:8], "probe fixture must be a WebAssembly v1 module")
	return wasmCode
}

func setupWasmQueryHarness(t *testing.T) *wasmQueryHarness {
	t.Helper()
	configureWasmQueryTestGlobals()

	appOptions := make(simtestutil.AppOptionsMap)
	appOptions[flags.FlagHome] = t.TempDir()
	appOptions[server.FlagInvCheckPeriod] = uint(0)
	testApp, err := chainapp.New(
		log.NewNopLogger(),
		dbm.NewMemDB(),
		nil,
		true,
		appOptions,
		nil,
		baseapp.SetChainID(wasmQueryTestChainID),
	)
	require.NoError(t, err)

	genesisState := testApp.DefaultGenesis()
	genesisConsensusKey := ed25519.GenPrivKeyFromSecret([]byte("wasm-query-consensus-key")).PubKey()
	genesisOperatorAddr := sdk.AccAddress(deterministicQueryKey(1).PubKey().Address())
	genesisValAddr := sdk.ValAddress(genesisOperatorAddr)
	genesisValidatorTokens := math.NewInt(10_000_000_000)
	pkAny, err := codectypes.NewAnyWithValue(genesisConsensusKey)
	require.NoError(t, err)

	genesisValidator := stakingtypes.Validator{
		OperatorAddress:   genesisValAddr.String(),
		ConsensusPubkey:   pkAny,
		Status:            stakingtypes.Bonded,
		Tokens:            genesisValidatorTokens,
		DelegatorShares:   math.LegacyNewDecFromInt(genesisValidatorTokens),
		Description:       stakingtypes.Description{Moniker: "wasm-query-validator"},
		Commission:        stakingtypes.NewCommission(math.LegacyZeroDec(), math.LegacyOneDec(), math.LegacyZeroDec()),
		MinSelfDelegation: math.OneInt(),
	}
	genesisDelegation := stakingtypes.Delegation{
		DelegatorAddress: genesisOperatorAddr.String(),
		ValidatorAddress: genesisValAddr.String(),
		Shares:           math.LegacyNewDecFromInt(genesisValidatorTokens),
	}
	hostAddr := sdk.AccAddress(deterministicQueryKey(2).PubKey().Address())
	bondedPoolAddr := authtypes.NewModuleAddress(stakingtypes.BondedPoolName)

	bankGenesis := banktypes.DefaultGenesisState()
	bankGenesis.DenomMetadata = []banktypes.Metadata{{
		Base:    inferencetypes.BaseCoin,
		Display: inferencetypes.NativeCoin,
		Name:    "Gonka",
		Symbol:  "GNK",
		DenomUnits: []*banktypes.DenomUnit{
			{Denom: inferencetypes.BaseCoin, Exponent: 0},
			{Denom: inferencetypes.NativeCoin, Exponent: 9},
		},
	}}
	bankGenesis.Balances = []banktypes.Balance{
		{Address: bondedPoolAddr.String(), Coins: sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, genesisValidatorTokens))},
		{Address: hostAddr.String(), Coins: sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, math.NewInt(1_000_000_000)))},
	}
	genesisState[banktypes.ModuleName] = testApp.AppCodec().MustMarshalJSON(bankGenesis)

	stakingGenesis := stakingtypes.DefaultGenesisState()
	stakingGenesis.Params.BondDenom = inferencetypes.BaseCoin
	stakingGenesis.Validators = []stakingtypes.Validator{genesisValidator}
	stakingGenesis.Delegations = []stakingtypes.Delegation{genesisDelegation}
	genesisState[stakingtypes.ModuleName] = testApp.AppCodec().MustMarshalJSON(stakingGenesis)

	govGenesis := v1.DefaultGenesisState()
	govGenesis.Params.MinDeposit = sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, math.NewInt(10_000_000)))
	genesisState[govtypes.ModuleName] = testApp.AppCodec().MustMarshalJSON(govGenesis)

	wasmGenesis := wasmtypes.GenesisState{Params: wasmtypes.DefaultParams()}
	wasmGenesis.Params.CodeUploadAccess = wasmtypes.AllowEverybody
	wasmGenesis.Params.InstantiateDefaultPermission = wasmtypes.AccessTypeEverybody
	genesisState[wasmtypes.ModuleName] = testApp.AppCodec().MustMarshalJSON(&wasmGenesis)

	stateBytes, err := json.Marshal(genesisState)
	require.NoError(t, err)
	_, err = testApp.InitChain(&abci.RequestInitChain{
		ChainId:         wasmQueryTestChainID,
		AppStateBytes:   stateBytes,
		ConsensusParams: simtestutil.DefaultConsensusParams,
	})
	require.NoError(t, err)
	_, err = testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 1})
	require.NoError(t, err)
	_, err = testApp.Commit()
	require.NoError(t, err)

	ctx := testApp.BaseApp.NewUncachedContext(false, cmtproto.Header{
		ChainID: wasmQueryTestChainID,
		Height:  2,
		Time:    time.Unix(1_700_000_000, 0).UTC(),
	})
	permissionedWasm := wasmkeeper.NewDefaultPermissionKeeper(testApp.GetWasmKeeper())
	codeID, _, err := permissionedWasm.Create(ctx, hostAddr, loadVerifiedQueryProbe(t), nil)
	require.NoError(t, err)
	contractAddr, _, err := permissionedWasm.Instantiate(ctx, codeID, hostAddr, nil, []byte(`{}`), "wasm-grpc-query-probe", nil)
	require.NoError(t, err)
	require.NotNil(t, testApp.WasmKeeper.GetContractInfo(ctx, contractAddr))

	return &wasmQueryHarness{app: testApp, ctx: ctx, hostAddr: hostAddr, contractAddr: contractAddr}
}

func mustQueryJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

func TestWasmGrpcForwardMarketplaceQueryAllowlist(t *testing.T) {
	h := setupWasmQueryHarness(t)
	vestingAmount := queryWorkCoins + queryRewardCoins
	require.NoError(t, h.app.InferenceKeeper.SetEffectiveEpochIndex(h.ctx, queryCurrentEpoch))
	require.NoError(t, h.app.InferenceKeeper.SetClaimRecipientForEpoch(h.ctx, h.hostAddr, queryClaimEpoch, h.contractAddr.String()))
	require.NoError(t, h.app.InferenceKeeper.SetEpochPerformanceSummary(h.ctx, inferencetypes.EpochPerformanceSummary{
		EpochIndex:    queryClaimEpoch,
		ParticipantId: h.hostAddr.String(),
		RewardedCoins: uint64(queryRewardCoins),
		EarnedCoins:   uint64(queryWorkCoins),
		Claimed:       true,
	}))
	require.NoError(t, h.app.BankKeeper.SendCoinsFromAccountToModule(
		h.ctx,
		h.hostAddr,
		inferencetypes.ModuleName,
		sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, math.NewInt(vestingAmount))),
	))
	vestingEpochs := uint64(3)
	require.NoError(t, h.app.StreamvestingKeeper.AddVestedRewards(
		h.ctx,
		h.contractAddr.String(),
		inferencetypes.ModuleName,
		sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, math.NewInt(vestingAmount))),
		&vestingEpochs,
		"wasm-query-allowlist-state",
	))

	allowedQueries := []struct {
		name     string
		query    any
		expected string
	}{
		{name: "current_epoch", query: map[string]any{"get_current_epoch": map[string]any{}}, expected: fmt.Sprintf(`{"epoch":%d}`, queryCurrentEpoch)},
		{
			name:     "claim_recipients",
			query:    map[string]any{"list_claim_recipients": map[string]any{"participant": h.hostAddr.String()}},
			expected: fmt.Sprintf(`{"entries":[{"epoch":%d,"recipient":"%s"}]}`, queryClaimEpoch, h.contractAddr),
		},
		{
			name:  "participant_epoch_summary",
			query: map[string]any{"epoch_performance_summary": map[string]any{"epoch_index": queryClaimEpoch, "participant_id": h.hostAddr.String()}},
			expected: fmt.Sprintf(
				`{"epoch_index":%d,"participant_id":"%s","earned_coins":%d,"rewarded_coins":%d,"claimed":true}`,
				queryClaimEpoch, h.hostAddr, queryWorkCoins, queryRewardCoins,
			),
		},
		{
			name:     "total_vesting",
			query:    map[string]any{"total_vesting": map[string]any{"participant_address": h.contractAddr.String()}},
			expected: fmt.Sprintf(`{"total_amount":[{"denom":"ngonka","amount":"%d"}]}`, vestingAmount),
		},
	}
	for _, test := range allowedQueries {
		t.Run(test.name, func(t *testing.T) {
			response, err := h.app.WasmKeeper.QuerySmart(h.ctx, h.contractAddr, mustQueryJSON(t, test.query))
			require.NoError(t, err)
			require.JSONEq(t, test.expected, string(response))
		})
	}

	deniedPaths := []struct {
		name string
		path string
	}{
		{name: "denies_epoch_summary_scan", path: "/inference.inference.Query/EpochPerformanceSummary"},
		{name: "denies_epoch_summary_all", path: "/inference.inference.Query/EpochPerformanceSummaryAll"},
		{name: "denies_vesting_schedule", path: "/inference.streamvesting.Query/VestingSchedule"},
		{name: "denies_unknown_path", path: "/inference.inference.Query/DefinitelyNotAllowed"},
	}
	for _, test := range deniedPaths {
		t.Run(test.name, func(t *testing.T) {
			query := map[string]any{"raw_grpc": map[string]any{"path": test.path, "data": ""}}
			_, err := h.app.WasmKeeper.QuerySmart(h.ctx, h.contractAddr, mustQueryJSON(t, query))
			require.ErrorIs(t, err, wasmtypes.ErrQueryFailed, "unlisted gRPC path %s must fail in the Wasm query boundary", test.path)
			require.ErrorContains(t, err, fmt.Sprintf("'%s' path is not allowed from the contract", test.path))
		})
	}

	t.Run("malformed_protobuf_is_not_rejected_by_allowlist", func(t *testing.T) {
		const allowedPath = "/inference.inference.Query/GetCurrentEpoch"
		query := map[string]any{"raw_grpc": map[string]any{"path": allowedPath, "data": "/w=="}}
		_, err := h.app.WasmKeeper.QuerySmart(h.ctx, h.contractAddr, mustQueryJSON(t, query))
		require.ErrorIs(t, err, wasmtypes.ErrQueryFailed)
		require.NotContains(
			t,
			err.Error(),
			"path is not allowed from the contract",
			"an allowed path with a malformed payload must get past the allowlist boundary",
		)
	})
}
