package metrics

import (
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"devshard/cmd/gateway/engine"
)

// See README.md, "Histogram buckets".
const (
	bucketGrowthFactor         = 2
	latencyBucketStartSeconds  = 0.01
	latencyBucketCount         = 19
	chunkGapBucketStartSeconds = 0.005
	chunkGapBucketCount        = 15
	prefillBucketStartSeconds  = 0.0001
	prefillBucketCount         = 12
)

// See README.md, "Histogram buckets".
var (
	latencyBuckets  = prometheus.ExponentialBuckets(latencyBucketStartSeconds, bucketGrowthFactor, latencyBucketCount)
	chunkGapBuckets = prometheus.ExponentialBuckets(chunkGapBucketStartSeconds, bucketGrowthFactor, chunkGapBucketCount)
)

// RaceRecorder satisfies the engine's metrics hook. See operations.md, "Metrics".
type RaceRecorder struct {
	attemptsStarted  *prometheus.CounterVec
	attemptsTerminal *prometheus.CounterVec
	attemptFailures  *prometheus.CounterVec
	transportErrors  *prometheus.CounterVec
	missedDeadlines  *prometheus.CounterVec
	requests         *prometheus.CounterVec
	hiddenFailures   *prometheus.CounterVec
	sweeps           *prometheus.CounterVec
	timeoutActions   *prometheus.CounterVec
	missRejects      *prometheus.CounterVec
	carryOverflows   *prometheus.CounterVec

	receiptSeconds  *prometheus.HistogramVec
	firstContent    *prometheus.HistogramVec
	prefillPerToken *prometheus.HistogramVec
	outputTokens    *prometheus.CounterVec
	totalAttempt    *prometheus.HistogramVec
	maxChunkGap     *prometheus.HistogramVec
	meanChunkGap    *prometheus.HistogramVec

	now                 func() time.Time
	staleness           func() time.Duration
	participantFamilies []partialDeleter

	agingMu   sync.Mutex
	lastSeen  map[hostSeries]time.Time
	lastSweep time.Time
}

// hostSeries is the label pair every participant family carries, and the unit a sweep forgets.
type hostSeries struct {
	participant string
	model       string
}

// partialDeleter is what a CounterVec and a HistogramVec share for forgetting one label pair.
type partialDeleter interface {
	DeletePartialMatch(labels prometheus.Labels) int
}

