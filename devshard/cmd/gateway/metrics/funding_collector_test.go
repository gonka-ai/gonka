package metrics

import "testing"

// Test flow:
//  1. Register a `FundingCollector` whose source reports one model's money, counts, one decision and its guarantee.
//  2. Assert each family reports those values under the model's labels, one series per label value.
func TestTheFundingCollectorReportsOneSeriesPerModelAndLabel(t *testing.T) {
	telemetry := New()
	telemetry.Register(NewFundingCollector(func() []FundingModel {
		return []FundingModel{{
			Model:        "qwen",
			Money:        map[string]uint64{"free": 40_000, "returning": 3_000, "late": 0, "stuck": 700},
			Counts:       map[string]int{"starved": 1, "full": 2},
			Guarantee:    -1,
			Decisions:    []FundingDecisionCount{{Action: "create", Reason: "guard", Total: 5}},
			IgnoredMarks: map[string]uint64{"balance_floor": 3},
		}}
	}, func() []DeadlineUnsettledCount {
		return []DeadlineUnsettledCount{{Model: "qwen", Reason: "settlement_disabled", Total: 2}}
	}))

	expectGauge(t, telemetry, "devshard_gateway_escrow_money", labels{"model": "qwen", "class": "free"}, 40_000)
	expectGauge(t, telemetry, "devshard_gateway_escrow_money", labels{"model": "qwen", "class": "stuck"}, 700)
	expectSeriesCount(t, telemetry, "devshard_gateway_escrow_money", 4)
	expectGauge(t, telemetry, "devshard_gateway_escrow_count", labels{"model": "qwen", "state": "full"}, 2)
	expectCounter(t, telemetry, "devshard_gateway_planner_decisions_total", labels{"model": "qwen", "action": "create", "reason": "guard"}, 5)
	expectGauge(t, telemetry, "devshard_gateway_funding_guarantee", labels{"model": "qwen"}, -1)
	expectCounter(t, telemetry, "devshard_gateway_planner_ignored_marks_total", labels{"model": "qwen", "reason": "balance_floor"}, 3)
	expectCounter(t, telemetry, "devshard_gateway_escrow_deadline_unsettled_total", labels{"model": "qwen", "reason": "settlement_disabled"}, 2)
}

// Test flow:
//  1. Register a `FundingCollector` with a nil source, and one whose source reports no model, as the planner does while off.
//  2. Assert neither emits a guarantee series.
func TestAFundingCollectorWithNothingToReportEmitsNothing(t *testing.T) {
	for name, models := range map[string]func() []FundingModel{"nil source": nil, "planner off": func() []FundingModel { return nil }} {
		t.Run(name, func(t *testing.T) {
			telemetry := New()
			telemetry.Register(NewFundingCollector(models, nil))

			expectSeriesCount(t, telemetry, "devshard_gateway_funding_guarantee", 0)
		})
	}
}
