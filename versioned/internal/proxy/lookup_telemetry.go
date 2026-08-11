package proxy

import (
	"log/slog"
	"sync/atomic"
	"time"
)

// lookupFanoutErrors counts session-version lookup failures that fell back to
// fan-out. Exposed for tests and ops (also included in rate-limited warn logs).
var lookupFanoutErrors atomic.Uint64

// lookupWarnLastUnixNano gates warn spam when PG is persistently broken.
var lookupWarnLastUnixNano atomic.Int64

const lookupWarnInterval = 30 * time.Second

// LookupFanoutErrors returns how many times versionless obs degraded from PG
// lookup failure to fan-out since process start.
func LookupFanoutErrors() uint64 {
	return lookupFanoutErrors.Load()
}

func noteLookupFanout(escrowID string, err error) {
	n := lookupFanoutErrors.Add(1)
	now := time.Now().UnixNano()
	last := lookupWarnLastUnixNano.Load()
	if now-last < lookupWarnInterval.Nanoseconds() {
		return
	}
	if !lookupWarnLastUnixNano.CompareAndSwap(last, now) {
		return
	}
	slog.Warn("session version lookup failed; falling back to fan-out",
		"escrow_id", escrowID,
		"error", err,
		"fanout_errors_total", n,
	)
}

func resetLookupFanoutTelemetryForTest() {
	lookupFanoutErrors.Store(0)
	lookupWarnLastUnixNano.Store(0)
}
