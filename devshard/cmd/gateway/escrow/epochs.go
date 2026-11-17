package escrow

import (
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/store"
)

func effectiveEpoch(snapshot chain.PhaseSnapshot) uint64 {
	if snapshot.EffectiveEpochIndex > 0 {
		return snapshot.EffectiveEpochIndex
	}
	return snapshot.EpochIndex
}

// ownEpochOver compares the effective epoch's lower bound with the chain epoch's upper bound, the label while unresolved.
func ownEpochOver(record store.DevshardRecord, snapshot chain.PhaseSnapshot) bool {
	chainEpoch, known := latestChainEpochOf(record)
	return known && effectiveEpochFloor(snapshot) > chainEpoch
}

func latestChainEpochOf(record store.DevshardRecord) (uint64, bool) {
	switch {
	case record.ChainEpoch > 0:
		return record.ChainEpoch, true
	case record.RotationEpoch > 0:
		return uint64(record.RotationEpoch), true
	}
	return 0, false
}

func effectiveEpochFloor(snapshot chain.PhaseSnapshot) uint64 {
	if snapshot.EffectiveEpochIndex == 0 && snapshot.EpochIndex > 0 {
		return snapshot.EpochIndex - 1
	}
	return snapshot.EffectiveEpochIndex
}

func (m *Manager) prunableOnChain(record store.DevshardRecord) bool {
	chainEpoch, known := latestChainEpochOf(record)
	if !known || m.snapshots == nil {
		return false
	}
	snapshot := m.snapshots.Snapshot()
	snapshot.EffectiveEpochIndex = effectiveEpochFloor(snapshot)
	return snapshot.EffectiveEpochIndex > 0 && deadlineOfEpoch(chainEpoch, snapshot, noSettleMargin).passed
}

func bridgeLabel(snapshot chain.PhaseSnapshot) uint64 {
	return effectiveEpoch(snapshot) + 1
}
