// Package v0_2_14 holds the upgrade handler scaffold for the v0.2.14 release.
//
// At bootstrap time this stays intentionally small: capability-version fix
// plus RunMigrations. As upgrade work lands, add migration steps below the
// capability fix and above RunMigrations.
//
// If later work bumps a module ConsensusVersion, it must also register the
// corresponding migration in app/upgrades.go's registerMigrations().
package v0_2_14

import (
	"context"
	"fmt"

	math "cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	mintkeeper "github.com/cosmos/cosmos-sdk/x/mint/keeper"

	genesistransferkeeper "github.com/productscience/inference/x/genesistransfer/keeper"
	genesistransfertypes "github.com/productscience/inference/x/genesistransfer/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

var devshardAllowedCreatorAddressesToAdd = []string{
	// Dahl / @paranjko
	"gonka1t9akhsrqjkavh68c7cannlfdj58y25vsewfflt",
}

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	gtKeeper genesistransferkeeper.Keeper,
	mintKeeper mintkeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		// v0.2.13 introduced new DevshardEscrowParams fields but mainnet executed
		// that upgrade before these backfills landed. Repair on-disk state here.
		if err := backfillDevshardEscrowParamDefaults(ctx, k); err != nil {
			return nil, err
		}
		if err := setDevshardAllowedCreatorAddresses(ctx, k); err != nil {
			return nil, err
		}
		if err := backfillDevshardEscrowFees(ctx, k); err != nil {
			return nil, err
		}
		if err := backfillDevshardEscrowInferenceSealGrace(ctx, k); err != nil {
			return nil, err
		}
		if err := seedDelegationRewardSnapshotForEffectiveEpoch(ctx, k); err != nil {
			return nil, err
		}
		if err := zeroMintInflation(ctx, k, mintKeeper); err != nil {
			return nil, err
		}
		if err := burnFeeCollectorBalance(ctx, k); err != nil {
			return nil, err
		}

		// Initialize maintenance params with defaults for existing chains.
		// All participants start with zero credit — credit is earned going forward.
		if err := initMaintenanceParams(ctx, k); err != nil {
			return nil, err
		}

		// Update genesistransfer params to enable the whitelist and restrict to founders
		if err := updateGenesisTransferParams(ctx, gtKeeper); err != nil {
			return nil, fmt.Errorf("update genesistransfer params: %w", err)
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

func zeroMintInflation(ctx context.Context, k keeper.Keeper, mintKeeper mintkeeper.Keeper) error {
	params, err := mintKeeper.Params.Get(ctx)
	if err != nil {
		return fmt.Errorf("get mint params: %w", err)
	}

	oldInflationMax := params.InflationMax
	oldInflationMin := params.InflationMin
	oldInflationRateChange := params.InflationRateChange
	params.InflationMax = math.LegacyZeroDec()
	params.InflationMin = math.LegacyZeroDec()
	params.InflationRateChange = math.LegacyZeroDec()
	if err := mintKeeper.Params.Set(ctx, params); err != nil {
		return fmt.Errorf("set mint params: %w", err)
	}

	minter, err := mintKeeper.Minter.Get(ctx)
	if err != nil {
		return fmt.Errorf("get mint minter: %w", err)
	}

	oldInflation := minter.Inflation
	oldAnnualProvisions := minter.AnnualProvisions
	minter.Inflation = math.LegacyZeroDec()
	minter.AnnualProvisions = math.LegacyZeroDec()
	if err := mintKeeper.Minter.Set(ctx, minter); err != nil {
		return fmt.Errorf("set mint minter: %w", err)
	}

	k.LogInfo("zeroed mint inflation", types.Upgrades,
		"old_inflation_max", oldInflationMax.String(),
		"old_inflation_min", oldInflationMin.String(),
		"old_inflation_rate_change", oldInflationRateChange.String(),
		"old_inflation", oldInflation.String(),
		"old_annual_provisions", oldAnnualProvisions.String(),
		"new_inflation_max", params.InflationMax.String(),
		"new_inflation_min", params.InflationMin.String(),
		"new_inflation_rate_change", params.InflationRateChange.String(),
		"new_inflation", minter.Inflation.String(),
		"new_annual_provisions", minter.AnnualProvisions.String(),
	)
	return nil
}

func burnFeeCollectorBalance(ctx context.Context, k keeper.Keeper) error {
	feeCollectorAddress := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	balance := k.BankView.GetAllBalances(ctx, feeCollectorAddress)
	burnAmount := balance.AmountOf(types.BaseCoin)
	if burnAmount.IsZero() {
		k.LogInfo("burn fee collector balance skipped: no base denom balance", types.Upgrades,
			"denom", types.BaseCoin,
			"fee_collector_balance", balance.String(),
		)
		return nil
	}

	burnCoins := sdk.NewCoins(sdk.NewCoin(types.BaseCoin, burnAmount))
	const memo = "v0.2.14: burn erroneously minted inflation"
	if err := k.BankKeeper.SendCoinsFromModuleToModule(ctx, authtypes.FeeCollectorName, types.ModuleName, burnCoins, memo); err != nil {
		return fmt.Errorf("transfer fee collector balance to inference module for burn: %w", err)
	}
	if err := k.BankKeeper.BurnCoins(ctx, types.ModuleName, burnCoins, memo); err != nil {
		return fmt.Errorf("burn fee collector balance: %w", err)
	}

	k.LogInfo("burned fee collector balance", types.Upgrades,
		"amount", burnCoins.String(),
		"fee_collector_balance", balance.String(),
	)
	return nil
}

func setDevshardAllowedCreatorAddresses(ctx context.Context, k keeper.Keeper) error {
	return addDevshardAllowedCreatorAddresses(ctx, k, devshardAllowedCreatorAddressesToAdd)
}

func addDevshardAllowedCreatorAddresses(ctx context.Context, k keeper.Keeper, addresses []string) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.DevshardEscrowParams == nil {
		params.DevshardEscrowParams = types.DefaultDevshardEscrowParams()
	}

	seen := make(map[string]struct{}, len(params.DevshardEscrowParams.AllowedCreatorAddresses)+len(addresses))
	for _, address := range params.DevshardEscrowParams.AllowedCreatorAddresses {
		seen[address] = struct{}{}
	}

	added := 0
	for _, address := range addresses {
		if _, ok := seen[address]; ok {
			continue
		}
		params.DevshardEscrowParams.AllowedCreatorAddresses = append(params.DevshardEscrowParams.AllowedCreatorAddresses, address)
		seen[address] = struct{}{}
		added++
	}

	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("set devshard allowed creator addresses", types.Upgrades,
		"total", len(params.DevshardEscrowParams.AllowedCreatorAddresses),
		"added", added)
	return nil
}

func seedDelegationRewardSnapshotForEffectiveEpoch(ctx context.Context, k keeper.Keeper) error {
	effectiveEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogError("seed delegation reward snapshot skipped: effective epoch not found", types.Upgrades)
		return nil
	}

	snapshot, snapshotFound := k.GetDelegationRewardTransferSnapshot(ctx)
	if snapshotFound {
		if snapshot.EpochIndex != effectiveEpochIndex {
			k.LogWarn("existing delegation reward snapshot epoch differs from effective epoch", types.Upgrades,
				"snapshot_epoch", snapshot.EpochIndex, "effective_epoch", effectiveEpochIndex)
		}
		return nil
	}

	if err := k.SetDelegationRewardTransferSnapshot(ctx, types.DelegationRewardTransferSnapshot{
		EpochIndex: effectiveEpochIndex,
	}); err != nil {
		return err
	}

	k.LogInfo("seeded delegation reward snapshot for effective epoch", types.Upgrades, "epoch", effectiveEpochIndex)
	return nil
}

