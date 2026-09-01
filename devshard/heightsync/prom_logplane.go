package heightsync

import (
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// Host-side log-plane metric names (proposal §8.12). Scraped from devshardd.
const (
	MetricHeartbeatTurnsTotal   = "devshard_heightsync_heartbeat_turns_total"
	MetricHeartbeatSkippedTotal = "devshard_heightsync_heartbeat_skipped_total"
	MetricAckTotal              = "devshard_heightsync_ack_total"
	MetricAckRejectedTotal      = "devshard_heightsync_ack_rejected_total"
	MetricStaleStampTotal       = "devshard_heightsync_stale_stamp_total"
	MetricTurnState             = "devshard_heightsync_turn_state"
	MetricRepairProbesTotal     = "devshard_heightsync_repair_probes_total"
	MetricPeerSeenSlots         = "devshard_heightsync_peer_seen_slots"
	MetricCloseReadyArmed       = "devshard_heightsync_close_ready_armed"
	MetricMarksTotal            = "devshard_heightsync_marks_total"
)

var (
	logPlanePromMu sync.RWMutex
	// logPlaneReady is set after the first successful RegisterLogPlaneMetrics.
	// Inc*/Set* check it before taking logPlanePromMu so gateway processes
	// that never register pay nothing.
	logPlaneReady atomic.Bool

	heartbeatTurnsCounter   *prometheus.CounterVec
	heartbeatSkippedCounter *prometheus.CounterVec
	ackCounter              *prometheus.CounterVec
	ackRejectedCounter      *prometheus.CounterVec
	staleStampCounter       *prometheus.CounterVec
	turnStateGauge          *prometheus.GaugeVec
	repairProbesCounter     *prometheus.CounterVec
	peerSeenSlotsGauge      prometheus.Gauge
	closeReadyArmedGauge    prometheus.Gauge
	marksCounter            *prometheus.CounterVec
)

// RegisterLogPlaneMetrics registers host-side log-plane instruments on reg.
// Collectors are process-global (Inc* stays cheap); every Registerer passed
// in gets the same objects. AlreadyRegisteredError is ignored so in-process
// tests can build fresh registries without order dependence.
func RegisterLogPlaneMetrics(reg prometheus.Registerer) error {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	logPlanePromMu.Lock()
	defer logPlanePromMu.Unlock()
	if heartbeatTurnsCounter == nil {
		heartbeatTurnsCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricHeartbeatTurnsTotal,
			Help: "Heartbeat turns that reached a terminal state.",
		}, []string{"reason", "outcome"})
		heartbeatSkippedCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricHeartbeatSkippedTotal,
			Help: "Heartbeat due-checks that did not open a turn.",
		}, []string{"cause"})
		ackCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricAckTotal,
			Help: "MsgHeightAck records folded into a turn.",
		}, []string{"sync_state", "late"})
		ackRejectedCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricAckRejectedTotal,
			Help: "Diffs rejected by log-plane ack/heartbeat checks.",
		}, []string{"reason"})
		staleStampCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricStaleStampTotal,
			Help: "Stale stamps marked at admission (L5a). Always a mark, never a rejected diff.",
		}, []string{"tier"})
		turnStateGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: MetricTurnState,
			Help: "Latest heartbeat turn state (1 = current).",
		}, []string{"state"})
		repairProbesCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricRepairProbesTotal,
			Help: "Repair-probe outcomes and skips.",
		}, []string{"outcome"})
		peerSeenSlotsGauge = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: MetricPeerSeenSlots,
			Help: "Slots this host currently holds a fresh height claim for.",
		})
		closeReadyArmedGauge = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: MetricCloseReadyArmed,
			Help: "Whether this host is armed for USER_TIMEOUT (1 = armed).",
		})
		marksCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricMarksTotal,
			Help: "Attributable height-sync marks.",
		}, []string{"kind"})
	}

	for _, c := range []prometheus.Collector{
		heartbeatTurnsCounter,
		heartbeatSkippedCounter,
		ackCounter,
		ackRejectedCounter,
		staleStampCounter,
		turnStateGauge,
		repairProbesCounter,
		peerSeenSlotsGauge,
		closeReadyArmedGauge,
		marksCounter,
	} {
		if err := reg.Register(c); err != nil {
			if _, ok := err.(prometheus.AlreadyRegisteredError); ok {
				continue
			}
			return err
		}
	}
	logPlaneReady.Store(true)
	return nil
}

func logPlaneCounterVec(get func() *prometheus.CounterVec) *prometheus.CounterVec {
	if !logPlaneReady.Load() {
		return nil
	}
	logPlanePromMu.RLock()
	defer logPlanePromMu.RUnlock()
	return get()
}

// IncHeartbeatTurn counts a terminal turn (complete or degraded).
func IncHeartbeatTurn(reason, outcome string) {
	c := logPlaneCounterVec(func() *prometheus.CounterVec { return heartbeatTurnsCounter })
	if c == nil {
		return
	}
	c.WithLabelValues(heartbeatReasonLabel(reason), heartbeatOutcomeLabel(outcome)).Inc()
}

