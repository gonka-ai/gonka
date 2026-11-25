package perf

// The strings this package puts on the wire. They are a contract with everything downstream, so they
// are declared here once and referenced by name. See README.md, "When a host stops taking work".

// Which trigger withheld a host from routing.
const (
	ejectionReasonConsecutiveFailures = "consecutive_failures"
	ejectionReasonFailureRate         = "failure_rate"
)
