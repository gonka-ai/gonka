package inference

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"math"
)

// Start with a power of 2 for even distribution?
const EpochNewCoin = 1_048_576
const CoinHalvingHeight = 100

func (am AppModule) SettleAccounts(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockHeight := sdkCtx.BlockHeight()
	halvings := blockHeight / CoinHalvingHeight
	// Halve it that many times
	finalCoin := EpochNewCoin / math.Pow(2.0, float64(halvings))

	participants, err := am.keeper.ParticipantAll(ctx, &types.QueryAllParticipantRequest{})
	if err != nil {
		am.LogError("Error getting participants", "error", err)
		return err
	}

	totalWork := uint64(0)

	// We are iterating twice over this list, which might get expensive?
	// We could instead keep a running total of work done in the state?
	for _, p := range participants.Participant {
		totalWork += p.CoinBalance
	}

	if totalWork != 0 {
		err = am.keeper.MintRewardCoins(ctx, uint64(finalCoin))
		if err != nil {
			am.LogError("Unable to mint new coins!", "error", err)
			return err
		}
	}
	for _, p := range participants.Participant {
		if p.CoinBalance == 0 && p.RefundBalance == 0 {
			continue
		}
		err = am.keeper.SettleParticipant(ctx, &p, totalWork, uint64(finalCoin))
		am.keeper.SetParticipant(ctx, p)
		if err != nil {
			return err
		}
	}

	return nil
}
