package journal

// Kind is one lifecycle step the journal orders. See README.md, "Kinds".
type Kind uint8

const (
	KindRaceReported Kind = iota + 1
	KindTimeoutVote
	KindNonceBurned
	KindBurnBudgetExhausted
	KindDiffComposed
	KindWarmupProbe
	KindNonceStranded
	KindHostDiverged
	KindReplyNotCached
	KindNonceCommitted
	KindEscalationUnfilled
	KindAttemptCrowned
	KindAttemptFinished
	KindRequestFinished
	KindRequestThrottled
	KindHostTransition
	KindExcludedHostServed
	KindForcedSend
	KindEscrowTransition
	KindChainTransition
	KindFundingTransition
	KindWinnerFinishLanded
)

// kindCount is one past the last kind, so a table indexed by kind has a slot for each.
const kindCount = KindWinnerFinishLanded + 1

var kindNames = [kindCount]string{
	KindRaceReported:        "race_reported",
	KindTimeoutVote:         "timeout_vote",
	KindNonceBurned:         "nonce_burned",
	KindBurnBudgetExhausted: "burn_budget_exhausted",
	KindDiffComposed:        "diff_composed",
	KindWarmupProbe:         "warmup_probe",
	KindNonceStranded:       "nonce_stranded",
	KindHostDiverged:        "host_diverged",
	KindReplyNotCached:      "reply_not_cached",
	KindNonceCommitted:      "nonce_committed",
	KindEscalationUnfilled:  "escalation_unfilled",
	KindAttemptCrowned:      "attempt_crowned",
	KindAttemptFinished:     "attempt_finished",
	KindRequestFinished:     "request_finished",
	KindRequestThrottled:    "request_throttled",
	KindHostTransition:      "host_transition",
	KindExcludedHostServed:  "excluded_host_served",
	KindForcedSend:          "forced_send",
	KindEscrowTransition:    "escrow_transition",
	KindChainTransition:     "chain_transition",
	KindFundingTransition:   "funding_transition",
	KindWinnerFinishLanded:  "winner_finish_landed",
}

func (k Kind) String() string {
	if k == 0 || k >= kindCount {
		return "unknown"
	}
	return kindNames[k]
}

// onMoneyLane reports a kind refused only past the money ceiling. See README.md, "Two lanes".
func (k Kind) onMoneyLane() bool {
	switch k {
	case KindRaceReported, KindTimeoutVote, KindNonceBurned, KindBurnBudgetExhausted, KindDiffComposed,
		KindWarmupProbe, KindNonceStranded, KindHostDiverged, KindReplyNotCached, KindRequestFinished,
		KindHostTransition, KindExcludedHostServed, KindForcedSend, KindEscrowTransition, KindChainTransition, KindFundingTransition:
		return true
	}
	return false
}
