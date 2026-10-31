package escrow

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/funding"
	"devshard/cmd/gateway/liquidity"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
	"devshard/types"
	"devshard/user"
)

type modelReading struct {
	state    funding.ModelState
	reserved uint64
	live     []string
	report   FundingReport
}

type modelPricing struct {
	reserveTokens uint64
	attempts      uint64
}

// OnMoneyShort marks a model routing found no escrow able to pay for and wakes the tick once per mark; it does no I/O. See README.md, "The funding planner".
func (m *Manager) OnMoneyShort(model string) {
	if !m.moneyShort.mark(model) {
		return
	}
	select {
	case m.wakeup <- struct{}{}:
	default:
	}
}

// FundingReports is the funding planner's last reading of every configured model, for the metrics collector.
func (m *Manager) FundingReports() []FundingReport {
	return m.planner.reports()
}

// planFunding reads every configured model's money, plans its creates and retires, narrates the decision and executes it. See README.md, "The funding planner".
func (m *Manager) planFunding(ctx context.Context, scheduled bool) error {
	configuration := m.config.Load()
	models, ready := m.plannableModels(ctx, configuration.Rotation)
	if !ready {
		return nil
	}
	if len(models) == 0 {
		m.planner.forget(nil, nil)
		return nil
	}
	rows, err := m.store.ListDevshards(ctx)
	if err != nil {
		m.narratePlanFailed(err)
		return nil
	}
	commitments, err := m.store.LoadCommitments(ctx)
	if err != nil {
		m.narratePlanFailed(err)
		return nil
	}
	moneyShort := m.moneyShort.drain()
	snapshot, now := m.snapshots.Snapshot(), m.now()
	liveEscrows := map[string]bool{}
	planned := map[string]bool{}
	var errs []error
	for _, model := range models {
		if planned[model.ModelID] {
			continue
		}
		planned[model.ModelID] = true
		reading := m.readModel(model, rows, commitments, snapshot, configuration, now)
		reading.state.MoneyShort = moneyShort[model.ModelID]
		reading.state.BreakerGated = m.breaker.gated(model.ModelID, roleRegular)
		for _, escrowID := range reading.live {
			liveEscrows[escrowID] = true
		}
		state, decision, transitions := m.planner.decide(model.ModelID, reading, scheduled, now)
		m.narratePlan(model.ModelID, state, decision, transitions)
		errs = append(errs, m.applyPlan(ctx, model, snapshot, rows, decision, state.InBridgeWindow))
	}
	m.planner.forget(models, liveEscrows)
	return errors.Join(errs...)
}

func (m *Manager) plannableModels(ctx context.Context, rotation config.Rotation) ([]ModelConfig, bool) {
	if m.funds == nil || ctx.Err() != nil {
		return nil, false
	}
	models, err := rotationModels(rotation)
	if err != nil {
		return nil, true
	}
	return models, true
}

