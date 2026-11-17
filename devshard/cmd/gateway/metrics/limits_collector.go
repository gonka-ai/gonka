package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"devshard/cmd/gateway/limits"
)

var cutoffStates = limits.AllCutoffStates()

type LimiterSource interface {
	Snapshot() limits.LimiterSnapshot
}

type CapacitySource interface {
	ModelWeights(model string) limits.ModelWeights
}

type ParticipantSource interface {
	Snapshot() []limits.HostWindow
}

// LimitsSources is everything the limits collector reads; Models covers those no request has touched yet.
type LimitsSources struct {
	Limiter       LimiterSource
	Capacity      CapacitySource
	Participants  ParticipantSource
	Models        func() []string
	BufferedBytes func() int64
	OwedVotes     func() int64
}

type LimitsCollector struct {
	sources LimitsSources

	inflightRequests             *prometheus.Desc
	bufferedResponseBytes        *prometheus.Desc
	owedTimeoutVotes             *prometheus.Desc
	inflightInputTokens          *prometheus.Desc
	effectiveMaxConcurrent       *prometheus.Desc
	effectiveMaxTokens           *prometheus.Desc
	enforcedMaxConcurrentByModel *prometheus.Desc
	enforcedMaxTokensByModel     *prometheus.Desc
	inflightRequestsByModel      *prometheus.Desc
	inflightInputTokensByModel   *prometheus.Desc
	queueDepth                   *prometheus.Desc
	capacityScale                *prometheus.Desc
	capacityTotalWeight          *prometheus.Desc
	capacityBaselineWeight       *prometheus.Desc
	capacityWeightsUnobserved    *prometheus.Desc
	participantsTracked          *prometheus.Desc
	participantsExhausted        *prometheus.Desc
	cutoffState                  *prometheus.Desc
}

func NewLimitsCollector(sources LimitsSources) *LimitsCollector {
	return &LimitsCollector{
		sources:                sources,
		inflightRequests:       gaugeDesc("devshard_gateway_inflight_requests", "Current in-flight requests tracked by the gateway limiter."),
		bufferedResponseBytes:  gaugeDesc("devshard_gateway_buffered_response_bytes", "Bytes held for non-streaming replies being assembled, across every request at once."),
		owedTimeoutVotes:       gaugeDesc("devshard_gateway_owed_timeout_votes", "Timeout votes taken from finished races and not yet posted, waiting out a protocol deadline or a free poster."),
		inflightInputTokens:    gaugeDesc("devshard_gateway_inflight_input_tokens", "Current in-flight input tokens tracked by the gateway limiter."),
		effectiveMaxConcurrent: gaugeDesc("devshard_gateway_effective_max_concurrent_requests", "Configured concurrent-request cap, before per-model overrides and capacity scaling."),
		effectiveMaxTokens:     gaugeDesc("devshard_gateway_effective_max_input_tokens_in_flight", "Configured input-token cap, before per-model overrides and capacity scaling."),

		enforcedMaxConcurrentByModel: gaugeDesc("devshard_gateway_enforced_max_concurrent_requests_by_model", "Concurrent-request cap a model's requests are judged against, after its overrides and capacity scaling.", "model"),
		enforcedMaxTokensByModel:     gaugeDesc("devshard_gateway_enforced_max_input_tokens_by_model", "Input-token cap a model's requests are judged against, after its overrides and capacity scaling.", "model"),

		inflightRequestsByModel:    gaugeDesc("devshard_gateway_inflight_requests_by_model", "Current in-flight requests per model.", "model"),
		inflightInputTokensByModel: gaugeDesc("devshard_gateway_inflight_input_tokens_by_model", "Current in-flight input tokens per model.", "model"),
		queueDepth:                 gaugeDesc("devshard_gateway_limiter_queue_depth", "Requests waiting for a free slot, per model.", "model"),
		capacityScale:              gaugeDesc("devshard_gateway_capacity_scale_by_model", "Ratio of current to baseline host weight per model (1.0 = full capacity).", "model"),
		capacityTotalWeight:        gaugeDesc("devshard_gateway_capacity_total_weight_by_model", "Current availability-filtered host weight per model.", "model"),
		capacityBaselineWeight:     gaugeDesc("devshard_gateway_capacity_baseline_weight_by_model", "Baseline steady-state host weight per model.", "model"),
		capacityWeightsUnobserved:  gaugeDesc("devshard_gateway_capacity_weights_unobserved_by_model", "1 while escrow scoring for a model runs on the membership-share fallback because the chain has reported no weights.", "model"),

		participantsTracked:   gaugeDesc("devshard_gateway_participants_tracked", "Participant/model pairs the per-host limiter is tracking."),
		participantsExhausted: gaugeDesc("devshard_gateway_participants_exhausted", "Tracked participant/model pairs that would currently refuse an attempt."),
		cutoffState:           gaugeDesc("devshard_gateway_participant_breaker_state", "Whether a host is currently cut off after repeated faults it never answered, or a failed half-open probe (1 for the current state).", "participant_key", "model", "state"),
	}
}

