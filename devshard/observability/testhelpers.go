package observability

import "github.com/prometheus/client_golang/prometheus"

// RequestTerminalCounterForTest exposes a single label combination of the
// request_terminal_total CounterVec to tests in other packages. It is meant
// for assertions only and should not be used in production code.
func RequestTerminalCounterForTest(terminal Terminal, reason Reason) prometheus.Counter {
	ensureMetrics()
	return requestTerminalTotal.WithLabelValues(string(terminal), string(reason))
}
