package escrow

import (
	"time"

	"devshard/cmd/gateway/chain"
)

const staleFallbackBlockTime = time.Second

type staleProjection struct {
	narrated bool
}

func (m *Manager) projectStale(snapshot chain.PhaseSnapshot) chain.PhaseSnapshot {
	if m.chainFacts == nil || snapshot.LastUpdatedAt.IsZero() || snapshotHasNoEpochYet(snapshot) {
		return snapshot
	}
	maxAge := time.Duration(m.config.Load().Chain.SnapshotMaxAgeSeconds) * time.Second
	staleFor := m.now().Sub(snapshot.LastUpdatedAt)
	if maxAge <= 0 || staleFor <= maxAge {
		m.stale.narrated = false
		return snapshot
	}
	blockTime, measured := m.pace.blockTime()
	if !measured || blockTime < staleFallbackBlockTime {
		blockTime = staleFallbackBlockTime
	}
	projected := snapshot
	projected.BlockHeight += int64(staleFor / blockTime)
	if !m.stale.narrated && m.narrator != nil {
		m.narrator.EscrowDeadlinesProjected(snapshot.BlockHeight, projected.BlockHeight, staleFor)
	}
	m.stale.narrated = true
	return projected
}