func (m *Manager) readModel(model ModelConfig, rows []store.DevshardRecord, commitments []store.Commitment, snapshot chain.PhaseSnapshot, configuration *config.Config, now time.Time) modelReading {
	pricing := newModelPricing(model.ModelID, snapshot, configuration)
	fullCost, slot, priced := pricing.at(snapshot.TokenPrice, snapshot.FeePerNonce)
	served, known := servedByNetwork(snapshot, model.ModelID)
	blocksToSwitch, prePoC := snapshot.EpochSwitchBlockHeight-snapshot.BlockHeight, configuration.Rotation.PrePoCBlocks
	reading := modelReading{
		state: funding.ModelState{
			TargetCount: model.TargetCount, TempCount: model.TempCount, ReserveCount: model.ReserveCount,
			MaxUnsettled: model.MaxUnsettled, FullContextSlots: model.FullContextSlots,
			Amount: model.Amount, CreateFee: snapshot.CreateDevshardFee,
			FullCost: fullCost, Slot: slot, Priced: priced && snapshot.TokenPrice > 0,
			RequestsBlocked: snapshot.RequestsBlocked, EpochKnown: !snapshotHasNoEpochYet(snapshot),
			ServedByNetwork: served || !known, BreakerGated: m.breaker.open(model.ModelID, roleRegular),
			InBridgeWindow:   blocksToSwitch >= 0 && blocksToSwitch <= prePoC,
			NearBridgeWindow: blocksToSwitch > prePoC && blocksToSwitch <= 3*prePoC,
			CurrentLabel:     int64(snapshot.EpochIndex),
		},
		report: FundingReport{
			Model:  model.ModelID,
			Money:  map[liquidity.Class]uint64{liquidity.ClassFree: 0, liquidity.ClassReturning: 0, liquidity.ClassLate: 0, liquidity.ClassStuck: 0},
			Counts: make(map[CountState]int, len(countStates)),
		},
	}
	for _, countState := range countStates {
		reading.report.Counts[countState] = 0
	}
	sweepGrace := time.Duration(configuration.TimeoutSweep.GraceSeconds) * time.Second
	for _, row := range rows {
		if row.Model == model.ModelID {
			if !goneFromChain(row) {
				reading.state.Counted++
			}
			m.readRow(&reading, row, pricing, sweepGrace, now)
		}
	}
	for _, commitment := range commitments {
		if commitment.Model == model.ModelID {
			reading.state.Counted++
			reading.report.Counts[CountCommitted]++
			reading.countPendingCreate(commitment.Role)
		}
	}
	return reading
}

func (reading *modelReading) countPendingCreate(role string) {
	switch role {
	case RoleReserve:
		reading.state.PendingStandbyCreates++
	case roleRegular:
		reading.state.PendingRegularCreates++
	}
}

func (m *Manager) readRow(reading *modelReading, row store.DevshardRecord, pricing modelPricing, sweepGrace time.Duration, now time.Time) {
	switch {
	case goneFromChain(row):
		reading.report.Counts[CountInactive]++
		return
	case !row.Active && (row.SettlementPending || row.SettleTxHash != ""):
		reading.report.Counts[CountParked]++
		if row.SettlementPending {
			reading.state.Parking++
		}
		return
	case !row.Active:
		reading.report.Counts[CountInactive]++
		return
	}
	money, live := m.funds.EscrowMoney(row.EscrowID)
	if !live {
		reading.report.Counts[CountUnread]++
		if row.Amount > 0 {
			reading.state.Escrows = append(reading.state.Escrows, unreadEscrow(row, reading.state))
		}
		return
	}
	nonceCapped := m.exhaustion != nil && m.exhaustion.Exhaustion(row.EscrowID) == scheduler.ExhaustionNonceCap
	fullCost, slot, priced := pricing.at(money.TokenPrice, money.FeePerNonce)
	classified := liquidity.Classify(liquidity.Input{
		Config: money.Config, Balance: money.Balance, Open: money.Open, NonceCapReached: nonceCapped,
		Price:   liquidity.Price{TokenPrice: money.TokenPrice, BytesPerToken: filters.PromptBytesPerToken, Slot: slot, Priced: priced},
		Margins: liquidity.Margins{TimeoutBuffer: user.TimeoutBuffer, SweepGrace: sweepGrace},
		Now:     now,
	})
	reading.live = append(reading.live, row.EscrowID)
	reading.reserved = saturatingAdd(reading.reserved, reservedIn(money.Open))
	totals := reading.report.Money
	totals[liquidity.ClassFree] = saturatingAdd(totals[liquidity.ClassFree], classified.Free)
	totals[liquidity.ClassReturning] = saturatingAdd(totals[liquidity.ClassReturning], classified.Returning)
	totals[liquidity.ClassLate] = saturatingAdd(totals[liquidity.ClassLate], classified.Late)
	totals[liquidity.ClassStuck] = saturatingAdd(totals[liquidity.ClassStuck], classified.Stuck)
	reading.report.Counts[countStateOf(row, classified)]++
	reading.state.Escrows = append(reading.state.Escrows, funding.EscrowState{
		ID: row.EscrowID, Standby: row.RotationRole == RoleReserve, Temp: row.RotationRole == roleTemp,
		Label: row.RotationEpoch, NonceCapReached: nonceCapped, FullCost: fullCost, Money: classified,
		IdleFor: m.planner.idleFor(row.EscrowID, classified.Idle, money.Holds, now),
	})
}

