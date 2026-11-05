package scheduler

import (
	"errors"
	"fmt"
	"math"

	"devshard/cmd/gateway/chain"
	"devshard/types"
)

const (
	fallbackNonceCeiling     uint64 = 19_800
	nonceInFlightMargin      uint64 = 200
	nonceCeilingPerMaxMargin uint64 = 2
	unboundedAttemptsToFund         = 2
)

// avoidReason is why one round of a pick steps over an escrow; only one that could not pay opens the reserves.
type avoidReason int

const (
	avoidedNone avoidReason = iota
	avoidedOutOfFunds
	avoidedHostsBusy
	avoidedQueueFull
	avoidedRetired
	avoidedAdvanceFailed
)

type avoidedEscrows map[string]avoidReason

func (reason avoidReason) opensReserves() bool {
	return reason == avoidedOutOfFunds
}

func (reason avoidReason) secondAnswerStands(retryErr error) bool {
	switch reason {
	case avoidedRetired:
		return true
	case avoidedAdvanceFailed:
		return !errors.Is(retryErr, ErrNoEscrowCapacity)
	}
	return false
}

func (s *Scheduler) pickEscrow(profile RequestProfile, snapshot chain.PhaseSnapshot, queued *waiter, avoided avoidedEscrows) (Escrow, error) {
	candidates := s.escrows.Candidates(profile.Model)
	retirement, request := s.retirementReserve(profile.Model, snapshot), requestReserve(profile)

	if profile.Escrow != "" {
		for _, candidate := range candidates {
			if candidate.ID != profile.Escrow {
				continue
			}
			// A pinned escrow is capped too: the ceiling reserves room for the finalize and settlement. See routing.md, "Picking an escrow".
			ceiling := nonceCeilingReason(candidate, snapshot.MaxNonce)
			if ceiling == ExhaustionNonceCap {
				s.reportExhausted(candidate.ID, ceiling)
				if !escalationFits(profile, candidate, snapshot.MaxNonce) {
					return Escrow{}, noCapacity(ceiling)
				}
				ceiling = ExhaustionNone
			}
			if belowBalanceFloor(candidate, request) {
				return Escrow{}, pinnedEscrowShort()
			}
			if ceiling != ExhaustionNone {
				return Escrow{}, noCapacity(ceiling)
			}
			return candidate, nil
		}
		if retired, ok := s.retiredForEscalation(profile, snapshot.MaxNonce); ok {
			if belowBalanceFloor(retired, request) {
				return Escrow{}, pinnedEscrowShort()
			}
			return retired, nil
		}
		return Escrow{}, fmt.Errorf("escrow %q for model %q: %w", profile.Escrow, profile.Model, ErrEscrowGone)
	}
	// Read here as well as at dispatch: an escrow whose whole group it refuses can never serve.
	ranking := escrowRanking{
		profile: profile, snapshot: snapshot, queued: queued, avoided: avoided,
		retirement: retirement, request: request, attempts: s.attemptsToFund(),
		reachable: reachableByAllowlist(s.participantAllowlist(), s.unthrottledParticipants()),
		fleet:     s.fleetGates(profile.Model, snapshot),
		ahead:     s.queuedAhead(candidates),
	}
	regular := s.rankCandidates(candidates, ranking, false)
	picked, admitted, declined := regular.picked, regular.admitted, regular.declined
	moneyDeclined := regular.moneyDeclined
	if picked < 0 && regular.passedOverForHosts == 0 {
		reserve := s.rankCandidates(candidates, ranking, true)
		picked, admitted = reserve.picked, admitted+reserve.admitted
		moneyDeclined = moneyDeclined || reserve.moneyDeclined
		if reserve.declined != ExhaustionNone {
			declined = reserve.declined
		}
	}

	if admitted == 0 && len(candidates) > 0 {
		return Escrow{}, ErrAllowlistUnreachable
	}
	if picked < 0 {
		if moneyDeclined {
			s.reportMoneyShort(profile.Model)
		}
		return Escrow{}, noCapacity(declined)
	}
	return candidates[picked], nil
}

// escrowRanking is what one pick reads for every candidate it ranks.
type escrowRanking struct {
	profile    RequestProfile
	snapshot   chain.PhaseSnapshot
	queued     *waiter
	avoided    avoidedEscrows
	retirement uint64
	request    uint64
	attempts   int
	reachable  func(Escrow) bool
	fleet      availability
	ahead      []uint64
}

type rankedPick struct {
	picked             int
	admitted           int
	declined           ExhaustionReason
	passedOverForHosts int
	moneyDeclined      bool
}

type scoreTier struct {
	bestScore float64
	tied      []int
}

func newScoreTier() scoreTier { return scoreTier{bestScore: math.Inf(1)} }

func (tier *scoreTier) offer(index int, score float64) {
	switch {
	case score < tier.bestScore:
		tier.bestScore, tier.tied = score, append(tier.tied[:0], index)
	case score == tier.bestScore:
		tier.tied = append(tier.tied, index)
	}
}

