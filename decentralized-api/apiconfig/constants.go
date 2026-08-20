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

	// NodeTestTimeoutSeconds bounds a single one-shot MLnode validation
	// (model load + health probe + inference probe, per model). It is the
	// same budget for the manual endpoint and for auto-test, so the
	// pre-PoC safety windows below can be derived from one number.
	NodeTestTimeoutSeconds int64 = 300

	// ManualTestMinSecondsBeforePoC is the minimum slack before the next
	// PoC for the manual test endpoint. A test stops the MLnode and can
	// run for up to NodeTestTimeoutSeconds, so allowing it any later than
	// OnlineAlertLeadSeconds + NodeTestTimeoutSeconds could leave the node
	// stopped inside the "must be online" window. Auto-test uses the much
	// larger AutoTestMinSecondsBeforePoC; this is the operator-discretion
	// floor, not a second opinion on the same question.
	ManualTestMinSecondsBeforePoC = OnlineAlertLeadSeconds + NodeTestTimeoutSeconds
)
