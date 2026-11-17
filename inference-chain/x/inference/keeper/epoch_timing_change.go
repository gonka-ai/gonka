package keeper

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

// validateEpochTimingChange rejects a change of epoch timing that would move a
// boundary of the running epoch, or of an active confirmation PoC, behind the
// current height. EndBlock fires stages on equality, so a skipped boundary
// stalls or halts the epoch machine (#344).
func (k Keeper) validateEpochTimingChange(ctx sdk.Context, oldParams, newParams types.Params) error {
	if oldParams.EpochParams == nil || newParams.EpochParams == nil ||
		!epochTimingChanged(oldParams, newParams) {
		return nil
	}

	active, err := k.ActiveConfirmationPoCEventItem.Has(ctx)
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("epoch timing params cannot change while a confirmation PoC event is active")
	}

	epoch, found := k.GetEffectiveEpoch(ctx)
	if !found || epoch == nil {
		return nil
	}
	height := ctx.BlockHeight()
	for _, p := range []types.Params{oldParams, newParams} {
		ec := types.NewEpochContext(*epoch, *p.EpochParams)
		from := ec.ClaimMoney()
		to := ec.NextPoCStart() - p.DelegationParams.GetDeployWindow()
		if height <= from || height >= to {
			return fmt.Errorf("epoch timing params can only change after ClaimMoney and before the delegation snapshot of epoch %d under both old and new params: height %d is outside (%d, %d)",
				epoch.Index, height, from, to)
		}
	}
	return nil
}

func epochTimingChanged(oldParams, newParams types.Params) bool {
	a, b := oldParams.EpochParams, newParams.EpochParams
	return oldParams.DelegationParams.GetDeployWindow() != newParams.DelegationParams.GetDeployWindow() ||
		a.EpochLength != b.EpochLength ||
		a.EpochShift != b.EpochShift ||
		a.PocStageDuration != b.PocStageDuration ||
		a.PocExchangeDuration != b.PocExchangeDuration ||
		a.PocValidationDelay != b.PocValidationDelay ||
		a.PocValidationDuration != b.PocValidationDuration ||
		a.SetNewValidatorsDelay != b.SetNewValidatorsDelay
}
