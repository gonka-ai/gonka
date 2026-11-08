package escrow

import (
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/funding"
	"devshard/cmd/gateway/liquidity"
)

// FundingReport is one model's money, escrow counts and planner decisions as the last planning tick read them. See README.md, "The funding planner".
type FundingReport struct {
	Model        string
	Money        map[liquidity.Class]uint64
	Counts       map[CountState]int
	Guarantee    int
	Decisions    map[FundingDecision]uint64
	IgnoredMarks map[string]uint64
}

// FundingDecision is one action and its reason, the key the planner counts its decisions under.
type FundingDecision struct {
	Action funding.Action
	Reason funding.Reason
}

type fundingPlanner struct {
	mu      sync.Mutex
	models  map[string]*modelFunding
	escrows map[string]*escrowFunding
}

type modelFunding struct {
	bucket        funding.Bucket
	demand        funding.DemandWindow
	surplusStreak int
	broken        bool
	misconfigured bool
	budgetReached bool
	walletShort   bool
	decisions     map[FundingDecision]uint64
	ignoredMarks  map[string]uint64
	report        FundingReport
}

type escrowFunding struct {
	seen         bool
	full         bool
	pendingReads int
	idle         bool
	idleSince    time.Time
	holds        uint64
}

type fundingEdge int

const (
	noFundingEdge fundingEdge = iota
	starvedEdge
	fullEdge
)

const edgeConfirmingReads = 2

func (entry *escrowFunding) observe(full, starved bool) fundingEdge {
	if !entry.seen {
		entry.seen, entry.full = true, full
		if starved {
			return starvedEdge
		}
		return noFundingEdge
	}
	if full == entry.full {
		entry.pendingReads = 0
		return noFundingEdge
	}
	entry.pendingReads++
	if entry.pendingReads < edgeConfirmingReads {
		return noFundingEdge
	}
	entry.full, entry.pendingReads = full, 0
	switch {
	case full:
		return fullEdge
	case starved:
		return starvedEdge
	}
	return noFundingEdge
}

type planTransitions struct {
	starved            []funding.EscrowState
	full               []funding.EscrowState
	brokenBegan        bool
	misconfiguredBegan bool
	budgetBegan        bool
}

func (p *fundingPlanner) idleFor(escrowID string, idle bool, holds uint64, now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.escrowLocked(escrowID)
	taken := holds != entry.holds
	entry.holds = holds
	if !idle {
		entry.idle = false
		return 0
	}
	if !entry.idle || taken {
		entry.idle, entry.idleSince = true, now
	}
	return now.Sub(entry.idleSince)
}

func (p *fundingPlanner) recordCreate(model string, err error, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current := p.modelLocked(model, now)
	switch {
	case err == nil:
		current.walletShort = false
	case errors.Is(err, chain.ErrWalletUnderfunded):
		current.walletShort = true
	}
}

func (p *fundingPlanner) ignoreMark(model, reason string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.models == nil {
		return
	}
	p.modelLocked(model, now).ignoredMarks[reason]++
}

func (p *fundingPlanner) decide(model string, reading modelReading, scheduled bool, now time.Time) (funding.ModelState, funding.Decision, planTransitions) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current := p.modelLocked(model, now)
	if scheduled {
		current.demand.Record(reading.reserved)
	}
	current.bucket.Refill(now, TickInterval)
	state := reading.state
	state.WalletShort = current.walletShort
	state.DemandPeak, state.DemandWindowFull = current.demand.Peak(), current.demand.Full()
	state.BucketTokens, state.SurplusStreak, state.ScheduledTick = current.bucket.Available(), current.surplusStreak, scheduled
	decision := funding.Plan(state)
	if decision.Shortfalls.Guard+decision.Shortfalls.Capacity+decision.Shortfalls.Spread == 0 {
		current.walletShort = false
	}
	current.bucket.Take(len(decision.Creates))
	if scheduled {
		current.surplusStreak = streakAfter(current.surplusStreak, decision.SurplusHeld)
	}
	for _, create := range decision.Creates {
		current.decisions[FundingDecision{Action: funding.ActionCreate, Reason: create.Reason}]++
	}
	for _, retire := range decision.Retires {
		current.decisions[FundingDecision{Action: funding.ActionRetire, Reason: retire.Reason}]++
	}
	transitions := planTransitions{
		brokenBegan:        decision.Broken && !current.broken,
		misconfiguredBegan: decision.Misconfigured && !current.misconfigured,
		budgetBegan:        decision.BudgetReached && !current.budgetReached,
	}
	switch {
	case decision.Broken:
		current.broken = true
	case decision.Shortfalls.Guard == 0:
		current.broken = false
	}
	current.misconfigured, current.budgetReached = decision.Misconfigured, decision.BudgetReached
	for _, escrow := range state.Escrows {
		if escrow.Unread {
			continue
		}
		switch p.escrowLocked(escrow.ID).observe(escrow.Money.Full, escrow.Money.Starved) {
		case starvedEdge:
			transitions.starved = append(transitions.starved, escrow)
		case fullEdge:
			transitions.full = append(transitions.full, escrow)
		}
	}
	current.report = reading.report
	current.report.Guarantee = decision.FullCount - state.FullContextSlots
	current.report.Decisions = maps.Clone(current.decisions)
	current.report.IgnoredMarks = maps.Clone(current.ignoredMarks)
	return state, decision, transitions
}

func (p *fundingPlanner) forget(models []ModelConfig, liveEscrows map[string]bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	configured := make(map[string]bool, len(models))
	for _, model := range models {
		configured[model.ModelID] = true
	}
	maps.DeleteFunc(p.models, func(model string, _ *modelFunding) bool { return !configured[model] })
	maps.DeleteFunc(p.escrows, func(escrowID string, _ *escrowFunding) bool { return !liveEscrows[escrowID] })
}

func (p *fundingPlanner) reports() []FundingReport {
	p.mu.Lock()
	defer p.mu.Unlock()
	reports := make([]FundingReport, 0, len(p.models))
	for _, model := range slices.Sorted(maps.Keys(p.models)) {
		report := p.models[model].report
		if report.Model == "" {
			continue
		}
		report.Money, report.Counts, report.Decisions, report.IgnoredMarks = maps.Clone(report.Money), maps.Clone(report.Counts), maps.Clone(report.Decisions), maps.Clone(report.IgnoredMarks)
		reports = append(reports, report)
	}
	return reports
}

func (p *fundingPlanner) modelLocked(model string, now time.Time) *modelFunding {
	current, known := p.models[model]
	if !known {
		current = &modelFunding{bucket: funding.NewBucket(now), decisions: map[FundingDecision]uint64{}, ignoredMarks: map[string]uint64{}}
		p.models[model] = current
	}
	return current
}

func (p *fundingPlanner) escrowLocked(escrowID string) *escrowFunding {
	entry, known := p.escrows[escrowID]
	if !known {
		entry = &escrowFunding{}
		p.escrows[escrowID] = entry
	}
	return entry
}

func streakAfter(streak int, held bool) int {
	if held {
		return streak + 1
	}
	return 0
}
