package v0_2_18

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	blstypes "github.com/productscience/inference/x/bls/types"
)

// BeaconKeeper is what the block beacon activation needs from x/bls.
type BeaconKeeper interface {
	ActivateBeacon(ctx sdk.Context, extFrom uint64, candidateEpochs []uint64) bool
	GetCurrentSigningEpochID(ctx sdk.Context) (uint64, bool)
	GetActiveEpochID(ctx sdk.Context) (uint64, bool)
	GetParams(ctx context.Context) (blstypes.Params, error)
	SetParams(ctx context.Context, params blstypes.Params) error
}

// ActivateBlockBeacon turns on Step 3 of the block beacon at the upgrade
// height U (rule R6): validators put partials in their vote extensions for
// heights > U (U itself is precommitted by the old binary, and nothing is
// signed before VoteExtensionsEnableHeight anyway). It seeds the beacon key
// from the newest completed DKG around the signing epoch (rule R9) and sets
// beacon_key_switch_delay, a parameter added by this release. Idempotent: a
// second run leaves the activation and parameters unchanged.
func ActivateBlockBeacon(ctx context.Context, k BeaconKeeper) (activated bool, err error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	upgradeHeight := sdkCtx.BlockHeight()
	if upgradeHeight <= 0 {
		return false, fmt.Errorf("block beacon activation at height %d", upgradeHeight)
	}
	if !k.ActivateBeacon(sdkCtx, uint64(upgradeHeight), beaconKeyCandidates(sdkCtx, k)) {
		return false, nil
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return true, fmt.Errorf("read bls params: %w", err)
	}
	params.BeaconKeySwitchDelay = blstypes.DefaultBeaconKeySwitchDelay
	if err := k.SetParams(ctx, params); err != nil {
		return true, fmt.Errorf("set beacon_key_switch_delay: %w", err)
	}
	return true, nil
}

// beaconKeyCandidates lists the epochs whose DKG may be the newest completed
// one: the signing epoch, its neighbours, and the epoch whose DKG is running.
func beaconKeyCandidates(ctx sdk.Context, k BeaconKeeper) []uint64 {
	seen := map[uint64]bool{}
	var out []uint64
	add := func(id uint64) {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if s, ok := k.GetCurrentSigningEpochID(ctx); ok {
		add(s + 1)
		add(s)
		if s > 1 {
			add(s - 1)
		}
		if s > 2 {
			add(s - 2)
		}
	}
	if a, ok := k.GetActiveEpochID(ctx); ok {
		add(a)
		if a > 1 {
			add(a - 1)
		}
	}
	return out
}
