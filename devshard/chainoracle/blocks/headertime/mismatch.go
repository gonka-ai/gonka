package headertime

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"devshard/logging"
)

// Shared mismatch counter for gateway and verifier (B5/B6). The last sample
// is the debug snapshot (B7). The counter is not on the default Prometheus
// registry: each process registers it on the registry its /metrics handler
// scrapes. Gateway and host are separate binaries, so each has its own count.
var (
	timeoutClockMismatchOnce sync.Once
	timeoutClockMismatch     *prometheus.CounterVec

	timeoutClockSampleMu sync.Mutex
	timeoutClockSample   ClockMismatchSample
	timeoutClockSampleOK bool
)

// ClockMismatchSample is the last wall/stamp disagreement. JSON fields match
// the structured warn and GET /v1/debug/timeout-clock.
type ClockMismatchSample struct {
	Escrow        string `json:"escrow"`
	Nonce         uint64 `json:"nonce"`
	Reason        string `json:"reason"`
	Side          string `json:"side"`
	WallDue       bool   `json:"wall_due"`
	StampDue      bool   `json:"stamp_due"`
	WallAnchor    int64  `json:"wall_anchor"`
	StampMs       int64  `json:"stamp_ms"`
	Height        uint64 `json:"height,omitempty"`
	MismatchCause string `json:"mismatch_cause,omitempty"`
}

// TimeoutClockSnapshot is the process-wide mismatch total and last sample.
type TimeoutClockSnapshot struct {
	MismatchTotal float64              `json:"mismatch_total"`
	LastMismatch  *ClockMismatchSample `json:"last_mismatch,omitempty"`
}

func timeoutClockMismatchCounter() *prometheus.CounterVec {
	timeoutClockMismatchOnce.Do(func() {
		timeoutClockMismatch = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_timeout_clock_mismatch_total",
			Help: "Legacy wall due and stamp due disagreed for a timeout check.",
		}, []string{"side", "kind"})
	})
	return timeoutClockMismatch
}

// RegisterTimeoutClockMismatch adds the mismatch counter to reg. The gateway
// registry and the host observability registry each call this once. A second
// call on the same registry is a no-op. RecordClockMismatch remains the only
// increment.
func RegisterTimeoutClockMismatch(reg prometheus.Registerer) {
	if reg == nil {
		return
	}
	err := reg.Register(timeoutClockMismatchCounter())
	if err == nil {
		return
	}
	if _, ok := err.(prometheus.AlreadyRegisteredError); ok {
		return
	}
	panic(err)
}

// TimeoutClockMismatchCounter exposes the counter for tests (promtestutil).
func TimeoutClockMismatchCounter() *prometheus.CounterVec {
	return timeoutClockMismatchCounter()
}

// TimeoutClockSnapshot returns the counter sum and the last recorded sample.
func TimeoutClockDebug() TimeoutClockSnapshot {
	snap := TimeoutClockSnapshot{MismatchTotal: timeoutClockMismatchTotal()}
	timeoutClockSampleMu.Lock()
	defer timeoutClockSampleMu.Unlock()
	if timeoutClockSampleOK {
		sample := timeoutClockSample
		snap.LastMismatch = &sample
	}
	return snap
}

func timeoutClockMismatchTotal() float64 {
	ch := make(chan prometheus.Metric, 8)
	go func() {
		timeoutClockMismatchCounter().Collect(ch)
		close(ch)
	}()
	var total float64
	for metric := range ch {
		var pb dto.Metric
		if metric.Write(&pb) != nil || pb.GetCounter() == nil {
			continue
		}
		total += pb.GetCounter().GetValue()
	}
	return total
}

// RecordClockMismatch keeps the last sample for the debug endpoint and emits
// a structured warn. A real due disagreement increments the Prometheus
// counter. A classified non-mismatch, such as stamp_missing, is logged once
// and does not increment. side is "gateway" or "verifier"; kind is "refused"
// or "execution".
func RecordClockMismatch(side, kind, escrow string, nonce uint64, eval DueEval, wallUnix, stampMs int64, height uint64) {
	if !eval.HasShadow || (!eval.Mismatch && eval.Cause == "") {
		return
	}
	if eval.Mismatch {
		timeoutClockMismatchCounter().WithLabelValues(side, kind).Inc()
	}
	sample := ClockMismatchSample{
		Escrow:        escrow,
		Nonce:         nonce,
		Reason:        kind,
		Side:          side,
		WallDue:       eval.Active.Due,
		StampDue:      eval.Shadow.Due,
		WallAnchor:    wallUnix,
		StampMs:       stampMs,
		Height:        height,
		MismatchCause: eval.Cause,
	}
	timeoutClockSampleMu.Lock()
	timeoutClockSample = sample
	timeoutClockSampleOK = true
	timeoutClockSampleMu.Unlock()
	logging.Warn("timeout clock mismatch",
		"subsystem", side,
		"side", side,
		"reason", kind,
		"escrow", escrow,
		"nonce", nonce,
		"wall_due", eval.Active.Due,
		"stamp_due", eval.Shadow.Due,
		"wall_anchor", wallUnix,
		"stamp_ms", stampMs,
		"height", height,
		"mismatch_cause", eval.Cause,
	)
}
