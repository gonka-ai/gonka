package pocchallenge

import (
	"context"
	"math/big"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type TargetSettle struct {
	GrossShare  uint64
	WorkCoins   uint64
	RewardCoins uint64
}

func CompensationReasons(reason types.PoCChallengeFailReason) bool {
	return reason == types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_REJECTED ||
		reason == types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_UNDERWEIGHT ||
		reason == types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_MISSING_COMMIT
}

func GrossShare(minted int64, weight, total uint64) uint64 {
	if minted <= 0 || weight == 0 || total == 0 {
		return 0
	}
	result := new(big.Int).SetInt64(minted)
	result.Mul(result, new(big.Int).SetUint64(weight))
	result.Div(result, new(big.Int).SetUint64(total))
	if !result.IsUint64() {
		return 0
	}
	return result.Uint64()
}

func CompensationFromGross(ch types.PoCChallenge, settled TargetSettle) uint64 {
	if !CompensationReasons(ch.FailReason) {
		return 0
	}
	if settled.WorkCoins != 0 || settled.RewardCoins != 0 {
		return 0
	}
	if ch.ExpectedReward < settled.GrossShare {
		return ch.ExpectedReward
	}
	return settled.GrossShare
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

// SettleInCache pays locked P and optional min(E, gross_share) inside the
// caller's cache. Only challenges for epochIndex are paid. A payout error is
// returned so the caller can roll back settlement.
func SettleInCache(ctx context.Context, chain Chain, store *Store, epochIndex uint64, settled map[string]TargetSettle) (uint64, error) {
	list, err := store.ListOpen(ctx)
	if err != nil {
		return 0, err
	}
	params, err := chain.GetParams(ctx)
	if err != nil {
		return 0, err
	}
	var vesting *uint64
	if params.TokenomicsParams != nil && params.TokenomicsParams.RewardVestingPeriod > 0 {
		v := params.TokenomicsParams.RewardVestingPeriod
		vesting = &v
	}
	var withheld uint64
	for _, ch := range list {
		if ch.EpochIndex != epochIndex {
			continue
		}
		if !ReadyToPay(ch) {
			continue
		}
		comp := CompensationFromGross(ch, settled[ch.Target])
		if err := settleOne(ctx, chain, store, ch, vesting, comp); err != nil {
			return 0, err
		}
		withheld += comp
	}
	return withheld, nil
}

func settleOne(ctx context.Context, chain Chain, store *Store, ch types.PoCChallenge, vesting *uint64, compensation uint64) error {
	if ch.LockedPayment > 0 {
		if ch.FailReason == types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
			if err := chain.PayParticipantFromModule(ctx, ch.Target, int64(ch.LockedPayment), types.ModuleName, "poc_challenge_pass", vesting); err != nil {
				return err
			}
		} else {
			challenger, err := sdk.AccAddressFromBech32(ch.Challenger)
			if err != nil {
				return err
			}
			coins, err := types.GetCoins(int64(ch.LockedPayment))
			if err != nil {
				return err
			}
			if err := chain.SendCoinsFromModuleToAccount(ctx, types.ModuleName, challenger, coins, "poc_challenge_refund"); err != nil {
				return err
			}
		}
	}
	if compensation > 0 {
		if err := chain.PayParticipantFromModule(ctx, ch.Challenger, int64(compensation), types.ModuleName, "poc_challenge_compensation", vesting); err != nil {
			return err
		}
	}
	return store.DeleteChallengeData(ctx, ch)
}
