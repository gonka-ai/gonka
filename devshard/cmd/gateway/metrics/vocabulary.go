package metrics

// What one sweep tick did with the nonces it found.
const (
	sweepOutcomeApplied = "applied"
	sweepOutcomeFailed  = "failed"
)

// Why a store writer lost a row: shed on a full queue or a closed writer, or refused by the database.
const (
	lostCauseShed        = "shed"
	lostCauseWriteFailed = "write_failed"
)

// What a probe wave did.
const (
	HostPingTickStarted = "started"
	HostPingTickSkipped = "skipped"
)

// See README.md, "Race labels".
const (
	outcomeFailure = "failure"

	statusNoCode = "0"

	reasonNone             = "none"
	reasonNoAttempts       = "no_attempts"
	reasonEscrowMissing    = "escrow_missing"
	reasonBalanceExhausted = "balance_exhausted"
)
