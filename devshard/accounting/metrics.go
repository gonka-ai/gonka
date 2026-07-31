package accounting

import (
	"context"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

type Collector struct {
	book         *Book
	currentEpoch CurrentEpochFunc
	descs        []*prometheus.Desc

	assigned        *prometheus.Desc
	disposition     *prometheus.Desc
	timeout         *prometheus.Desc
	missed          *prometheus.Desc
	invalid         *prometheus.Desc
	recordedInvalid *prometheus.Desc
	challenges      *prometheus.Desc
	inFlight        *prometheus.Desc
	pending         *prometheus.Desc
	unclassified    *prometheus.Desc
	overclassified  *prometheus.Desc
	unknown         *prometheus.Desc
	recordingErrors *prometheus.Desc
	writerErrors    *prometheus.Desc
	crossCheck      *prometheus.Desc
	epochError      *prometheus.Desc
}

func NewCollector(book *Book, currentEpoch CurrentEpochFunc) *Collector {
	c := &Collector{
		book:         book,
		currentEpoch: currentEpoch,
		assigned: prometheus.NewDesc(
			"devshard_accounting_assigned_nonces",
			"Settlement-assigned nonces in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		disposition: prometheus.NewDesc(
			"devshard_accounting_disposition",
			"Current terminal nonce dispositions in the current epoch.",
			[]string{
				"participant", "model", "disposition", "dispatch_phase",
				"timeout_evaluation_phase", "quarantine_mode", "no_send_reason",
				"failure_origin",
			}, nil,
		),
		timeout: prometheus.NewDesc(
			"devshard_accounting_timeout_outcome",
			"Required timeout outcomes in the current epoch.",
			[]string{
				"participant", "model", "timeout_kind", "timeout_outcome",
				"timeout_reason", "timeout_evaluation_phase", "failure_origin",
			}, nil,
		),
		missed: prometheus.NewDesc(
			"devshard_accounting_protocol_misses",
			"Protocol HostStats missed count in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		invalid: prometheus.NewDesc(
			"devshard_accounting_protocol_invalid",
			"Protocol HostStats invalid count in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		recordedInvalid: prometheus.NewDesc(
			"devshard_accounting_recorded_invalid_transitions",
			"Invalid transitions observed by gateway accounting in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		challenges: prometheus.NewDesc(
			"devshard_accounting_unresolved_challenges",
			"Unresolved protocol challenges in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		inFlight: prometheus.NewDesc(
			"devshard_accounting_in_flight",
			"Live sent nonces before their protocol deadline in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		pending: prometheus.NewDesc(
			"devshard_accounting_pending_classification",
			"Live nonces waiting for their gateway disposition.",
			[]string{"participant", "model"}, nil,
		),
		unclassified: prometheus.NewDesc(
			"devshard_accounting_unclassified",
			"Consumed nonces without a disposition or live attempt in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		overclassified: prometheus.NewDesc(
			"devshard_accounting_overclassified",
			"Nonce classifications exceeding settlement assignments.",
			[]string{"participant", "model"}, nil,
		),
		unknown: prometheus.NewDesc(
			"devshard_accounting_unknown_reason_total",
			"Classified nonces carrying an unknown reason in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		recordingErrors: prometheus.NewDesc(
			"devshard_accounting_recording_errors",
			"Accounting events rejected by the in-memory book.",
			nil, nil,
		),
		writerErrors: prometheus.NewDesc(
			"devshard_accounting_writer_errors",
			"Accounting snapshot writer errors observed by the book.",
			nil, nil,
		),
		crossCheck: prometheus.NewDesc(
			"devshard_accounting_cross_check_error",
			"Absolute protocol-to-gateway accounting cross-check difference.",
			[]string{"participant", "model"}, nil,
		),
		epochError: prometheus.NewDesc(
			"devshard_accounting_current_epoch_error",
			"Whether the current chain epoch was unavailable during collection.",
			nil, nil,
		),
	}
	c.descs = []*prometheus.Desc{
		c.assigned,
		c.disposition,
		c.timeout,
		c.missed,
		c.invalid,
		c.recordedInvalid,
		c.challenges,
		c.inFlight,
		c.pending,
		c.unclassified,
		c.overclassified,
		c.unknown,
		c.recordingErrors,
		c.writerErrors,
		c.crossCheck,
		c.epochError,
	}
	return c
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range c.descs {
		ch <- desc
	}
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	if c == nil || c.book == nil || c.currentEpoch == nil {
		return
	}
	emitGauge(ch, c.recordingErrors, c.book.EventErrors())
	emitGauge(ch, c.writerErrors, c.book.WriterErrors())
	epoch, err := c.currentEpoch(context.Background())
	if err != nil {
		emitGauge(ch, c.epochError, 1)
		return
	}
	emitGauge(ch, c.epochError, 0)
	for _, record := range c.book.Query(QueryFilter{EpochIndex: epoch}) {
		base := []string{record.Participant, record.Model}
		emitGauge(ch, c.assigned, record.AssignedNonces, base...)
		emitGauge(ch, c.missed, record.ProtocolMisses, base...)
		emitGauge(ch, c.invalid, record.ProtocolInvalid, base...)
		emitGauge(ch, c.recordedInvalid, record.CrossChecks.RecordedInvalid, base...)
		emitGauge(ch, c.challenges, record.UnresolvedChallenges, base...)
		emitGauge(ch, c.inFlight, record.InFlight, base...)
		emitGauge(ch, c.pending, record.PendingClassification, base...)
		emitGauge(ch, c.unclassified, record.Unclassified, base...)
		emitGauge(ch, c.overclassified, record.Overclassified, base...)
		emitGauge(ch, c.unknown, record.UnknownReasonTotal, base...)
		emitGauge(ch, c.crossCheck, record.CrossChecks.ErrorCount, base...)

		dispositions := make(map[string]uint64)
		timeouts := make(map[string]uint64)
		for _, counter := range record.Counters {
			dispositionLabels := []string{
				record.Participant,
				record.Model,
				string(counter.Disposition),
				string(counter.DispatchPhase),
				string(counter.TimeoutEvaluationPhase),
				string(counter.QuarantineMode),
				string(counter.NoSendReason),
				string(counter.FailureOrigin),
			}
			dispositions[strings.Join(dispositionLabels, "\x00")] += counter.Count
			if counter.TimeoutOutcome != "" {
				timeoutLabels := []string{
					record.Participant,
					record.Model,
					string(counter.TimeoutKind),
					string(counter.TimeoutOutcome),
					string(counter.TimeoutReason),
					string(counter.TimeoutEvaluationPhase),
					string(counter.FailureOrigin),
				}
				timeouts[strings.Join(timeoutLabels, "\x00")] += counter.Count
			}
		}
		for labels, count := range dispositions {
			emitGauge(ch, c.disposition, count, strings.Split(labels, "\x00")...)
		}
		for labels, count := range timeouts {
			emitGauge(ch, c.timeout, count, strings.Split(labels, "\x00")...)
		}
	}
}

func emitGauge(ch chan<- prometheus.Metric, desc *prometheus.Desc, value uint64, labels ...string) {
	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(value), labels...)
}

var _ prometheus.Collector = (*Collector)(nil)
