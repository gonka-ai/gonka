package v0_2_11

import (
	"context"
	"encoding/json"
	"errors"

	"cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func Gonka(amount int64) int64 {
	return amount * 1_000_000_000
}

type BountyReward struct {
	Address string
	Amount  int64
}

var bountyRewards = []BountyReward{
	// Extra bounty for a comprehensive review of all cases where the data race conditions fix was needed.
	// PR: https://github.com/gonka-ai/gonka/pull/543
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: Gonka(2500)},

	// PoC Integration into vLLM v0.11.1.
	// Issue: https://github.com/gonka-ai/gonka/issues/628
	{Address: "gonka1yhdhp4vwsvdsplv4acksntx0zxh8saueq6lj9m", Amount: Gonka(25000)},

	// Report of series of prompts resulting in vLLM HTTP 502 response, significant impact.
	{Address: "gonka12jaf7m4eysyqt32mrgarum6z96vt55tckvcleq", Amount: Gonka(10000)},

	// Report of dust transaction vulnerability extending blocks.
	{Address: "gonka12jaf7m4eysyqt32mrgarum6z96vt55tckvcleq", Amount: Gonka(1000)},

	// Report of Remote DoS of Validator PoC Software via dist Assertion.
	{Address: "gonka1f0elpwnx7ezytdlck35003nz6qk8kzvurvnj4a", Amount: Gonka(5000)},

	// Report of State Bloat PoC and End-Block DoS via Unbounded Batch / Validation Payloads.
	{Address: "gonka1f0elpwnx7ezytdlck35003nz6qk8kzvurvnj4a", Amount: Gonka(5000)},

	// Report of bridge Ethereum address parsing fallback vulnerability.
	{Address: "gonka1f0elpwnx7ezytdlck35003nz6qk8kzvurvnj4a", Amount: Gonka(750)},

	// Planned task.
	// PR: https://github.com/gonka-ai/gonka/pull/775
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: Gonka(1000)},

	// Planned task.
	// PR: https://github.com/gonka-ai/gonka/pull/773
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: Gonka(1250)},

	// vLLM 0.15.1 compatibility experiments; basis for next ML node version.
	// PR: https://github.com/qdanik/vllm/pull/5
	{Address: "gonka1j3f2xkapx8cmczpjqcsrh7cc3peyj3ngkjv4p8", Amount: Gonka(12000)},

	// vLLM 0.15.1 compatibility experiments covering simultaneous PoC and inference.
	// PR: https://github.com/qdanik/vllm/pull/6
	{Address: "gonka1j3f2xkapx8cmczpjqcsrh7cc3peyj3ngkjv4p8", Amount: Gonka(15000)},

	// Report of wind down window vulnerability fixed in PR #767.
	{Address: "gonka1j3f2xkapx8cmczpjqcsrh7cc3peyj3ngkjv4p8", Amount: Gonka(5000)},

	// Collective solving of nodes unable to join from snapshots - proposed valuable hypothesis.
	// Issue: https://github.com/gonka-ai/gonka/issues/797
	{Address: "gonka1ejkupq3cy6p8xd64ew2wlzveml86ckpzn9dl56", Amount: Gonka(1000)},

	// Collective solving of nodes unable to join from snapshots - found source problem.
	// Issue: https://github.com/gonka-ai/gonka/issues/797
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: Gonka(3000)},

	// Collective solving StartInference and FinishInference issue.
	// Issue: https://github.com/gonka-ai/gonka/issues/780
	{Address: "gonka17kmfwzthep3alxt57vqcqr48uv7swp0u63gcnj", Amount: Gonka(750)},

	// Collective solving StartInference and FinishInference issue.
	// Issue: https://github.com/gonka-ai/gonka/issues/781
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: Gonka(5000)},

	// Collective solving StartInference and FinishInference issue.
	// Issue: https://github.com/gonka-ai/gonka/issues/782
	{Address: "gonka1ejkupq3cy6p8xd64ew2wlzveml86ckpzn9dl56", Amount: Gonka(5000)},

	// Important issue that affected many participants; extra payment for testing with fix.
	// PR: https://github.com/gonka-ai/gonka/pull/867
	{Address: "gonka128nd36m2pz5qcs4q6rd69622flyls05nleazqq", Amount: Gonka(7500)},

	// vLLM 0.15.1 compatibility experiments; basis for next ML node version.
	// Issue: https://github.com/gonka-ai/gonka/issues/730
	{Address: "gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp", Amount: Gonka(22500)},

	// Batch Transfer With Vesting implementation.
	// PR: https://github.com/gonka-ai/gonka/pull/835
	{Address: "gonka100s7x2t0npruu9ta02306qfmaened3vg3a9dn6", Amount: Gonka(5000)},

	// Collateral slashing vulnerability and fix; low severity.
	// PR: https://github.com/gonka-ai/gonka/pull/868
	{Address: "gonka1j3f2xkapx8cmczpjqcsrh7cc3peyj3ngkjv4p8", Amount: Gonka(5000)},

	// v0.2.11 release management.
	{Address: "gonka1ejkupq3cy6p8xd64ew2wlzveml86ckpzn9dl56", Amount: Gonka(7500)},

	// v0.2.11 release management.
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: Gonka(7500)},

	// v0.2.10 upgrade review.
	{Address: "gonka1s8szs7n43jxgz4a4xaxmzm5emh7fmjxhach7w8", Amount: Gonka(2500)},

	// v0.2.10 upgrade review.
	{Address: "gonka12jaf7m4eysyqt32mrgarum6z96vt55tckvcleq", Amount: Gonka(2500)},

	// v0.2.10 upgrade review.
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: Gonka(2500)},
}