// rankCandidates scores one tier, regulars or reserves, and counts the ones it passed over for their hosts. See routing.md, "A reserve escrow".
func (s *Scheduler) rankCandidates(candidates []Escrow, ranking escrowRanking, reserveTier bool) rankedPick {
	residue, funded, thin := newScoreTier(), newScoreTier(), newScoreTier()
	result := rankedPick{picked: -1}
	for index, candidate := range candidates {
		if candidate.IsReserve != reserveTier {
			continue
		}
		if !ranking.reachable(candidate) {
			result.passedOverForHosts++
			continue
		}
		result.admitted++
		if reason, avoided := ranking.avoided[candidate.ID]; avoided {
			if reason.opensReserves() {
				result.moneyDeclined = true
			} else {
				result.passedOverForHosts++
			}
			continue
		}
		if reason := exhaustionReason(candidate, ranking.snapshot.MaxNonce); reason != ExhaustionNone {
			result.declined = reason
			// Routing only declines; the rotation lifecycle is what replaces an exhausted escrow.
			s.reportExhausted(candidate.ID, reason)
			continue
		}
		if belowBalanceFloor(candidate, ranking.request) {
			result.declined = ExhaustionBalanceFloor
			result.moneyDeclined = true
			continue
		}
		weight := s.capacity.EscrowWeight(candidate.ID, ranking.profile.Model)
		if unusableWeight(weight) {
			result.passedOverForHosts++
			continue
		}
		forecast := expectedBurns(candidate, ranking.fleet.forEscrow(s.stateBlocked(candidate.ID), s.absentFromChain(candidate.ID), chatBudgetSpent(candidate)), ranking.queued, ranking.ahead[index])
		score := (float64(candidate.ActiveUsers+forecast) + float64(ranking.ahead[index])) / weight
		switch {
		case !reserveTier && !affordsAttempts(candidate, ranking.request, ranking.attempts):
			thin.offer(index, score)
		case !reserveTier && !affordsAttempts(candidate, ranking.retirement, ranking.attempts):
			residue.offer(index, score)
		default:
			funded.offer(index, score)
		}
	}

	tied := residue.tied
	if len(tied) == 0 {
		tied = funded.tied
	}
	if len(tied) == 0 {
		tied = thin.tied
	}
	switch len(tied) {
	case 0:
	case 1:
		result.picked = tied[0]
	default:
		result.picked = tied[int(uint64(s.tieBreak.Add(1)-1)%uint64(len(tied)))]
	}
	return result
}

// AttemptsToFund is how many attempts of one request an escrow is priced for: the configured cap, else two. See capacity.md, "The balance floor".
func AttemptsToFund(configured int64) int {
	if configured > 0 {
		return int(configured)
	}
	return unboundedAttemptsToFund
}

func (s *Scheduler) attemptsToFund() int {
	if s.settings == nil {
		return unboundedAttemptsToFund
	}
	return AttemptsToFund(s.settings.Load().Engine.MaxAttemptsPerRequest)
}

func (s *Scheduler) reportReserveTaken(escrowID string) {
	if s.onReserveTaken != nil {
		s.onReserveTaken(escrowID)
	}
}

func (s *Scheduler) reportMoneyShort(model string) {
	if s.onMoneyShort != nil {
		s.onMoneyShort(model)
	}
}

// reportExhausted passes over the fallback ceiling: it is not the hosts' cap, and a reported nonce cap retires the escrow. See routing.md, "Picking an escrow".
func (s *Scheduler) reportExhausted(escrowID string, reason ExhaustionReason) {
	if reason = retirable(reason); s.onEscrowExhausted == nil || reason == ExhaustionNone {
		return
	}
	s.onEscrowExhausted(escrowID, reason)
}

// Exhaustion is the reason routing would retire the escrow on, read without a request: the hosts' nonce cap or nothing. See routing.md, "Picking an escrow".
func (s *Scheduler) Exhaustion(candidate Escrow) ExhaustionReason {
	return retirable(exhaustionReason(candidate, s.snapshots.Snapshot().MaxNonce))
}

func retirable(reason ExhaustionReason) ExhaustionReason {
	if reason == exhaustionFallbackNonceCeiling {
		return ExhaustionNone
	}
	return reason
}

// noCapacity carries why the last candidate was declined, so running dry is not read as a model nobody serves.
func noCapacity(reason ExhaustionReason) error {
	if reason == ExhaustionBalanceFloor {
		return fmt.Errorf("%w: %w", ErrNoEscrowCapacity, types.ErrInsufficientBalance)
	}
	return ErrNoEscrowCapacity
}

// pinnedEscrowShort keeps every reader that classifies a pick by ErrNoEscrowCapacity or ErrInsufficientBalance answering as before.
func pinnedEscrowShort() error {
	return fmt.Errorf("%w: %w: %w", ErrPinnedEscrowShort, ErrNoEscrowCapacity, types.ErrInsufficientBalance)
}

