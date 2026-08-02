package accounting

import (
	"context"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

type Collector struct {
	tracker      *Tracker
	currentEpoch CurrentEpochFunc

	assigned     *prometheus.Desc
	disposition  *prometheus.Desc
	timeout      *prometheus.Desc
	missed       *prometheus.Desc
	invalid      *prometheus.Desc
	challenges   *prometheus.Desc
	inFlight     *prometheus.Desc
	unclassified *prometheus.Desc
	unknown      *prometheus.Desc
	writerErrors *prometheus.Desc
	crossCheck   *prometheus.Desc
}

func NewCollector(tracker *Tracker, currentEpoch CurrentEpochFunc) *Collector {
	return &Collector{
		tracker:      tracker,
		currentEpoch: currentEpoch,
		assigned: prometheus.NewDesc(
			"devshard_accounting_assigned_nonces",
			"Settlement-assigned nonces in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		disposition: prometheus.NewDesc(
			"devshard_accounting_disposition",
			"Terminal nonce dispositions in the current epoch.",
			[]string{"participant", "model", "disposition", "dispatch_phase", "timeout_evaluation_phase", "quarantine_mode", "no_send_reason", "failure_origin"}, nil,
		),
		timeout: prometheus.NewDesc(
			"devshard_accounting_timeout_outcome",
			"Required timeout outcomes in the current epoch.",
			[]string{"participant", "model", "timeout_kind", "timeout_outcome", "timeout_reason", "timeout_evaluation_phase", "failure_origin"}, nil,
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
		challenges: prometheus.NewDesc(
			"devshard_accounting_unresolved_challenges",
			"Unresolved protocol challenges in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		inFlight: prometheus.NewDesc(
			"devshard_accounting_in_flight",
			"Live sent nonces before finish or timeout in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		unclassified: prometheus.NewDesc(
			"devshard_accounting_unclassified",
			"Consumed nonces without a disposition or live attempt in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		unknown: prometheus.NewDesc(
			"devshard_accounting_unknown_reason_total",
			"Classified nonces carrying an unknown reason in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		writerErrors: prometheus.NewDesc(
			"devshard_accounting_writer_errors",
			"Accounting snapshot writer errors.",
			[]string{"participant", "model"}, nil,
		),
		crossCheck: prometheus.NewDesc(
			"devshard_accounting_cross_check_error",
			"Absolute protocol-to-gateway accounting cross-check difference.",
			[]string{"participant", "model"}, nil,
		),
	}
}

func NewPrometheusCollector(tracker *Tracker, currentEpoch CurrentEpochFunc) prometheus.Collector {
	return NewCollector(tracker, currentEpoch)
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{
		c.assigned, c.disposition, c.timeout, c.missed, c.invalid,
		c.challenges, c.inFlight, c.unclassified, c.unknown, c.writerErrors, c.crossCheck,
	} {
		ch <- desc
	}
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	if c == nil || c.tracker == nil || c.currentEpoch == nil {
		return
	}
	epoch, err := c.currentEpoch(context.Background())
	if err != nil {
		return
	}
	for _, record := range c.tracker.Query(QueryFilter{EpochIndex: epoch}) {
		base := []string{record.Participant, record.Model}
		gauge(ch, c.assigned, record.AssignedNonces, base...)
		gauge(ch, c.missed, record.ProtocolMisses, base...)
		gauge(ch, c.invalid, record.ProtocolInvalid, base...)
		gauge(ch, c.challenges, record.UnresolvedChallenges, base...)
		gauge(ch, c.inFlight, record.InFlight, base...)
		gauge(ch, c.unclassified, record.Unclassified, base...)
		gauge(ch, c.unknown, record.UnknownReasonTotal, base...)
		gauge(ch, c.writerErrors, record.WriterErrors, base...)
		gauge(ch, c.crossCheck, record.CrossChecks.ErrorCount, base...)

		dispositions := make(map[string]uint64)
		timeouts := make(map[string]uint64)
		for _, counter := range record.Counters {
			labels := []string{
				record.Participant, record.Model, string(counter.Key.Disposition),
				string(counter.Key.DispatchPhase), string(counter.Key.TimeoutEvaluationPhase),
				string(counter.Key.QuarantineMode), string(counter.Key.NoSendReason),
				string(counter.Key.FailureOrigin),
			}
			dispositions[strings.Join(labels, "\x00")] += counter.Count
			if counter.Key.TimeoutOutcome != "" {
				timeoutLabels := []string{
					record.Participant, record.Model, string(counter.Key.TimeoutKind),
					string(counter.Key.TimeoutOutcome), string(counter.Key.TimeoutReason),
					string(counter.Key.TimeoutEvaluationPhase), string(counter.Key.FailureOrigin),
				}
				timeouts[strings.Join(timeoutLabels, "\x00")] += counter.Count
			}
		}
		for labels, count := range dispositions {
			gauge(ch, c.disposition, count, strings.Split(labels, "\x00")...)
		}
		for labels, count := range timeouts {
			gauge(ch, c.timeout, count, strings.Split(labels, "\x00")...)
		}
	}
}

func gauge(ch chan<- prometheus.Metric, desc *prometheus.Desc, value uint64, labels ...string) {
	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(value), labels...)
}

var _ prometheus.Collector = (*Collector)(nil)