func (c *LimitsCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{
		c.inflightRequests, c.bufferedResponseBytes, c.owedTimeoutVotes, c.inflightInputTokens, c.effectiveMaxConcurrent, c.effectiveMaxTokens,
		c.inflightRequestsByModel, c.inflightInputTokensByModel, c.queueDepth,
		c.enforcedMaxConcurrentByModel, c.enforcedMaxTokensByModel,
		c.capacityScale, c.capacityTotalWeight, c.capacityBaselineWeight, c.capacityWeightsUnobserved,
		c.participantsTracked, c.participantsExhausted, c.cutoffState,
	} {
		ch <- desc
	}
}

func (c *LimitsCollector) Collect(ch chan<- prometheus.Metric) {
	snapshot := c.sources.Limiter.Snapshot()
	gauge(ch, c.inflightRequests, float64(snapshot.Total.Requests))
	if c.sources.BufferedBytes != nil {
		gauge(ch, c.bufferedResponseBytes, float64(c.sources.BufferedBytes()))
	}
	if c.sources.OwedVotes != nil {
		gauge(ch, c.owedTimeoutVotes, float64(c.sources.OwedVotes()))
	}
	gauge(ch, c.inflightInputTokens, float64(snapshot.Total.InputTokens))
	gauge(ch, c.effectiveMaxConcurrent, float64(snapshot.ConfiguredMaxConcurrentRequests))
	gauge(ch, c.effectiveMaxTokens, float64(snapshot.ConfiguredMaxInputTokensInFlight))

	for _, model := range c.reportedModels(snapshot) {
		inFlight := snapshot.ByModel[model]
		gauge(ch, c.inflightRequestsByModel, float64(inFlight.Requests), model)
		gauge(ch, c.inflightInputTokensByModel, float64(inFlight.InputTokens), model)
		gauge(ch, c.queueDepth, float64(inFlight.QueueDepth), model)
		enforced := snapshot.EnforcedByModel[model]
		gauge(ch, c.enforcedMaxConcurrentByModel, float64(enforced.MaxConcurrentRequests), model)
		gauge(ch, c.enforcedMaxTokensByModel, float64(enforced.MaxInputTokensInFlight), model)
		weights := c.sources.Capacity.ModelWeights(model)
		gauge(ch, c.capacityScale, weights.ScaleFactor, model)
		gauge(ch, c.capacityTotalWeight, weights.CurrentWeight, model)
		gauge(ch, c.capacityBaselineWeight, weights.BaselineWeight, model)
		gauge(ch, c.capacityWeightsUnobserved, boolGauge(weights.WeightsUnobserved), model)
	}

	windows := c.sources.Participants.Snapshot()
	exhausted := 0
	for _, window := range windows {
		if !window.Available {
			exhausted++
		}
		for _, state := range cutoffStates {
			gauge(ch, c.cutoffState, boolGauge(window.Cutoff == state), window.Participant, window.Model, string(state))
		}
	}
	gauge(ch, c.participantsTracked, float64(len(windows)))
	gauge(ch, c.participantsExhausted, float64(exhausted))
}

// reportedModels folds the configured list into the live one, so an idle model still reports its capacity.
func (c *LimitsCollector) reportedModels(snapshot limits.LimiterSnapshot) []string {
	var configured []string
	if c.sources.Models != nil {
		configured = c.sources.Models()
	}
	models := make([]string, 0, len(snapshot.ByModel)+len(configured))
	for model := range snapshot.ByModel {
		models = append(models, model)
	}
	added := make(map[string]struct{}, len(configured))
	for _, model := range configured {
		_, live := snapshot.ByModel[model]
		_, alreadyAdded := added[model]
		if live || alreadyAdded {
			continue
		}
		added[model] = struct{}{}
		models = append(models, model)
	}
	return models
}
