package apiconfig

// Timing constants used by onboarding UX helpers in the admin layer.
// Kept in apiconfig (not admin) so other components — including the
// selfcheck mode — can reference the same defaults without importing
// the admin package.
const (
	// DefaultBlockTimeSeconds is the assumed average chain block time
	// used to translate block-distance to seconds for human-facing timing.
	DefaultBlockTimeSeconds = 6.0

	// AutoTestMinSecondsBeforePoC is the minimum slack required before
	// the next PoC to trigger a pre-PoC self-test on an MLnode. Below
	// this slack we avoid disrupting an MLnode that may need to load
	// models for the upcoming PoC.
	AutoTestMinSecondsBeforePoC int64 = 3600

	// OnlineAlertLeadSeconds is the lead time before the next PoC at
	// which we switch UX guidance from "safe to be offline" to
	// "must be online now".
	OnlineAlertLeadSeconds int64 = 600

	// AutoTestRetryBackoffSeconds is the minimum gap between auto-test
	// attempts after a transient (retryable) failure. Auto-test is
	// re-evaluated on every synced block, so this backoff prevents a
	// retry-storm while still letting a transient MLnode hiccup self-heal
	// without an operator manually re-testing or editing config.
	AutoTestRetryBackoffSeconds int64 = 300
)