// NewRaceRecorder forgets a participant and model left unwritten for staleness(). See README.md, "Cardinality in practice".
func NewRaceRecorder(telemetry *Metrics, now func() time.Time, staleness func() time.Duration) *RaceRecorder {
	recorder := &RaceRecorder{
		now:       now,
		staleness: staleness,
		lastSeen:  make(map[hostSeries]time.Time),
		attemptsStarted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_attempts_started_total",
			Help: "Total gateway attempts dispatched by participant, model, role, and start reason.",
		}, []string{"participant_key", "model", "role", "reason"}),
		attemptsTerminal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_attempts_terminal_total",
			Help: "Total gateway attempts by terminal outcome and visibility.",
		}, []string{"participant_key", "model", "role", "outcome", "visibility"}),
		attemptFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_attempt_failures_total",
			Help: "Total failed gateway attempts by bounded failure reason and visibility.",
		}, []string{"participant_key", "model", "role", "reason", "visibility"}),
		transportErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_participant_transport_errors_total",
			Help: "Total participant-bound request errors by participant, model, and upstream status.",
		}, []string{"participant_key", "model", "status"}),
		missedDeadlines: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_participant_missed_deadlines_total",
			Help: "Total receipt and first-token deadlines a participant missed on a model, each of which narrowed its window.",
		}, []string{"participant_key", "model", "deadline"}),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_requests_total",
			Help: "Total gateway chat requests by model, user-visible outcome, and bounded reason.",
		}, []string{"model", "outcome", "reason"}),
		hiddenFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_user_requests_with_hidden_failure_total",
			Help: "Total successful user requests that hid a gateway-visible attempt failure.",
		}, []string{"model", "reason"}),
		sweeps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_timeout_sweep_total",
			Help: "Total execution-timeout votes the escrow tick's sweep applied and failed to apply.",
		}, []string{"outcome"}),
		timeoutActions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_timeout_actions_total",
			Help: "Total nonce timeout-vote actions by participant, model, kind, action, and reason.",
		}, []string{"participant_key", "model", "kind", "action", "reason"}),
		missRejects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_error_miss_verify_rejects_total",
			Help: "Total verifier rejections of an error-miss claim by cause and how whole the offered proof was.",
		}, []string{"cause", "completeness"}),
		carryOverflows: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_stream_carry_overflow_total",
			Help: "Total SSE reassembly buffers that overflowed their carry budget.",
		}, []string{"participant_key", "model"}),
		receiptSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "devshard_gateway_participant_receipt_seconds",
			Help:    "Time from inference send until receipt confirmation by participant and model.",
			Buckets: latencyBuckets,
		}, []string{"participant_key", "model"}),
		firstContent: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "devshard_gateway_participant_first_content_seconds",
			Help:    "Time from inference send until first content by participant and model.",
			Buckets: latencyBuckets,
		}, []string{"participant_key", "model"}),
		prefillPerToken: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "devshard_gateway_participant_prefill_seconds_per_input_token",
			Help:    "Receipt-to-first-content time divided by input tokens, by participant and model.",
			Buckets: prometheus.ExponentialBuckets(prefillBucketStartSeconds, bucketGrowthFactor, prefillBucketCount),
		}, []string{"participant_key", "model"}),
		outputTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_participant_output_tokens_total",
			Help: "Output tokens a participant generated on a model: the host's usage completion_tokens, else the logprob entries it streamed.",
		}, []string{"participant_key", "model"}),
		maxChunkGap: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "devshard_gateway_participant_max_inter_chunk_seconds",
			Help:    "Longest silence between two streamed chunks within one attempt, by participant and model.",
			Buckets: chunkGapBuckets,
		}, []string{"participant_key", "model"}),
		meanChunkGap: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "devshard_gateway_participant_inter_chunk_seconds",
			Help:    "Mean silence between streamed chunks within one attempt, by participant and model.",
			Buckets: chunkGapBuckets,
		}, []string{"participant_key", "model"}),
		totalAttempt: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "devshard_gateway_participant_total_attempt_seconds",
			Help:    "Total inference attempt time by participant and model.",
			Buckets: latencyBuckets,
		}, []string{"participant_key", "model"}),
	}
	recorder.participantFamilies = []partialDeleter{
		recorder.attemptsStarted, recorder.attemptsTerminal, recorder.attemptFailures, recorder.transportErrors,
		recorder.missedDeadlines, recorder.timeoutActions, recorder.missRejects, recorder.carryOverflows, recorder.receiptSeconds, recorder.firstContent,
		recorder.prefillPerToken, recorder.outputTokens, recorder.totalAttempt, recorder.maxChunkGap, recorder.meanChunkGap,
	}
	telemetry.Register(recorder.collectors()...)
	return recorder
}

func (r *RaceRecorder) collectors() []prometheus.Collector {
	return []prometheus.Collector{
		r.attemptsStarted, r.attemptsTerminal, r.attemptFailures, r.transportErrors, r.missedDeadlines, r.requests,
		r.hiddenFailures, r.timeoutActions, r.missRejects, r.carryOverflows, r.sweeps,
		r.receiptSeconds, r.firstContent, r.prefillPerToken, r.outputTokens, r.totalAttempt,
		r.maxChunkGap, r.meanChunkGap,
	}
}

// RecordSweep counts what one tick of the execution-timeout sweep applied and failed; a tick that found nothing moves no series.
func (r *RaceRecorder) RecordSweep(due, applied, failed int) {
	if due <= 0 {
		return
	}
	r.sweeps.WithLabelValues(sweepOutcomeApplied).Add(float64(applied))
	r.sweeps.WithLabelValues(sweepOutcomeFailed).Add(float64(failed))
}

