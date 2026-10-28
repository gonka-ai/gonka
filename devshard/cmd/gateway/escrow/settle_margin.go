package escrow

import (
	"time"

	"devshard/cmd/gateway/chain"
)

const (
	blockPaceMinimumBlocks = 100
	marginSettleWindows    = 2
)

type blockPace struct {
	firstHeight    int64
	firstAt        time.Time
	measured       time.Duration
	narratedMargin int64
}

func (pace *blockPace) observe(height int64, at time.Time) (time.Duration, bool) {
	switch {
	case height <= 0:
		return 0, false
	case pace.firstHeight == 0 || height < pace.firstHeight:
		pace.firstHeight, pace.firstAt = height, at
		return 0, false
	case height-pace.firstHeight < blockPaceMinimumBlocks:
		return 0, false
	}
	pace.measured = at.Sub(pace.firstAt) / time.Duration(height-pace.firstHeight)
	return pace.measured, true
}

func (pace *blockPace) blockTime() (time.Duration, bool) {
	return pace.measured, pace.measured > 0
}

func (m *Manager) checkSettleMargin(snapshot chain.PhaseSnapshot) {
	if m.chainFacts == nil {
		return
	}
	observedAt := snapshot.LastUpdatedAt
	if observedAt.IsZero() {
		observedAt = m.now()
	}
	blockTime, measured := m.pace.observe(snapshot.BlockHeight, observedAt)
	if !measured {
		return
	}
	marginBlocks := m.config.Load().Rotation.SettleMarginBlocks
	need := marginSettleWindows * commitmentReconcileGrace
	if time.Duration(marginBlocks)*blockTime >= need {
		m.pace.narratedMargin = 0
		return
	}
	if m.pace.narratedMargin == marginBlocks {
		return
	}
	m.pace.narratedMargin = marginBlocks
	if m.narrator != nil {
		m.narrator.SettleMarginShort(marginBlocks, blockTime, need)
	}
}
