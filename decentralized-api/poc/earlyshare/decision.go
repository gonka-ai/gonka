package earlyshare

import "sort"

// SharePoint is a single (early_share, weight) data point for the weighted
// median over a (stage, model_id) distribution.
type SharePoint struct {
	Share  float64
	Weight int64
}

// WeightedMedianShare returns the early_share value at which cumulative weight
// crosses half of the total weight. Points are sorted by share ascending and
// their weights accumulated. Points with non-positive weight do not move the
// median but are otherwise ignored. Returns (0, false) when there is no
// positive total weight.
func WeightedMedianShare(points []SharePoint) (float64, bool) {
	var total int64
	filtered := make([]SharePoint, 0, len(points))
	for _, p := range points {
		if p.Weight <= 0 {
			continue
		}
		filtered = append(filtered, p)
		total += p.Weight
	}
	if total <= 0 || len(filtered) == 0 {
		return 0, false
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].Share < filtered[j].Share
	})

	// half is the smallest cumulative weight that reaches the median crossing.
	// Using a strict "> total/2" with rational comparison (2*cum >= total)
	// keeps behavior deterministic for even totals and ties.
	var cum int64
	for _, p := range filtered {
		cum += p.Weight
		if 2*cum >= total {
			return p.Share, true
		}
	}
	// Unreachable when total > 0, but return the last share defensively.
	return filtered[len(filtered)-1].Share, true
}

// MissOutcome is the result of applying the miss-streak state machine.
type MissOutcome struct {
	// VoteNo is true when the participant should be voted no for low early
	// share (subject to the caller's mode gating).
	VoteNo bool
	// NewState is the guard state to persist.
	NewState GuardState
}

// ApplyMissStreak runs the one-miss-grace state machine.
//
// Asymmetry between PoC and CPoC: regular PoC early-share is cheap to fake, so a
// passing PoC round is NOT trusted to clear the streak or demonstrate
// capability. Only a passing confirmation PoC (CPoC) does that. Failures count
// the same in either phase.
//
//   - pass and isConfirmation: passed_once=true, consecutive_misses=0, no vote.
//     (A genuine CPoC pass resets the streak and proves capability.)
//   - pass and !isConfirmation: no vote and no state change. A regular PoC pass
//     neither resets the streak nor sets passed_once.
//   - fail (either phase): consecutive_misses += 1.
//   - fail and !passed_once: no vote (never demonstrated capability via CPoC).
//   - fail and passed_once: allow one grace miss (consecutive_misses == 1, no
//     vote), then vote no once consecutive_misses >= 2.
func ApplyMissStreak(prev GuardState, passed bool, isConfirmation bool, stageHeight int64) MissOutcome {
	next := prev
	next.UpdatedStageHeight = stageHeight

	if passed {
		if isConfirmation {
			// Only a confirmation PoC pass is trusted: reset and mark capability.
			next.PassedOnce = true
			next.ConsecutiveMisses = 0
		}
		// Regular PoC pass: leave streak and passed_once untouched.
		return MissOutcome{VoteNo: false, NewState: next}
	}

	// Failure in either phase always accrues a miss.
	next.ConsecutiveMisses = prev.ConsecutiveMisses + 1

	if !prev.PassedOnce {
		// Never demonstrated capability via a CPoC pass yet; do not penalize.
		return MissOutcome{VoteNo: false, NewState: next}
	}

	// Demonstrated capability: one grace miss, then vote no.
	if next.ConsecutiveMisses <= 1 {
		return MissOutcome{VoteNo: false, NewState: next}
	}
	return MissOutcome{VoteNo: true, NewState: next}
}