func (r *RaceRecorder) RecordRace(outcome engine.RaceOutcome) {
	model := metricLabel(outcome.Model, labelUnknown)
	firstFailure := ""
	for _, attempt := range outcome.Attempts {
		labels := outcome.Labels(attempt)
		participant := metricLabel(labels.Participant, labelUnknown)
		role := metricLabel(labels.Role, labelUnknown)
		r.markSeen(participant, model)

		if !attempt.SendTime.IsZero() {
			r.attemptsStarted.WithLabelValues(participant, model, role, metricLabel(attempt.StartReason, role)).Inc()
		}
		if !outcome.FinishAwaited(attempt) {
			if reason, failed := r.countTerminal(participant, model, role, labels, attempt); failed && firstFailure == "" {
				firstFailure = reason
			}
		}
		r.countMissedDeadlines(participant, model, attempt)
		r.observeAttemptLatency(participant, model, outcome.InputTokens, attempt)
	}
	r.recordRequest(model, outcome, firstFailure)
}

// RecordAwaitedFinish counts the terminal RecordRace left to the winner's finish; the request's hidden failure is its own only when no other attempt failed.
func (r *RaceRecorder) RecordAwaitedFinish(outcome engine.RaceOutcome, attempt engine.AttemptOutcome) {
	model := metricLabel(outcome.Model, labelUnknown)
	labels := outcome.Labels(attempt)
	participant := metricLabel(labels.Participant, labelUnknown)
	r.markSeen(participant, model)
	reason, failed := r.countTerminal(participant, model, metricLabel(labels.Role, labelUnknown), labels, attempt)
	if failed && outcome.Succeeded && !otherAttemptFailed(outcome) {
		r.hiddenFailures.WithLabelValues(model, reason).Inc()
	}
}

func (r *RaceRecorder) countTerminal(participant, model, role string, labels engine.AttemptLabels, attempt engine.AttemptOutcome) (string, bool) {
	r.attemptsTerminal.WithLabelValues(participant, model, role, labels.Outcome, labels.Visibility).Inc()
	if labels.Outcome != engine.AttemptOutcomeFailed {
		return "", false
	}
	reason := metricLabel(labels.Reason, labelUnknown)
	r.attemptFailures.WithLabelValues(participant, model, role, reason, labels.Visibility).Inc()
	if status, upstream := transportStatus(attempt); upstream {
		r.transportErrors.WithLabelValues(participant, model, status).Inc()
	}
	return reason, true
}

func otherAttemptFailed(outcome engine.RaceOutcome) bool {
	for _, attempt := range outcome.Attempts {
		if !outcome.FinishAwaited(attempt) && outcome.Labels(attempt).Outcome == engine.AttemptOutcomeFailed {
			return true
		}
	}
	return false
}

func (r *RaceRecorder) recordRequest(model string, outcome engine.RaceOutcome, firstFailure string) {
	if outcome.Succeeded {
		r.requests.WithLabelValues(model, engine.AttemptOutcomeSuccess, reasonNone).Inc()
		if firstFailure != "" {
			r.hiddenFailures.WithLabelValues(model, firstFailure).Inc()
		}
		return
	}
	r.requests.WithLabelValues(model, outcomeFailure, raceFailureReason(outcome, firstFailure)).Inc()
}

func raceFailureReason(outcome engine.RaceOutcome, firstFailure string) string {
	switch {
	case outcome.Lifecycle.EscrowMissing:
		return reasonEscrowMissing
	case outcome.Lifecycle.BalanceExhausted:
		return reasonBalanceExhausted
	case outcome.Lifecycle.ClientGone:
		return engine.ReasonClientCancelled
	case len(outcome.Attempts) == 0:
		return reasonNoAttempts
	}
	return metricLabel(firstFailure, labelUnknown)
}

// transportStatus maps an attempt back to the host's upstream status. See operations.md, "Cardinality rules".
func transportStatus(attempt engine.AttemptOutcome) (string, bool) {
	if status, recovered := engine.StatusFor(attempt.Terminal); recovered {
		return strconv.Itoa(status), true
	}
	switch attempt.Terminal {
	case engine.TerminalUpstreamServerError:
		if attempt.UpstreamStatus > 0 {
			return strconv.Itoa(attempt.UpstreamStatus), true
		}
		return statusNoCode, true
	case engine.TerminalRejected, engine.TerminalDialFailure, engine.TerminalStreamTruncated,
		engine.TerminalUnexpectedEOF, engine.TerminalStalled, engine.TerminalResponseTooLarge:
		return statusNoCode, true
	}
	return "", false
}

