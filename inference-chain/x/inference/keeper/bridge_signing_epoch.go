package keeper

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k Keeper) GetBridgeSigningEpochID(ctx sdk.Context, effectiveEpochID uint64) (uint64, error) {
	if k.BlsKeeper == nil {
		return 0, fmt.Errorf("BLS keeper is unavailable")
	}
	return k.BlsKeeper.GetBridgeSigningEpochID(ctx, effectiveEpochID)
}
