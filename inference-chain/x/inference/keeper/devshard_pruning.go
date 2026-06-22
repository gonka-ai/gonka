package keeper

import (
	"context"
	"fmt"
	"math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const DevshardPruningThreshold = uint64(2)
const DevshardPruningMax = int64(100)

// refundUnsettledEscrow returns the full locked amount to the creator when an
// escrow ages out unsettled. Settlement is the only proof of validator work, so
// an unsettled escrow pays no validators; the full amount is still held by the
// module account (the unsettled path never disbursed it) and is refunded intact.
func (k Keeper) refundUnsettledEscrow(ctx context.Context, escrow types.DevshardEscrow) error {
	if escrow.Amount == 0 {
		return nil
	}
	if escrow.Amount > math.MaxInt64 {
		return fmt.Errorf("unsettled escrow %d refund amount %d exceeds max int64", escrow.Id, escrow.Amount)
	}
	creatorAddr, err := sdk.AccAddressFromBech32(escrow.Creator)
	if err != nil {
		return fmt.Errorf("invalid creator address %q for unsettled escrow %d: %w", escrow.Creator, escrow.Id, err)
	}
	coins, err := types.GetCoins(int64(escrow.Amount))
	if err != nil {
		return fmt.Errorf("invalid refund amount for unsettled escrow %d: %w", escrow.Id, err)
	}
	if err := k.BankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, creatorAddr, coins, "devshard_escrow_unsettled_refund"); err != nil {
		return fmt.Errorf("failed to refund unsettled escrow %d to creator %s: %w", escrow.Id, escrow.Creator, err)
	}
	return nil
}