func (r *RaceRecorder) countMissedDeadlines(participant, model string, attempt engine.AttemptOutcome) {
	if attempt.ReceiptDeadlineMissed {
		r.missedDeadlines.WithLabelValues(participant, model, engine.EscalationReasonReceipt).Inc()
	}
	if attempt.FirstTokenDeadlineMissed {
		r.missedDeadlines.WithLabelValues(participant, model, engine.EscalationReasonFirstToken).Inc()
	}
}

func (r *RaceRecorder) observeAttemptLatency(participant, model string, inputTokens uint64, attempt engine.AttemptOutcome) {
	if produced := attempt.OutputTokens(); produced > 0 {
		r.outputTokens.WithLabelValues(participant, model).Add(float64(produced))
	}
	if seconds := elapsedSeconds(attempt.SendTime, attempt.ReceiptTime); seconds > 0 {
		r.receiptSeconds.WithLabelValues(participant, model).Observe(seconds)
	}
	if seconds := elapsedSeconds(attempt.SendTime, attempt.FirstContent); seconds > 0 {
		r.firstContent.WithLabelValues(participant, model).Observe(seconds)
	}
	if prefill := elapsedSeconds(attempt.ReceiptTime, attempt.FirstContent); prefill > 0 && inputTokens > 0 {
		r.prefillPerToken.WithLabelValues(participant, model).Observe(prefill / float64(inputTokens))
	}
	if attempt.MaxChunkGap > 0 {
		r.maxChunkGap.WithLabelValues(participant, model).Observe(attempt.MaxChunkGap.Seconds())
		r.meanChunkGap.WithLabelValues(participant, model).Observe(attempt.MeanChunkGap.Seconds())
	}
	if seconds := elapsedSeconds(attempt.SendTime, attempt.Completed); seconds > 0 {
		r.totalAttempt.WithLabelValues(participant, model).Observe(seconds)
	}
}

func (r *RaceRecorder) RecordTimeout(event engine.TimeoutEvent) {
	participant := metricLabel(event.Participant, labelUnknown)
	model := metricLabel(event.Model, labelUnknown)
	r.markSeen(participant, model)
	r.timeoutActions.WithLabelValues(
		participant, model,
		metricLabel(event.Kind, labelUnknown),
		metricLabel(event.Action, labelUnknown),
		metricLabel(event.Reason, reasonNone),
	).Inc()
	completeness := metricLabel(string(event.Completeness), labelUnknown)
	for _, cause := range event.VerifyRejects {
		r.missRejects.WithLabelValues(metricLabel(cause, labelUnknown), completeness).Inc()
	}
}

func (r *RaceRecorder) RecordClassifyOverflow(participant, model string) {
	participantLabel, modelLabel := metricLabel(participant, labelUnknown), metricLabel(model, labelUnknown)
	r.markSeen(participantLabel, modelLabel)
	r.carryOverflows.WithLabelValues(participantLabel, modelLabel).Inc()
}

// See README.md, "Cardinality in practice".
func (r *RaceRecorder) markSeen(participant, model string) {
	r.agingMu.Lock()
	defer r.agingMu.Unlock()
	now := r.now()
	r.lastSeen[hostSeries{participant: participant, model: model}] = now
	staleness := r.staleness()
	if staleness <= 0 || now.Sub(r.lastSweep) < staleness/10 {
		return
	}
	r.lastSweep = now
	for series, seen := range r.lastSeen {
		if now.Sub(seen) > staleness {
			delete(r.lastSeen, series)
			r.forget(series)
		}
	}
}

func (r *RaceRecorder) forget(series hostSeries) {
	forgotten := prometheus.Labels{"participant_key": series.participant, "model": series.model}
	for _, family := range r.participantFamilies {
		family.DeletePartialMatch(forgotten)
	}
}