// initMaintenanceParams initializes the MaintenanceParams sub-struct with defaults.
// The feature starts disabled; governance can enable it once the network is ready.
// No per-participant state initialization is needed because:
//   - MaintenanceState is lazily created via GetOrCreateMaintenanceState
//   - Credit starts at zero (the default for a missing entry)
//   - Maintenance collections (reservations, transitions, indexes) start empty
//
// This is the seeding step from the maintenance-windows PR (#998), relocated
// from the v0.2.12 handler: mainnet executed that upgrade before the feature
// landed, so it could never run there. Two deliberate deviations from the
// original: errors are returned instead of logged-and-swallowed (matching the
// other v0.2.14 migrations, so a failed SetParams halts the upgrade), and
// existing non-nil params short-circuit without a redundant SetParams
// round-trip.
func initMaintenanceParams(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.MaintenanceParams != nil {
		k.LogInfo("maintenance params already present, skipping init", types.Upgrades,
			"maintenance_enabled", params.MaintenanceParams.MaintenanceEnabled)
		return nil
	}

	params.MaintenanceParams = types.DefaultMaintenanceParams()
	if err := k.SetParams(ctx, params); err != nil {
		return err
	}

	k.LogInfo("initialized maintenance params", types.Upgrades,
		"maintenance_enabled", params.MaintenanceParams.MaintenanceEnabled,
		"min_schedule_lead_blocks", params.MaintenanceParams.MaintenanceMinScheduleLeadBlocks,
		"max_window_blocks", params.MaintenanceParams.MaintenanceMaxWindowBlocks,
		"max_concurrent_validators", params.MaintenanceParams.MaintenanceMaxConcurrentValidators,
		"max_concurrent_power_bps", params.MaintenanceParams.MaintenanceMaxConcurrentPowerBps,
		"credit_cap_blocks", params.MaintenanceParams.MaintenanceCreditCapBlocks,
		"credit_earn_per_epoch_blocks", params.MaintenanceParams.MaintenanceCreditEarnPerSuccessfulEpochBlocks,
	)
	return nil
}