// MigrationData expected in the Plan.Info JSON
type MigrationData struct {
	CommunitySaleAddress string `json:"community_sale_address"`
	NewCodeID            uint64 `json:"new_code_id"`
}

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	distrKeeper distrkeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Keep capability module version explicit to avoid re-running InitGenesis
		// on chains where capability state already exists but version map is missing.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		err := setParameters(ctx, k)
		if err != nil {
			return nil, err
		}
		err = setPruningState(ctx, k)
		if err != nil {
			return nil, err
		}

		err = setEpochParticipantsSets(ctx, k)
		if err != nil {
			return fromVM, err
		}

		err = k.MigrateEpochGroupValidationsToEntries(ctx)
		if err != nil {
			return fromVM, err
		}

		if err := distributeBountyRewards(ctx, k, distrKeeper); err != nil {
			return nil, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		// Execute Dynamic Contract Migration from Plan.Info
		if err := executeContractMigration(ctx, k, plan.Info); err != nil {
			k.LogError("contract migration failed", types.Upgrades, "error", err)
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

func setEpochParticipantsSets(ctx context.Context, k keeper.Keeper) error {
	currentEpochIndex, err := k.EffectiveEpochIndex.Get(ctx)
	if err != nil {
		return err
	}
	if currentEpochIndex < 2 {
		return err
	}
	err = setEpochParticipantsSet(ctx, k, currentEpochIndex)
	if err != nil {
		return err
	}
	err = setEpochParticipantsSet(ctx, k, currentEpochIndex-1)
	if err != nil {
		return err
	}
	return nil
}

// executeContractMigration parses the JSON Plan.Info and triggers Contract Migration.
// It uses `allow_all_trade_tokens` set to true to complete the community-sale migration payload.
func executeContractMigration(ctx context.Context, k keeper.Keeper, infoJSON string) error {
	// Note: For all failures except for actual migration issues
	// we return nil so the chain will continue. Otherwise we just need to
	// fix the (obvious) error and try again.
	if infoJSON == "" {
		k.LogInfo("no migration data found in Plan.Info, skipping contract migration", types.Upgrades)
		return nil
	}

	var data MigrationData
	if err := json.Unmarshal([]byte(infoJSON), &data); err != nil {
		k.LogError("failed to unmarshal Plan.Info", types.Upgrades, "info", infoJSON, "error", err)
		// Log the error and do NOT kill the chain
		return nil
	}

	// Get the governance admin address
	adminAddr, err := sdk.AccAddressFromBech32(k.GetAuthority())
	if err != nil {
		k.LogError("invalid governance address", types.Upgrades, "error", err)
		return nil
	}

	// Make sure both arguments are provided
	if data.CommunitySaleAddress == "" || data.NewCodeID == 0 {
		k.LogInfo("incomplete migration data in Plan.Info, skipping contract migration", types.Upgrades, "info", infoJSON)
		return nil
	}

	contractAddr, err := sdk.AccAddressFromBech32(data.CommunitySaleAddress)
	if err != nil {
		k.LogError("invalid contract address in Plan.Info", types.Upgrades, "address", data.CommunitySaleAddress, "error", err)
		return nil
	}

	// Prepare the CosmWasm Migrate message (enabling all trade tokens natively)
	migrateMsg := []byte(`{"allow_all_trade_tokens":true}`)

	// Perform the actual contract migration via the Wasm Keeper
	permissionedKeeper := wasmkeeper.NewGovPermissionKeeper(k.GetWasmKeeper())
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	_, err = permissionedKeeper.Migrate(sdkCtx, contractAddr, adminAddr, data.NewCodeID, migrateMsg)
	if err != nil {
		k.LogError("failed to migrate community sale contract", types.Upgrades, "address", data.CommunitySaleAddress, "new_code_id", data.NewCodeID, "error", err)
		return err // We critically fail the upgrade if migration fails but parameters were provided
	}

	k.LogInfo("successfully migrated community sale contract", types.Upgrades, "address", data.CommunitySaleAddress, "new_code_id", data.NewCodeID)
	return nil
}

// setParameters sets the safety_window parameter to 50.
func setParameters(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("failed to get params during upgrade", types.Upgrades, "error", err)
		return err
	}

	// Impossible, but explicitness is important
	if params.EpochParams == nil || params.ValidationParams == nil {
		k.LogError("params not initialized", types.Upgrades)
		return errors.New("Params not initialized")
	}

	params.EpochParams.ConfirmationPocSafetyWindow = 50

	params.ValidationParams.ClaimValidationEnabled = false

	params.SubnetEscrowParams = &types.SubnetEscrowParams{
		MinAmount:          50_000_000,      // 0.05 GNK
		MaxAmount:          100_000_000_000, // 100 GNK
		MaxEscrowsPerEpoch: 100,
		GroupSize:          16,
		TokenPrice:         10,
		AllowedCreatorAddresses: []string{
			"gonka10fynmy2npvdvew0vj2288gz8ljfvmjs35lat8n",
			"gonka1uyqp5z3dveamfw4pmw7p7rfvwdvgzewnqrzhsu",
			"gonka1sy7ug80wrnm6gk47creak0j5eagjpf7maqcqwk",
			"gonka1w66aw6jayepglwgz66qtunetr5nyw9ls7evq5g",
			"gonka1v8gk5z7gcv72447yfcd2y8g78qk05yc4f3nk4w",
			"gonka1gndhek2h2y5849wf6tmw6gnw9qn4vysgljed0u",
			"gonka1z66ec2zedwpapp6jrj9raxgl93e5ec9z5my52h",
			"gonka1jw6xg0wun3g8m2fjm8lula82dw5p6jl8yp28mn",
			"gonka15sjedpgseutpnrjx2ge3mgau3s8ft5qzym9waa",
			"gonka1l4a2wtls9rgd2mnnj6mheml5xlq3kknngj4p7h",
			"gonka1f3yg5385n3f9pdw2g3dcjcnfqyej67hcu9vfet",
			"gonka15g5pu70k7l6hvdt8xl80h4mxe332762csupaeg",
			"gonka1p0uanq0aay6n3l4gtnshg63cy6vx3zgvkyc5lc",
		},
	}
	if err := k.SetParams(ctx, params); err != nil {
		k.LogError("failed to set params with safety window", types.Upgrades, "error", err)
		return err
	}

	k.LogInfo("set safety window", types.Upgrades, "safety_window", params.EpochParams.ConfirmationPocSafetyWindow)
	return nil
}

func setPruningState(ctx context.Context, k keeper.Keeper) error {
	state, err := k.PruningState.Get(ctx)
	if err != nil {
		return err
	}
	state.EpochGroupValidationsPrunedEpoch = 0
	state.SubnetPrunedEpoch = 0
	return k.PruningState.Set(ctx, state)
}

func setEpochParticipantsSet(ctx context.Context, k keeper.Keeper, epochIndex uint64) error {
	epochActiveParticipants, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return types.ErrEpochNotFound
	}
	return k.SetActiveParticipantsCache(ctx, epochActiveParticipants)
}

