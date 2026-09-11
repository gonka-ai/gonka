package pocchallenge

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/productscience/inference/x/inference/types"
)

func CompensationReasons(reason types.PoCChallengeFailReason) bool {
	return reason == types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_REJECTED ||
		reason == types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_UNDERWEIGHT
}

func SettleChallengePayments(ctx context.Context, chain Chain, store *Store, epochIndex uint64) error {
	list, err := store.ListOpen(ctx)
	if err != nil {
		return err
	}
	params, err := chain.GetParams(ctx)
	if err != nil {
		return err
	}
	var vesting *uint64
	if params.TokenomicsParams != nil && params.TokenomicsParams.RewardVestingPeriod > 0 {
		v := params.TokenomicsParams.RewardVestingPeriod
		vesting = &v
	}
	for _, ch := range list {
		if ch.EpochIndex > epochIndex {
			continue
		}
		if !ReadyToPay(ch) {
			continue
		}
		summary, found := chain.GetEpochPerformanceSummary(ctx, ch.EpochIndex, ch.Target)
		if !found {
			continue
		}
		sdkCtx := sdk.UnwrapSDKContext(ctx)
		cacheCtx, write := sdkCtx.CacheContext()
		if err := settleOne(cacheCtx, chain, store, ch, vesting, summary); err != nil {
			chain.LogError("SettleChallengePayments failed", types.PoC,
				"target", ch.Target, "error", err)
			continue
		}
		write()
	}
	return nil
}

func ReadyToPay(ch types.PoCChallenge) bool {
	if ch.FailReason != types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
		return true
	}
	if IsGenerating(ch) {
		return false
	}
	for _, seg := range ch.Segments {
		if seg != nil && seg.Outcome == types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PENDING {
			return false
		}
	}
	return true
}

func settleOne(ctx context.Context, chain Chain, store *Store, ch types.PoCChallenge, vesting *uint64, summary types.EpochPerformanceSummary) error {
	if ch.LockedPayment > 0 {
		coins, err := types.GetCoins(int64(ch.LockedPayment))
		if err != nil {
			return err
		}
		if ch.FailReason == types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
			if err := chain.PayParticipantFromModule(ctx, ch.Target, int64(ch.LockedPayment), types.ModuleName, "poc_challenge_pass", vesting); err != nil {
				return err
			}
		} else {
			challenger, err := sdk.AccAddressFromBech32(ch.Challenger)
			if err != nil {
				return err
			}
			if err := chain.SendCoinsFromModuleToAccount(ctx, types.ModuleName, challenger, coins, "poc_challenge_refund"); err != nil {
				return err
			}
		}
	}

	if CompensationReasons(ch.FailReason) {
		unpaid := ch.UnpaidRewardShare
		if summary.RewardedCoins < unpaid {
			unpaid -= summary.RewardedCoins
		} else {
			unpaid = 0
		}
		comp := ch.ExpectedReward
		if unpaid < comp {
			comp = unpaid
		}
		if comp > 0 {
			coins, err := types.GetCoins(int64(comp))
			if err != nil {
				return err
			}
			if err := chain.SendCoinsFromModuleToModule(ctx, govtypes.ModuleName, types.ModuleName, coins, "poc_challenge_compensation"); err != nil {
				return err
			}
			if err := chain.PayParticipantFromModule(ctx, ch.Challenger, int64(comp), types.ModuleName, "poc_challenge_compensation", vesting); err != nil {
				return err
			}
		}
	}
	return store.DeleteChallengeData(ctx, ch)
}