// expectedBurns is how many nonces this escrow spends on nobody before one binds to a host that can take this request. See routing.md, "Pricing an escrow by the burns it will cost".
func expectedBurns(candidate Escrow, gates availability, queued *waiter, queuedAhead uint64) int {
	if candidate.Session == nil {
		return 0
	}
	slots := candidate.Session.SlotParticipants()
	if len(slots) == 0 {
		return 0
	}
	cursor := int((candidate.Session.LatestNonce() + 1 + queuedAhead) % uint64(len(slots)))
	for step := range slots {
		if gates.blocks(slots[(cursor+step)%len(slots)], queued) == blockNone {
			return step
		}
	}
	return len(slots)
}

// unusableWeight holds for a weight no ratio can be taken against. See routing.md, "Picking an escrow".
func unusableWeight(weight float64) bool {
	return weight <= 0 || math.IsNaN(weight)
}

// nonceCeilingReason names the ceiling an escrow has reached, and is ExhaustionNone below it.
func nonceCeilingReason(candidate Escrow, maxNonce uint64) ExhaustionReason {
	if candidate.Session == nil {
		return ExhaustionNone
	}
	cutoff, reason := fallbackNonceCeiling, exhaustionFallbackNonceCeiling
	if maxNonce > 0 {
		// Clamp, never wrap: a cap wrapping to 0 makes MaxActiveNonce return ^uint64(0), disabling the gate.
		if maxNonce > math.MaxUint32 {
			maxNonce = math.MaxUint32
		}
		cutoff = types.MaxActiveNonce(uint32(maxNonce), candidate.Session.GroupSize())
		cutoff -= min(nonceInFlightMargin, cutoff/nonceCeilingPerMaxMargin)
		reason = ExhaustionNonceCap
	}
	if candidate.Session.LatestNonce() < cutoff {
		return ExhaustionNone
	}
	return reason
}

// escalationNonceBound is the hosts' cap less half the in-flight margin, zero without a max_nonce read from the chain:
// escalations past the cutoff spend at most that half, and the races in flight keep the other for their timeout and validation diffs.
func escalationNonceBound(maxNonce uint64, groupSize int) uint64 {
	if maxNonce == 0 {
		return 0
	}
	hostsCap := types.MaxActiveNonce(uint32(min(maxNonce, math.MaxUint32)), groupSize)
	return hostsCap - min(nonceInFlightMargin, hostsCap/nonceCeilingPerMaxMargin)/2
}

// escalationFits lets a race's escalation past the cutoff on the escrow it already holds. See routing.md, "Picking an escrow".
func escalationFits(profile RequestProfile, candidate Escrow, maxNonce uint64) bool {
	return profile.Escalation && candidate.Session != nil &&
		candidate.Session.LatestNonce() < escalationNonceBound(maxNonce, candidate.Session.GroupSize())
}

func (s *Scheduler) retiredForEscalation(profile RequestProfile, maxNonce uint64) (Escrow, bool) {
	if !profile.Escalation {
		return Escrow{}, false
	}
	retired, ok := s.escrows.RetiredAtNonceCap(profile.Escrow)
	if !ok || retired.Model != profile.Model || !escalationFits(profile, retired, maxNonce) {
		return Escrow{}, false
	}
	return retired, true
}

// exhaustionReason is ExhaustionNone while the escrow may still be picked; a balance never retires an escrow. See routing.md, "Picking an escrow".
func exhaustionReason(candidate Escrow, maxNonce uint64) ExhaustionReason {
	return nonceCeilingReason(candidate, maxNonce)
}

// belowBalanceFloor prices each request the way the chain does, (input_length_bytes + max_tokens_cap) * token_price + fee_per_nonce.
func belowBalanceFloor(candidate Escrow, reserveTokens uint64) bool {
	return !affordsAttempts(candidate, reserveTokens, 1)
}

func affordsAttempts(candidate Escrow, reserveTokens uint64, attempts int) bool {
	if candidate.Session == nil || reserveTokens == 0 {
		return true
	}
	cost, ok := requestCost(candidate.Session, reserveTokens)
	if !ok {
		return false
	}
	floor, ok := safeMul(cost, uint64(attempts))
	if !ok {
		return false
	}
	return candidate.Session.Balance() >= floor
}

func requestCost(escrowSession session, reserveTokens uint64) (uint64, bool) {
	return RequestCost(reserveTokens, escrowSession.TokenPrice(), escrowSession.FeePerNonce())
}

// RequestCost is what the chain takes for one request: its reserve and the fee for the nonce it draws; unpriced when that overflows. See capacity.md, "The balance floor".
func RequestCost(reserveTokens, tokenPrice, feePerNonce uint64) (uint64, bool) {
	reserve, ok := safeMul(reserveTokens, tokenPrice)
	if !ok {
		return 0, false
	}
	cost := reserve + feePerNonce
	return cost, cost >= reserve
}

// safeMul reports the product only when it did not wrap: an unaffordable price must not read as a small one.
func safeMul(left, right uint64) (uint64, bool) {
	if left == 0 || right == 0 {
		return 0, true
	}
	product := left * right
	return product, product/left == right
}
