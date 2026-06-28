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

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
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
		if err := backfillDevshardEscrowFees(ctx, k); err != nil {
			return nil, err
		}
		if err := backfillDevshardEscrowInferenceSealGrace(ctx, k); err != nil {
			return nil, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
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