// backfillDevshardEscrowParamDefaults seeds zero-valued DevshardEscrowParams
// fields introduced in v0.2.13. Fresh genesis chains get these from defaults;
// mainnet chains that upgraded to v0.2.13 without this migration decode them as
// proto3 zero. Non-zero values are left in place so any governance override
// survives the upgrade.
func backfillDevshardEscrowParamDefaults(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.DevshardEscrowParams == nil {
		params.DevshardEscrowParams = types.DefaultDevshardEscrowParams()
	}

	changed := false

	if params.DevshardEscrowParams.DefaultInferenceSealGraceNonces == 0 {
		groupSize := params.DevshardEscrowParams.GroupSize
		if groupSize == 0 {
			groupSize = types.DefaultDevshardGroupSize
		}
		params.DevshardEscrowParams.DefaultInferenceSealGraceNonces = types.DefaultDevshardInferenceSealGraceNonces(groupSize)
		changed = true
	}
	if params.DevshardEscrowParams.DefaultInferenceSealGraceSeconds == 0 {
		params.DevshardEscrowParams.DefaultInferenceSealGraceSeconds = types.DefaultDevshardInferenceSealGraceSeconds
		changed = true
	}
	if params.DevshardEscrowParams.CreateDevshardFee == 0 {
		params.DevshardEscrowParams.CreateDevshardFee = types.DefaultDevshardCreateDevshardFee
		changed = true
	}
	if params.DevshardEscrowParams.FeePerNonce == 0 {
		params.DevshardEscrowParams.FeePerNonce = types.DefaultDevshardFeePerNonce
		changed = true
	}
	if params.DevshardEscrowParams.RefusalTimeout == 0 {
		params.DevshardEscrowParams.RefusalTimeout = types.DefaultDevshardRefusalTimeout
		changed = true
	}
	if params.DevshardEscrowParams.ExecutionTimeout == 0 {
		params.DevshardEscrowParams.ExecutionTimeout = types.DefaultDevshardExecutionTimeout
		changed = true
	}
	if params.DevshardEscrowParams.ValidationRate == 0 {
		params.DevshardEscrowParams.ValidationRate = types.DefaultDevshardValidationRate
		changed = true
	}
	if params.DevshardEscrowParams.VoteThresholdFactor == 0 {
		params.DevshardEscrowParams.VoteThresholdFactor = types.DefaultDevshardVoteThresholdFactor
		changed = true
	}

	if !changed {
		k.LogInfo("backfill devshard escrow param defaults skipped: nothing to update", types.Upgrades)
		return nil
	}

	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("backfilled devshard escrow param defaults", types.Upgrades,
		"default_inference_seal_grace_nonces", params.DevshardEscrowParams.DefaultInferenceSealGraceNonces,
		"default_inference_seal_grace_seconds", params.DevshardEscrowParams.DefaultInferenceSealGraceSeconds,
		"create_devshard_fee", params.DevshardEscrowParams.CreateDevshardFee,
		"fee_per_nonce", params.DevshardEscrowParams.FeePerNonce,
		"refusal_timeout", params.DevshardEscrowParams.RefusalTimeout,
		"execution_timeout", params.DevshardEscrowParams.ExecutionTimeout,
		"validation_rate", params.DevshardEscrowParams.ValidationRate,
		"vote_threshold_factor", params.DevshardEscrowParams.VoteThresholdFactor,
	)
	return nil
}