// unreadEscrow is a row with no live session yet as the fresh escrow it is: its stored amount, nothing in flight, at the model's current price.
func unreadEscrow(row store.DevshardRecord, state funding.ModelState) funding.EscrowState {
	classified := liquidity.Classify(liquidity.Input{
		Balance: row.Amount,
		Price:   liquidity.Price{BytesPerToken: filters.PromptBytesPerToken, Slot: state.Slot, Priced: state.Priced},
	})
	return funding.EscrowState{
		ID: row.EscrowID, Standby: row.RotationRole == RoleReserve, Temp: row.RotationRole == roleTemp, Unread: true,
		Label: row.RotationEpoch, FullCost: state.FullCost, Money: classified,
	}
}

func (m *Manager) narratePlan(model string, state funding.ModelState, decision funding.Decision, transitions planTransitions) {
	if m.narrator == nil {
		return
	}
	for _, escrow := range transitions.starved {
		m.narrator.EscrowStarved(escrow.ID, escrow.Money.Free)
	}
	for _, escrow := range transitions.full {
		m.narrator.EscrowFull(escrow.ID, escrow.Money.Free)
	}
	if transitions.misconfiguredBegan {
		m.narrator.FundingMisconfigured(model, state.Amount, decision.AmountNeeded)
	}
	if transitions.brokenBegan {
		m.narrator.FundingGuaranteeBroken(model, decision.FullCount, state.FullContextSlots)
	}
	if transitions.budgetBegan {
		m.narrator.EscrowBudgetReached(model, state.Counted, state.MaxUnsettled)
	}
	if len(decision.Creates)+len(decision.Retires) > 0 {
		m.narrator.EscrowPlanned(model, len(decision.Creates), len(decision.Retires), joinReasons(decision.Reasons()), state.MoneyShort, decision.Need, decision.Liquid, decision.FullCount)
	}
}

func (m *Manager) narratePlanFailed(err error) {
	if m.narrator != nil {
		m.narrator.FundingPlanFailed(err)
	}
}

func newModelPricing(modelID string, snapshot chain.PhaseSnapshot, configuration *config.Config) modelPricing {
	return modelPricing{
		reserveTokens: configuration.Limits.RetirementReserve(modelID, snapshot.Models[modelID].MaxModelLen),
		attempts:      uint64(scheduler.AttemptsToFund(configuration.Engine.MaxAttemptsPerRequest)),
	}
}

func (pricing modelPricing) at(tokenPrice, feePerNonce uint64) (fullCost, slot uint64, priced bool) {
	fullCost, fullPriced := scheduler.RequestCost(pricing.reserveTokens, tokenPrice, feePerNonce)
	slot = fullCost * pricing.attempts
	slotPriced := fullCost == 0 || slot/fullCost == pricing.attempts
	return fullCost, slot, fullPriced && slotPriced
}

func reservedIn(open []types.InferenceRecord) uint64 {
	var reserved uint64
	for _, record := range open {
		if record.Status == types.StatusPending || record.Status == types.StatusStarted {
			reserved = saturatingAdd(reserved, record.ReservedCost)
		}
	}
	return reserved
}

func saturatingAdd(left, right uint64) uint64 {
	if sum := left + right; sum >= left {
		return sum
	}
	return math.MaxUint64
}

func countStateOf(row store.DevshardRecord, money liquidity.Escrow) CountState {
	switch {
	case row.RotationRole == RoleReserve:
		return CountStandby
	case money.Full:
		return CountFull
	case money.Starved:
		return CountStarved
	}
	return CountSpent
}

func joinReasons(reasons []funding.Reason) string {
	names := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		names = append(names, string(reason))
	}
	return strings.Join(names, ",")
}
