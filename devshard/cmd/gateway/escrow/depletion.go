package escrow

import (
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
)

// OnBalanceExhausted marks an escrow for the next tick, so this hook does no I/O; only a nonce cap is narrated, since nothing replaces a balance mark.
func (m *Manager) OnBalanceExhausted(escrowID string, reason scheduler.ExhaustionReason) {
	if !m.depleted.mark(escrowID, reason) || m.narrator == nil || reason != scheduler.ExhaustionNonceCap {
		return
	}
	m.narrator.EscrowMarkedForReplacement(escrowID, string(reason))
}

func (m *Manager) markSpent(devshards []store.DevshardRecord) {
	if m.exhaustion == nil {
		return
	}
	for _, record := range devshards {
		if !record.Active {
			continue
		}
		if reason := m.exhaustion.Exhaustion(record.EscrowID); reason != scheduler.ExhaustionNone {
			m.OnBalanceExhausted(record.EscrowID, reason)
		}
	}
}