// backfillDevshardEscrowFees populates the per-escrow fee snapshot fields
// (create_devshard_fee, fee_per_nonce) on DevshardEscrow rows created before
// v0.2.13 fee snapshots existed. Rows that already carry a non-zero snapshot
// are left untouched.
func backfillDevshardEscrowFees(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.DevshardEscrowParams == nil {
		k.LogInfo("backfill devshard escrow fees skipped: devshard escrow params missing", types.Upgrades)
		return nil
	}
	createFee := params.DevshardEscrowParams.CreateDevshardFee
	feePerNonce := params.DevshardEscrowParams.FeePerNonce

	var updateIDs []uint64
	if err := k.DevshardEscrows.Walk(ctx, nil, func(_ uint64, escrow types.DevshardEscrow) (bool, error) {
		if escrow.CreateDevshardFee != 0 && escrow.FeePerNonce != 0 {
			return false, nil
		}
		updateIDs = append(updateIDs, escrow.Id)
		return false, nil
	}); err != nil {
		return fmt.Errorf("walk devshard escrows for fee backfill: %w", err)
	}

	for _, id := range updateIDs {
		escrow, found := k.GetDevshardEscrow(ctx, id)
		if !found {
			return fmt.Errorf("get devshard escrow %d during fee backfill: not found", id)
		}
		if escrow.CreateDevshardFee == 0 {
			escrow.CreateDevshardFee = createFee
		}
		if escrow.FeePerNonce == 0 {
			escrow.FeePerNonce = feePerNonce
		}
		if err := k.SetDevshardEscrow(ctx, escrow); err != nil {
			return fmt.Errorf("set devshard escrow %d during fee backfill: %w", escrow.Id, err)
		}
	}
	k.LogInfo("backfilled devshard escrow fees", types.Upgrades,
		"updated", len(updateIDs),
		"create_devshard_fee", createFee,
		"fee_per_nonce", feePerNonce,
	)
	return nil
}

// backfillDevshardEscrowInferenceSealGrace populates per-escrow inference seal
// grace snapshots on DevshardEscrow rows created before those fields existed.
// Rows that already carry a non-zero snapshot are left untouched.
func backfillDevshardEscrowInferenceSealGrace(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.DevshardEscrowParams == nil {
		k.LogInfo("backfill devshard escrow inference seal grace skipped: devshard escrow params missing", types.Upgrades)
		return nil
	}
	ep := params.DevshardEscrowParams

	var updateIDs []uint64
	if err := k.DevshardEscrows.Walk(ctx, nil, func(_ uint64, escrow types.DevshardEscrow) (bool, error) {
		if escrow.InferenceSealGraceNonces != 0 && escrow.InferenceSealGraceSeconds != 0 {
			return false, nil
		}
		updateIDs = append(updateIDs, escrow.Id)
		return false, nil
	}); err != nil {
		return fmt.Errorf("walk devshard escrows for inference seal grace backfill: %w", err)
	}

	for _, id := range updateIDs {
		escrow, found := k.GetDevshardEscrow(ctx, id)
		if !found {
			return fmt.Errorf("get devshard escrow %d during inference seal grace backfill: not found", id)
		}
		if escrow.InferenceSealGraceNonces == 0 {
			escrow.InferenceSealGraceNonces = types.DevshardInferenceSealGraceNoncesForCreate(ep, uint32(len(escrow.Slots)))
		}
		if escrow.InferenceSealGraceSeconds == 0 {
			escrow.InferenceSealGraceSeconds = types.DevshardInferenceSealGraceSecondsForCreate(ep)
		}
		if err := k.SetDevshardEscrow(ctx, escrow); err != nil {
			return fmt.Errorf("set devshard escrow %d during inference seal grace backfill: %w", escrow.Id, err)
		}
	}
	k.LogInfo("backfilled devshard escrow inference seal grace", types.Upgrades,
		"updated", len(updateIDs),
		"default_inference_seal_grace_nonces", ep.DefaultInferenceSealGraceNonces,
		"default_inference_seal_grace_seconds", ep.DefaultInferenceSealGraceSeconds,
	)
	return nil
}

