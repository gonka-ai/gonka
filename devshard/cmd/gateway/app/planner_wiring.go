package app

import (
	"cmp"
	"maps"
	"slices"

	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/metrics"
	"devshard/cmd/gateway/registry"
)

// escrowFunds reads a live escrow's money for the funding planner from the registry. See ../escrow/README.md, "The funding planner".
type escrowFunds struct{ escrows *registry.Registry }

func (f escrowFunds) EscrowMoney(escrowID string) (escrow.EscrowMoney, bool) {
	candidate, live := f.escrows.Routable(escrowID)
	if !live || candidate.Session == nil {
		return escrow.EscrowMoney{}, false
	}
	sessionConfig, open, known := f.escrows.OpenRecords(escrowID)
	if !known {
		return escrow.EscrowMoney{}, false
	}
	return escrow.EscrowMoney{
		Config: sessionConfig, Balance: candidate.Session.Balance(), TokenPrice: candidate.Session.TokenPrice(),
		FeePerNonce: candidate.Session.FeePerNonce(), Open: open, Holds: f.escrows.Holds(escrowID),
	}, true
}

func (d *depletionNotice) OnMoneyShort(model string) {
	if d.manager != nil {
		d.manager.OnMoneyShort(model)
	}
}

func deadlineCounts(manager *escrow.Manager) func() []metrics.DeadlineUnsettledCount {
	return func() []metrics.DeadlineUnsettledCount {
		counts := manager.DeadlineUnsettledCounts()
		keys := slices.SortedFunc(maps.Keys(counts), func(left, right escrow.DeadlineUnsettled) int {
			return cmp.Or(cmp.Compare(left.Model, right.Model), cmp.Compare(left.Reason, right.Reason))
		})
		exported := make([]metrics.DeadlineUnsettledCount, 0, len(keys))
		for _, key := range keys {
			exported = append(exported, metrics.DeadlineUnsettledCount{Model: key.Model, Reason: key.Reason, Total: counts[key]})
		}
		return exported
	}
}

func fundingModels(manager *escrow.Manager) func() []metrics.FundingModel {
	return func() []metrics.FundingModel {
		reports := manager.FundingReports()
		models := make([]metrics.FundingModel, 0, len(reports))
		for _, report := range reports {
			model := metrics.FundingModel{
				Model: report.Model, Guarantee: report.Guarantee, IgnoredMarks: maps.Clone(report.IgnoredMarks),
				Money: make(map[string]uint64, len(report.Money)), Counts: make(map[string]int, len(report.Counts)),
			}
			for class, value := range report.Money {
				model.Money[string(class)] = value
			}
			for state, count := range report.Counts {
				model.Counts[string(state)] = count
			}
			for _, decision := range slices.SortedFunc(maps.Keys(report.Decisions), compareFundingDecisions) {
				model.Decisions = append(model.Decisions, metrics.FundingDecisionCount{
					Action: string(decision.Action), Reason: string(decision.Reason), Total: report.Decisions[decision],
				})
			}
			models = append(models, model)
		}
		return models
	}
}

func compareFundingDecisions(left, right escrow.FundingDecision) int {
	return cmp.Or(cmp.Compare(left.Action, right.Action), cmp.Compare(left.Reason, right.Reason))
}
