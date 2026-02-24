package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) AddPunishmentGraceEpoch(ctx context.Context, epochIndex uint64, binomTestP0 *types.Decimal, upgradeProtectionWindow int64) error {
	return k.PunishmentGraceEpochs.Set(ctx, epochIndex, types.GraceEpochParams{
		EpochIndex:              epochIndex,
		BinomTestP0:             binomTestP0,
		UpgradeProtectionWindow: upgradeProtectionWindow,
	})
}

func (k Keeper) GetPunishmentGraceEpoch(ctx context.Context, epochIndex uint64) (*types.GraceEpochParams, bool) {
	params, err := k.PunishmentGraceEpochs.Get(ctx, epochIndex)
	if err != nil {
		return nil, false
	}
	return &params, true
}
