package keeper

import (
	"context"
)

// StartRevalidationVote starts a revalidation vote for the inference. If storeRevalidationVotes is true,
// creates the inference voting record in keeper storage and adds the first vote (invalidator votes for invalidation, pass=false).
// If false, only records the session in the ephemeral cache (eligible participants are already in revalidationVoteParticipants).
func (k *Keeper) StartRevalidationVote(ctx context.Context, inferenceId string, invalidator string, invalidatorWeight int64, totalEligibleWeight int64, blockHeight int64) error {
	if inferenceId == "" || totalEligibleWeight <= 0 {
		return nil
	}
	if k.storeRevalidationVotes {
		_ = k.ClearInferenceRevalidationsFromStore(ctx, inferenceId)
		if err := k.InferenceRevalidationTotalEligibleWeight.Set(ctx, inferenceId, totalEligibleWeight); err != nil {
			return err
		}
		return k.AddRevalidationVoteToStore(ctx, inferenceId, invalidator, invalidatorWeight, false)
	}
	k.ephemeralRevalidationVotes.StartSession(blockHeight, inferenceId, totalEligibleWeight, invalidator, invalidatorWeight)
	return nil
}

// AddRevalidationVoteAndCheckThreshold adds a revalidation vote (store or ephemeral per flag) and returns
// passTotal, noPassTotal, whether 50% threshold was reached, and whether invalidation won (noPass >= 50%).
// When using keeper storage, the inference must have been started with StartRevalidationVote (total weight stored).
func (k *Keeper) AddRevalidationVoteAndCheckThreshold(ctx context.Context, inferenceId string, pass bool, participant string, weight int64, blockHeight int64) (passTotal, noPassTotal int64, thresholdReached bool, invalidateWon bool, err error) {
	if k.storeRevalidationVotes {
		if err := k.AddRevalidationVoteToStore(ctx, inferenceId, participant, weight, pass); err != nil {
			return 0, 0, false, false, err
		}
		total, _ := k.InferenceRevalidationTotalEligibleWeight.Get(ctx, inferenceId)
		rec, found := k.GetInferenceRevalidationsFromStore(ctx, inferenceId)
		if !found || rec == nil {
			return 0, 0, false, false, nil
		}
		for _, v := range rec.Votes {
			if v.Pass {
				passTotal += v.Weight
			} else {
				noPassTotal += v.Weight
			}
		}
		half := total / 2
		thresholdReached = passTotal >= half || noPassTotal >= half
		invalidateWon = noPassTotal >= half
		return passTotal, noPassTotal, thresholdReached, invalidateWon, nil
	}
	passTotal, noPassTotal, thresholdReached, invalidateWon = k.ephemeralRevalidationVotes.AddVote(inferenceId, pass, participant, weight)
	return passTotal, noPassTotal, thresholdReached, invalidateWon, nil
}

// IsRevalidationVoteInKeeper returns true if this inference has a revalidation vote session in keeper (persistent or ephemeral).
// Used to decide whether to use keeper path (this) or x/group (epochGroup.Revalidate).
func (k *Keeper) IsRevalidationVoteInKeeper(ctx context.Context, inferenceId string) bool {
	if k.storeRevalidationVotes {
		_, err := k.InferenceRevalidationTotalEligibleWeight.Get(ctx, inferenceId)
		return err == nil
	}
	return k.ephemeralRevalidationVotes.HasSession(inferenceId)
}

// GetRevalidationInvalidator returns the address that started the revalidation vote (invalidator) for keeper path.
// For stored votes: first vote with Pass=false; for ephemeral: from cache. Returns "" if not found.
func (k *Keeper) GetRevalidationInvalidator(ctx context.Context, inferenceId string) string {
	if k.storeRevalidationVotes {
		rec, found := k.GetInferenceRevalidationsFromStore(ctx, inferenceId)
		if !found || rec == nil {
			return ""
		}
		for _, v := range rec.Votes {
			if !v.Pass {
				return v.Address
			}
		}
		return ""
	}
	return k.ephemeralRevalidationVotes.GetInvalidator(inferenceId)
}
