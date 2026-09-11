package keeper

import (
	"context"
	"math/big"

	"github.com/productscience/inference/x/inference/keeper/pocchallenge"
)

// FreezeChallengeUnpaidRewardShares stores each open challenge target's
// cap-adjusted share of the minted BitcoinResult.Amount. Call immediately
// before SettleAccounts. Pay subtracts RewardedCoins after settle.
func (k Keeper) FreezeChallengeUnpaidRewardShares(ctx context.Context, epochIndex uint64) error {
	list, err := k.PoCChallenge.ListOpen(ctx)
	if err != nil {
		return err
	}
	var needed bool
	for _, ch := range list {
		if ch.EpochIndex == epochIndex && pocchallenge.CompensationReasons(ch.FailReason) {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}

	inputs, found, err := k.loadBitcoinRewardInputs(ctx, epochIndex)
	if err != nil || !found {
		return err
	}
	amounts, bitcoinResult, err := GetBitcoinSettleAmountsWithTransfers(
		inputs.Participants,
		&inputs.EpochGroupData,
		inputs.Params.BitcoinRewardParams,
		inputs.ValidationParams,
		inputs.SettleParameters,
		inputs.ParticipantMLNodes,
		inputs.RewardTransfers,
		inputs.RewardPenalties,
		k.Logger(),
	)
	if err != nil {
		return err
	}

	shareByAddr := make(map[string]uint64, len(amounts))
	for _, amount := range amounts {
		if amount == nil || amount.TotalRewardWeight == 0 || bitcoinResult.Amount <= 0 {
			continue
		}
		if amount.Settle == nil {
			continue
		}
		share := challengeRewardShare(bitcoinResult.Amount, amount.ParticipantFullWeight, amount.TotalRewardWeight)
		// SettleAmount.Participant is the address
		shareByAddr[amount.Settle.Participant] = share
	}

	for _, ch := range list {
		if ch.EpochIndex != epochIndex {
			continue
		}
		ch.UnpaidRewardShare = shareByAddr[ch.Target]
		if err := k.PoCChallenge.Set(ctx, ch); err != nil {
			return err
		}
	}
	return nil
}

func challengeRewardShare(amount int64, weight, total uint64) uint64 {
	if amount <= 0 || weight == 0 || total == 0 {
		return 0
	}
	result := new(big.Int).SetInt64(amount)
	result.Mul(result, new(big.Int).SetUint64(weight))
	result.Div(result, new(big.Int).SetUint64(total))
	if !result.IsUint64() {
		return 0
	}
	return result.Uint64()
}
