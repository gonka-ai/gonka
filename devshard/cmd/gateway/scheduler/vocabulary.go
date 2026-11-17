package scheduler

// The strings this package puts on the wire, declared once and referenced by name. See routing.md, "Ghost burns".

// Why a nonce was burned instead of served.
const (
	GhostReasonPoCUnavailable      = "poc_unavailable_host"
	GhostReasonWindowFull          = "participant_window_full_no_send"
	GhostReasonCutOff              = "participant_cut_off_no_send"
	GhostReasonEjected             = "participant_ejected_no_send"
	GhostReasonOutsideAllowlist    = "participant_outside_allowlist"
	GhostReasonStateDiverged       = "participant_state_diverged_no_send"
	GhostReasonNoCompatibleRequest = "no_compatible_request_after_stale"
	GhostReasonAbandoned           = "request_abandoned_before_dispatch"
	GhostReasonPrepareFailed       = "nonce_spent_on_failed_prepare"
	GhostReasonAbsentFromChain     = "participant_absent_from_chain_no_send"
)

// ExhaustionReason is why routing will no longer pick an escrow, reported to the lifecycle that replaces it; ExhaustionNone is an escrow it may still pick, and the fallback nonce ceiling is never reported, so it never leaves this package. See routing.md, "Picking an escrow".
type ExhaustionReason string

const (
	ExhaustionNone                 ExhaustionReason = ""
	ExhaustionNonceCap             ExhaustionReason = "nonce_cap"
	ExhaustionBalanceFloor         ExhaustionReason = "balance_floor"
	ExhaustionInsufficientBalance  ExhaustionReason = "insufficient_balance"
	exhaustionFallbackNonceCeiling ExhaustionReason = "fallback_nonce_ceiling"
)
