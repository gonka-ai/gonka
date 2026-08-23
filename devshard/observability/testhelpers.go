package observability

import "github.com/prometheus/client_golang/prometheus"

// RequestTerminalCounterForTest exposes a single label combination of the
// request_terminal_total CounterVec to tests in other packages. It is meant
// for assertions only and should not be used in production code.
func RequestTerminalCounterForTest(terminal Terminal, reason Reason) prometheus.Counter {
	ensureMetrics()
	return requestTerminalTotal.WithLabelValues(string(terminal), string(reason))
}

// DiffForkDetectedForTest exposes the fork counter for unit tests.
func DiffForkDetectedForTest(escrowID string) prometheus.Counter {
	ensureMetrics()
	if escrowID == "" {
		escrowID = "unknown"
	}
	return diffForkDetectedTotal.WithLabelValues(escrowID)
}

// DiffPersistRetryForTest exposes the persist-retry counter for unit tests.
func DiffPersistRetryForTest(result string) prometheus.Counter {
	ensureMetrics()
	if result == "" {
		result = "unknown"
	}
	return diffPersistRetryTotal.WithLabelValues(result)
}