func heartbeatReasonLabel(reason string) string {
	switch HeartbeatReason(reason) {
	case ReasonHeightCadence, ReasonQuietSession, ReasonForced, ReasonCPoCBand, ReasonNoHeight, ReasonTurnTimeout:
		return reason
	case "":
		return "unknown"
	default:
		return "unknown"
	}
}

func heartbeatOutcomeLabel(outcome string) string {
	switch outcome {
	case "complete", "degraded":
		return outcome
	case "":
		return "unknown"
	default:
		return "unknown"
	}
}

// IncHeartbeatSkipped counts a due-check that did not open a turn.
func IncHeartbeatSkipped(cause string) {
	c := logPlaneCounterVec(func() *prometheus.CounterVec { return heartbeatSkippedCounter })
	if c == nil {
		return
	}
	if cause == "" {
		cause = "unknown"
	}
	c.WithLabelValues(cause).Inc()
}

// IncAck counts a folded MsgHeightAck.
func IncAck(syncState string, late bool) {
	c := logPlaneCounterVec(func() *prometheus.CounterVec { return ackCounter })
	if c == nil {
		return
	}
	if syncState == "" {
		syncState = "unspecified"
	}
	lateLabel := "false"
	if late {
		lateLabel = "true"
	}
	c.WithLabelValues(syncState, lateLabel).Inc()
}

// ObserveLogPlaneReject counts one log-plane INVALID that was acted on: a diff
// the verifier refused, or a tx the sequencer dropped from a compose set. Call
// it once per action, never inside CheckDiffLogPlane — the same diff is
// re-checked by the trial loop, by replay, and by catch-up.
func ObserveLogPlaneReject(reason string) {
	switch reason {
	case "ack_sig_invalid", "ack_causality", "bad_framing", "height_regression", "height_unbacked":
		IncAckRejected(reason)
	}
}

// IncAckRejected counts a log-plane INVALID on an ack/heartbeat check.
func IncAckRejected(reason string) {
	c := logPlaneCounterVec(func() *prometheus.CounterVec { return ackRejectedCounter })
	if c == nil {
		return
	}
	if reason == "" {
		reason = "unknown"
	}
	c.WithLabelValues(reason).Inc()
}

// IncStaleStamp counts an L5a admission mark.
func IncStaleStamp(tier string) {
	c := logPlaneCounterVec(func() *prometheus.CounterVec { return staleStampCounter })
	if c == nil {
		return
	}
	if tier == "" {
		tier = "l5a_admission"
	}
	c.WithLabelValues(tier).Inc()
}

// SetTurnState sets the latest-turn gauge. Only one state is 1.
func SetTurnState(state string) {
	if !logPlaneReady.Load() {
		return
	}
	logPlanePromMu.RLock()
	g := turnStateGauge
	logPlanePromMu.RUnlock()
	if g == nil {
		return
	}
	for _, s := range []string{"open", "complete", "degraded"} {
		v := 0.0
		if s == state {
			v = 1
		}
		g.WithLabelValues(s).Set(v)
	}
}

// IncRepairProbe counts a probe outcome or skip.
func IncRepairProbe(outcome string) {
	c := logPlaneCounterVec(func() *prometheus.CounterVec { return repairProbesCounter })
	if c == nil {
		return
	}
	if outcome == "" {
		outcome = "unknown"
	}
	c.WithLabelValues(repairProbeLabel(outcome)).Inc()
}

func repairProbeLabel(outcome string) string {
	switch outcome {
	case RepairOutcomeHeight, "height":
		return "height"
	case RepairOutcomeUnreachable, "unreachable":
		return "unreachable"
	case string(RepairSkipAckLanded):
		return "skipped_ack_landed"
	case string(RepairSkipBudget):
		return "budget_exhausted"
	default:
		return "unknown"
	}
}

// SetPeerSeenSlots sets the fresh-claim popcount gauge.
func SetPeerSeenSlots(n int) {
	if !logPlaneReady.Load() {
		return
	}
	logPlanePromMu.RLock()
	g := peerSeenSlotsGauge
	logPlanePromMu.RUnlock()
	if g == nil {
		return
	}
	g.Set(float64(n))
}

// SetCloseReadyArmed sets the host-side arming gauge (1 = armed).
func SetCloseReadyArmed(armed bool) {
	if !logPlaneReady.Load() {
		return
	}
	logPlanePromMu.RLock()
	g := closeReadyArmedGauge
	logPlanePromMu.RUnlock()
	if g == nil {
		return
	}
	if armed {
		g.Set(1)
		return
	}
	g.Set(0)
}

// IncMarks counts an attributable mark.
func IncMarks(kind string) {
	c := logPlaneCounterVec(func() *prometheus.CounterVec { return marksCounter })
	if c == nil {
		return
	}
	c.WithLabelValues(markKindLabel(kind)).Inc()
}

func markKindLabel(kind string) string {
	switch MarkKind(kind) {
	case MarkDisputeOriginator, MarkDisputeCarrier, MarkVectorContradiction,
		MarkDeferredFail, MarkAdmissionDelta, MarkFloorOutOfBand:
		return kind
	case "":
		return "unknown"
	default:
		return "unknown"
	}
}
