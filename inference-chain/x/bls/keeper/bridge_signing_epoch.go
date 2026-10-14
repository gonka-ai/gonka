package keeper

import (
	"errors"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/bls/types"
)

func (k Keeper) GetBridgeSigningEpochID(ctx sdk.Context, effectiveEpochID uint64) (uint64, error) {
	current, err := k.getEpochBLSDataBase(ctx, effectiveEpochID)
	if err != nil && !errors.Is(err, types.ErrEpochBLSDataNotFound) {
		return 0, fmt.Errorf("failed to read BLS data for epoch %d: %w", effectiveEpochID, err)
	}
	if err == nil {
		params, err := k.GetParams(ctx)
		if err != nil {
			return 0, fmt.Errorf("failed to get BLS parameters: %w", err)
		}
		if k.validateThresholdSigningEpochPhase(ctx, &current, params) == nil {
			return effectiveEpochID, nil
		}
	}

	if effectiveEpochID > 1 {
		previousID := effectiveEpochID - 1
		previous, err := k.getEpochBLSDataBase(ctx, previousID)
		if err != nil && !errors.Is(err, types.ErrEpochBLSDataNotFound) {
			return 0, fmt.Errorf("failed to read BLS data for epoch %d: %w", previousID, err)
		}
		if err == nil && previous.DkgPhase == types.DKGPhase_DKG_PHASE_SIGNED {
			return previousID, nil
		}
	}

	return 0, fmt.Errorf("no eligible BLS signing epoch for effective epoch %d or its signed predecessor", effectiveEpochID)
}