func distributeBountyRewards(ctx context.Context, k keeper.Keeper, distrKeeper distrkeeper.Keeper) error {
	if len(bountyRewards) == 0 {
		k.Logger().Info("No bounty rewards to distribute")
		return nil
	}

	var totalRequired int64
	for _, bounty := range bountyRewards {
		totalRequired += bounty.Amount
	}

	feePool, err := distrKeeper.FeePool.Get(ctx)
	if err != nil {
		k.Logger().Warn("failed to get fee pool, skipping bounty distribution", "error", err)
		return nil
	}

	available := feePool.CommunityPool.AmountOf(types.BaseCoin).TruncateInt64()
	if available < totalRequired {
		k.Logger().Warn("insufficient fee pool balance, skipping bounty distribution",
			"required", totalRequired, "available", available)
		return nil
	}

	k.Logger().Info("fee pool balance sufficient", "required", totalRequired, "available", available)

	for _, bounty := range bountyRewards {
		recipient, err := sdk.AccAddressFromBech32(bounty.Address)
		if err != nil {
			k.Logger().Error("invalid bounty address", "address", bounty.Address, "error", err)
			continue
		}

		coins := sdk.NewCoins(sdk.NewCoin(types.BaseCoin, math.NewInt(bounty.Amount)))
		if err := distrKeeper.DistributeFromFeePool(ctx, coins, recipient); err != nil {
			k.Logger().Error("failed to distribute bounty", "address", bounty.Address, "error", err)
			continue
		}

		k.Logger().Info("bounty distributed", "address", bounty.Address, "amount", bounty.Amount)
	}

	return nil
}
