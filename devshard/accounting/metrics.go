package accounting

import (
	"context"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

type Collector struct {
	tracker      *Tracker
	currentEpoch CurrentEpochFunc

	assigned              *prometheus.Desc
	disposition           *prometheus.Desc
	timeout               *prometheus.Desc
	delivery              *prometheus.Desc
	missed                *prometheus.Desc
	invalid               *prometheus.Desc
	challenges            *prometheus.Desc
	inFlight              *prometheus.Desc
	inFlightRequests      *prometheus.Desc
	timeoutPending        *prometheus.Desc
	pendingClassification *prometheus.Desc
	unclassified          *prometheus.Desc
	overclassified        *prometheus.Desc
	unknown               *prometheus.Desc
	recordingErrors       *prometheus.Desc
	writerErrors          *prometheus.Desc
	crossCheck            *prometheus.Desc
	finding               *prometheus.Desc
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
		delivery: prometheus.NewDesc(
			"devshard_accounting_delivery",
			"Terminal nonces by what the host actually delivered, in the current epoch.",
			[]string{"participant", "model", "disposition", "delivery_reason", "dispatch_phase"}, nil,
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
		inFlightRequests: prometheus.NewDesc(
			"devshard_accounting_in_flight_requests",
			"Client requests this host is working on right now. A live nonce cannot answer this: a losing attempt's nonce stays open long after its client was served.",
			[]string{"participant", "model"}, nil,
		),
		timeoutPending: prometheus.NewDesc(
			"devshard_accounting_timeout_pending",
			"Deadline-reached unfinished nonces without a timeout outcome.",
			[]string{"participant", "model"}, nil,
		),
		pendingClassification: prometheus.NewDesc(
			"devshard_accounting_pending_classification",
			"Live nonces waiting for gateway classification.",
			[]string{"participant", "model"}, nil,
		),
		unclassified: prometheus.NewDesc(
			"devshard_accounting_unclassified",
			"Consumed nonces without a disposition or live attempt in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		overclassified: prometheus.NewDesc(
			"devshard_accounting_overclassified",
			"Classifications exceeding settlement-assigned nonces.",
			[]string{"participant", "model"}, nil,
		),
		unknown: prometheus.NewDesc(
			"devshard_accounting_unknown_reason_total",
			"Classified nonces carrying an unknown reason in the current epoch.",
			[]string{"participant", "model"}, nil,
		),
		writerErrors: prometheus.NewDesc(
			"devshard_accounting_writer_errors",
			"Gateway-wide accounting snapshot writer errors.",
			nil, nil,
		),
		recordingErrors: prometheus.NewDesc(
			"devshard_accounting_recording_errors",
			"Gateway-wide accounting event recording errors.",
			nil, nil,
		),
		crossCheck: prometheus.NewDesc(
			"devshard_accounting_cross_check_error",
			"Absolute protocol-to-gateway accounting cross-check difference.",
			[]string{"participant", "model"}, nil,
		),
		finding: prometheus.NewDesc(
			"devshard_accounting_finding",
			"Findings raised against a participant in the current epoch, one per code and severity.",
			[]string{"participant", "model", "code", "severity"}, nil,
		),
	}
}

func NewPrometheusCollector(tracker *Tracker, currentEpoch CurrentEpochFunc) prometheus.Collector {
	return NewCollector(tracker, currentEpoch)
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{
		c.assigned, c.disposition, c.delivery, c.timeout, c.missed, c.invalid,
		c.challenges, c.inFlight, c.inFlightRequests, c.timeoutPending, c.pendingClassification,
		c.unclassified, c.overclassified, c.unknown, c.recordingErrors,
		c.writerErrors, c.crossCheck, c.finding,
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
	recordingErrors, writerErrors := c.tracker.ErrorCounts()
	gauge(ch, c.recordingErrors, recordingErrors)
	gauge(ch, c.writerErrors, writerErrors)
	for _, record := range c.tracker.Query(QueryFilter{EpochIndex: epoch}) {
		base := []string{record.Participant, record.Model}
		gauge(ch, c.assigned, record.AssignedNonces, base...)
		gauge(ch, c.missed, record.ProtocolMisses, base...)
		gauge(ch, c.invalid, record.ProtocolInvalid, base...)
		gauge(ch, c.challenges, record.UnresolvedChallenges, base...)
		gauge(ch, c.inFlight, record.InFlight, base...)
		gauge(ch, c.inFlightRequests, record.InFlightRequests, base...)
		gauge(ch, c.timeoutPending, record.TimeoutPending, base...)
		gauge(ch, c.pendingClassification, record.PendingClassification, base...)
		gauge(ch, c.unclassified, record.Unclassified, base...)
		gauge(ch, c.overclassified, record.Overclassified, base...)
		gauge(ch, c.unknown, record.UnknownReasonTotal, base...)
		gauge(ch, c.crossCheck, record.CrossChecks.ErrorCount, base...)
		for _, finding := range record.Findings {
			gauge(ch, c.finding, 1, record.Participant, record.Model, finding.Code, string(finding.Severity))
		}

		dispositions := make(map[string]uint64)
		deliveries := make(map[string]uint64)
		timeouts := make(map[string]uint64)
		for _, counter := range record.Counters {
			labels := []string{
				record.Participant, record.Model, string(counter.Key.Disposition),
				string(counter.Key.DispatchPhase), string(counter.Key.TimeoutEvaluationPhase),
				string(counter.Key.QuarantineMode), string(counter.Key.NoSendReason),
				string(counter.Key.FailureOrigin),
			}
			dispositions[strings.Join(labels, "\x00")] += counter.Count
			if counter.Key.DeliveryReason != "" {
				deliveryLabels := []string{
					record.Participant, record.Model, string(counter.Key.Disposition),
					counter.Key.DeliveryReason, string(counter.Key.DispatchPhase),
				}
				deliveries[strings.Join(deliveryLabels, "\x00")] += counter.Count
			}
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
		for labels, count := range deliveries {
			gauge(ch, c.delivery, count, strings.Split(labels, "\x00")...)
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
