package metrics

import (
	"maps"
	"slices"

	"github.com/prometheus/client_golang/prometheus"
)

// FundingModel is one model's funding as the planner last read it; plain values keep metrics free of an edge to escrow.
type FundingModel struct {
	Model        string
	Money        map[string]uint64
	Counts       map[string]int
	Guarantee    int
	Decisions    []FundingDecisionCount
	IgnoredMarks map[string]uint64
}

// FundingDecisionCount is how often the planner decided one action for one reason since its mode last changed.
type FundingDecisionCount struct {
	Action string
	Reason string
	Total  uint64
}

// DeadlineUnsettledCount is how many escrows the deadline rule narrated unsettled for one model and reason.
type DeadlineUnsettledCount struct {
	Model  string
	Reason string
	Total  uint64
}

// FundingCollector reports the funding planner's per-model money, counts, decisions and guarantee. See operations.md, "Metrics".
type FundingCollector struct {
	models    func() []FundingModel
	deadlines func() []DeadlineUnsettledCount

	money     *prometheus.Desc
	count     *prometheus.Desc
	decisions *prometheus.Desc
	guarantee *prometheus.Desc

	ignoredMarks      *prometheus.Desc
	deadlineUnsettled *prometheus.Desc
}

// NewFundingCollector reads models and deadline counts on every scrape; an empty answer emits no series.
func NewFundingCollector(models func() []FundingModel, deadlines func() []DeadlineUnsettledCount) *FundingCollector {
	return &FundingCollector{
		models:            models,
		deadlines:         deadlines,
		money:             gaugeDesc("devshard_gateway_escrow_money", "Money a model's live escrows hold, by class: free, returning, late or stuck.", "model", "class"),
		count:             gaugeDesc("devshard_gateway_escrow_count", "A model's escrows by funding state, commitments included.", "model", "state"),
		decisions:         counterDesc("devshard_gateway_planner_decisions_total", "Creates and retires the funding planner decided, by action and reason.", "model", "action", "reason"),
		guarantee:         gaugeDesc("devshard_gateway_funding_guarantee", "Full escrows a model holds minus full_context_slots; below zero the guarantee is short.", "model"),
		ignoredMarks:      counterDesc("devshard_gateway_planner_ignored_marks_total", "Depletion marks the planner dropped because nothing replaces a balance mark, by reason.", "model", "reason"),
		deadlineUnsettled: counterDesc("devshard_gateway_escrow_deadline_unsettled_total", "Escrows the deadline rule narrated unsettled at their settle margin or past it, by reason.", "model", "reason"),
	}
}

func (collector *FundingCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.money
	ch <- collector.count
	ch <- collector.decisions
	ch <- collector.guarantee
	ch <- collector.ignoredMarks
	ch <- collector.deadlineUnsettled
}

func (collector *FundingCollector) Collect(ch chan<- prometheus.Metric) {
	if collector.models != nil {
		collector.collectModels(ch)
	}
	if collector.deadlines != nil {
		for _, count := range collector.deadlines() {
			counter(ch, collector.deadlineUnsettled, float64(count.Total), count.Model, count.Reason)
		}
	}
}

func (collector *FundingCollector) collectModels(ch chan<- prometheus.Metric) {
	for _, model := range collector.models() {
		for _, class := range slices.Sorted(maps.Keys(model.Money)) {
			gauge(ch, collector.money, float64(model.Money[class]), model.Model, class)
		}
		for _, state := range slices.Sorted(maps.Keys(model.Counts)) {
			gauge(ch, collector.count, float64(model.Counts[state]), model.Model, state)
		}
		for _, decision := range model.Decisions {
			counter(ch, collector.decisions, float64(decision.Total), model.Model, decision.Action, decision.Reason)
		}
		for _, reason := range slices.Sorted(maps.Keys(model.IgnoredMarks)) {
			counter(ch, collector.ignoredMarks, float64(model.IgnoredMarks[reason]), model.Model, reason)
		}
		gauge(ch, collector.guarantee, float64(model.Guarantee), model.Model)
	}
}
