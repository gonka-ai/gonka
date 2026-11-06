package limits

// The named string vocabulary of this package: the words it puts on the wire and the ones its callers
// branch on. They are a contract with everything downstream, so they are declared here once and
// referenced by name. See README.md, "When a host stops taking work".

// Admission is why a host would or would not take one more request. See capacity.md, "The participant limiter: IOCW".
type Admission string

const (
	AdmissionOpen       Admission = "open"
	AdmissionWindowFull Admission = "window_full"
	AdmissionCutOff     Admission = "cut_off"
)

// CutoffState is how far a host's cut-off is open. See README.md, "When a host stops taking work".
type CutoffState string

const (
	CutoffClosed   CutoffState = "closed"
	CutoffOpen     CutoffState = "open"
	CutoffHalfOpen CutoffState = "half_open"
)

// AllCutoffStates lets metrics enumerate without restating them. See rules.md, "11. Labels, ordering and determinism".
func AllCutoffStates() []CutoffState {
	return []CutoffState{CutoffClosed, CutoffOpen, CutoffHalfOpen}
}

// Which trigger cut a host off.
const (
	cutoffReasonConsecutiveFaults = "consecutive_transport_faults"
	cutoffReasonProbeFailed       = "half_open_probe_failed"
)

// Which cap turned a request away, as the metric label and the log field name it.
const (
	RejectionConcurrentRequests = "concurrent_requests"
	RejectionInputTokens        = "input_tokens"
	RejectionQueueDepth         = "queue_depth"
	RejectionUnnamed            = "unnamed"
)
