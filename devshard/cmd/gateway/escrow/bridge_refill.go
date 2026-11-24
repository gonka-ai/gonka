package escrow

import (
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/funding"
	"devshard/cmd/gateway/store"
)

// bridgeRefillTarget keeps a filled bridge liquid without retiring temps that still own in-flight work.
func (m *Manager) bridgeRefillTarget(model ModelConfig, target int, snapshot chain.PhaseSnapshot, rows []store.DevshardRecord, commitments []store.Commitment) int {
	label := bridgeLabel(snapshot)
	existing := countActive(rows, model.ModelID, roleTemp, int64(label))
	configuration := m.config.Load()
	if m.funds == nil || existing < model.TempCount || configuration.Modes.BlocksRequests(snapshot) {
		return target
	}
	temps := make([]store.DevshardRecord, 0, existing)
	for _, row := range rows {
		if row.Active && row.Model == model.ModelID && row.RotationRole == roleTemp && row.RotationEpoch == int64(label) {
			temps = append(temps, row)
		}
	}
	now := m.now()
	reading := m.readModel(model, temps, nil, snapshot, configuration, now)
	// A deliberately smaller bridge pool need not satisfy the regular pool's
	// full-escrow guarantee; restore at most temp_count full temps instead.
	reading.state.FullContextSlots = min(reading.state.FullContextSlots, model.TempCount)
	shortfall, budget := m.planner.bridgeRefill(model.ModelID, reading, now)
	// Unread rows with a known amount already contribute money to the plan.
	// Give unresolved rows one pending slot each until their amount is known.
	unread := reading.report.Counts[CountUnread]
	for _, escrow := range reading.state.Escrows {
		if escrow.Unread {
			unread--
		}
	}
	pending := countOpenCreates(commitments, model.ModelID, roleTemp, label)
	wanted := bridgeTarget(model, max(target, existing+max(shortfall-unread, 0)))
	return min(wanted, existing+pending+budget)
}

func (p *fundingPlanner) bridgeRefill(model string, reading modelReading, now time.Time) (shortfall, budget int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current := p.modelLocked(model, now)
	current.bucket.Refill(now, TickInterval)
	state := reading.state
	state.DemandPeak = current.demand.Peak()
	decision := funding.Plan(state)
	return max(decision.Shortfalls.Guard, decision.Shortfalls.Capacity), current.bucket.Available()
}

func (p *fundingPlanner) recordBridgeCreates(model string, created int, now time.Time) {
	if created == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.modelLocked(model, now).bucket.Take(created)
}