// updateGenesisTransferParams updates the genesistransfer parameters to enable the whitelist of founders.
func updateGenesisTransferParams(ctx context.Context, gtKeeper genesistransferkeeper.Keeper) error {
	allowedAccounts := []string{
		"gonka18299ldv2cym0gh5spmm6yncv3at3qgn0t7km5w",
		"gonka16fdw9ve0xj5yy42asg85wgykr4y4kw2l37rkfp",
		"gonka1dtvqtnq6fjvetajs25kuc9f3944fctveuukxpx",
		"gonka1k4rnc2e9upscac85tkjwa43wr6kqpfyqcmdwej",
		"gonka1s7jy6fajstlxzhtxgqmdh4a2cgq26uxcu7jptk",
		"gonka1rusnaafgmk0h464fthswch3pzh0zzpsegrda09",
		"gonka12tz8qs5kxjp6qsdgj2u3rzx009z2qf6hjrwrrl",
		"gonka1n7ph0qezwcr666kp6tesylw4eqnyqwd0g76u7d",
		"gonka1ld2e89rhfmdlke69m444jcd99h7fra4whksdk4",
		"gonka1a0rh2dej6lrnj5zkx005f5jrxem0tpdh4cpcmg",
		"gonka18qmj244rxzt3274357jd2mp45tv8n4508g29r8",
		"gonka1v86vj8e8eqd3gk5zlf9zawrg0w7c7tfa7x9k25",
		"gonka108eh38a2pelplqz2lh3ujjnvk7ucdylql6gfmg",
		"gonka1cpxpnskkf6hlpldu76tya7d53rklnjlqu4uars",
		"gonka17u7kemct38fx3cuj2yh0x5j2fd9zxkuwkes6q7",
		"gonka1th0qvf05sqdd09p9rgq69nlchw5xzpc3ddply8",
		"gonka1pq9r54md9zrd9xqyxx9f3h7as9kfgnp2c0ucat",
		"gonka1gvx5swzrur89kg5r0z5jn9a0krvmhvzx3f2fea",
		"gonka1x29n95z989ycxymqw60w56dpz7kcdxpp8w8y6x",
		"gonka1327uf364ql39e63fax5n8t8y5e8lag4xjl7e49",
		"gonka1tyfdmaulndss4wqpxr24e8reytj03aw338sphz",
		"gonka1akmamql0z9vnd48tfw3d75zexu3dswqcawtnxa",
		"gonka13t3cvaeke9z5vlwps3z7eyp9uj98z2xkt02352",
		"gonka19cw7kkrkdcxkxmkz56hjatfwrunn79fsyvfky5",
		"gonka1689695npe7amwyunaaj9dwz58v8n7cgkw5emn4",
		"gonka1fxls283es0m7cymxky9xuml9x74flfjhkfrz4q",
		"gonka179nv57lqeana9nvgeul8twegj58ccrd2hm5uzc",
		"gonka1ucl3pfq6sy9ggxuxahvjhm52hgl3qu4wg46hsk",
		"gonka1jle7z5mmfczkvy76gvsxud6kjryzhr8c3cpyal",
		"gonka1xn25n0hphw8tkfg6r3hjvh8d6nerpzqjpql3nx",
		"gonka1z5a69mz9gzhphf4eu6yznzru2la0em89xutepm",
		"gonka1lrwu2czkxwaadqnvsttfa5uvc6mshv8wjd3wyz",
		"gonka1469wnu47q925a4ekazp09tx9k5phyj2nv67jv3",
		"gonka1h78hmsvknwp86qzu9xftv9znwga7u0sjghmz20",
		"gonka1qm66c5gqyew27nefdknspf2x5nwvetsk80zvay",
		"gonka1qgstmj6k2xwx5p8s2p0ch2qd32lkgegqkcsljs",
		"gonka1g02qwpgju9xu8lwtqpgrzfqrj7x45tnnhgh3xz",
		"gonka1clvs7fyag6zjkpnvapjj6m0jn38mgsn63tdnhc",
		"gonka18weyd0tlmtvnn47c0fqg0p8jj3q6s2hz3w67nc",
		"gonka1l4z4swg3e0pf937qm08u0cnkszss34kpe32y52",
		"gonka10k4rmsg3e9xrmm2my9dp6lal4nxlenck9uur39",
		"gonka1vthfz8j7gkeu04jx2ju6akcyv6s9apc497x2e3",
		"gonka19xcrqllduyr6ur3l5ldln4qj0jfpaqfx20280q",
	}
	params := genesistransfertypes.Params{
		AllowedAccounts: allowedAccounts,
		RestrictToList:  true,
	}
	return gtKeeper.SetParams(ctx, params)
}
