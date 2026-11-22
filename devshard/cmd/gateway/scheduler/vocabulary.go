package scheduler

// The strings this package puts on the wire, declared once and referenced by name. See routing.md, "Ghost burns".

// Why a nonce was burned instead of served, and the request a burn names when it was charged to none.
const (
	GhostReasonPoCUnavailable      = "poc_unavailable_host"
	GhostReasonWindowFull          = "participant_window_full_no_send"
	GhostReasonCutOff              = "participant_cut_off_no_send"
	GhostReasonEjected             = "participant_ejected_no_send"
	GhostReasonUntrusted           = "participant_untrusted"
	GhostReasonStateDiverged       = "participant_state_diverged_no_send"
	GhostReasonNoCompatibleRequest = "no_compatible_request_after_stale"
	GhostReasonAbandoned           = "request_abandoned_before_dispatch"
	GhostReasonPrepareFailed       = "nonce_spent_on_failed_prepare"
	GhostReasonAbsentFromChain     = "participant_absent_from_chain_no_send"
	GhostReasonProbeNoRequest      = "shadow_probe_no_request"
	GhostReasonProbeNoRoom         = "shadow_probe_no_room"
	NoRequestID                    = ""
)

// ProbeSkip is why a shadow participant's nonce is burned instead of probed; ProbeSkipNone is a probe that can be built. See routing.md, "Shadow participants".
type ProbeSkip int

const (
	ProbeSkipNone ProbeSkip = iota
	ProbeSkipNoRequest
	ProbeSkipNoRoom
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
