package state

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"

	"subnet/types"
)

func TestComputeStateRoot_Deterministic(t *testing.T) {
	hostStats := map[uint32]*types.HostStats{
		0: {Cost: 100},
		1: {Cost: 200},
	}
	inferences := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 100},
		2: {Status: types.StatusFinished, ExecutorSlot: 1, ActualCost: 200},
	}

	root1, err := ComputeStateRoot(500, hostStats, inferences, types.PhaseActive, nil)
	require.NoError(t, err)
	root2, err := ComputeStateRoot(500, hostStats, inferences, types.PhaseActive, nil)
	require.NoError(t, err)
	require.Equal(t, root1, root2)
}

func TestComputeStateRoot_DifferentState(t *testing.T) {
	hostStats := map[uint32]*types.HostStats{
		0: {Cost: 100},
	}
	inferences := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 100},
	}

	root1, err := ComputeStateRoot(500, hostStats, inferences, types.PhaseActive, nil)
	require.NoError(t, err)
	root2, err := ComputeStateRoot(600, hostStats, inferences, types.PhaseActive, nil)
	require.NoError(t, err)
	require.NotEqual(t, root1, root2)
}

func TestStateRoot_MerkleStructure(t *testing.T) {
	hostStats := map[uint32]*types.HostStats{
		0: {Cost: 50, Missed: 1},
		1: {Cost: 75},
	}
	inferences := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 50},
	}
	balance := uint64(875)

	root, err := ComputeStateRoot(balance, hostStats, inferences, types.PhaseActive, nil)
	require.NoError(t, err)

	// Manually recompute and verify structure.
	hostStatsHash, err := ComputeHostStatsHash(hostStats)
	require.NoError(t, err)
	restHash, err := ComputeRestHash(balance, inferences, nil)
	require.NoError(t, err)

	h := sha256.New()
	h.Write(hostStatsHash)
	h.Write(restHash)
	h.Write([]byte{uint8(types.PhaseActive)})
	expected := h.Sum(nil)

	require.Equal(t, expected, root)
}

func TestStateRoot_SortedKeys(t *testing.T) {
	// Create host stats with IDs in different insertion orders.
	// Both should produce the same hash.
	stats1 := map[uint32]*types.HostStats{
		5: {Cost: 10},
		2: {Cost: 20},
		8: {Cost: 30},
	}
	stats2 := map[uint32]*types.HostStats{
		8: {Cost: 30},
		5: {Cost: 10},
		2: {Cost: 20},
	}

	inferences := map[uint64]*types.InferenceRecord{}

	root1, err := ComputeStateRoot(1000, stats1, inferences, types.PhaseActive, nil)
	require.NoError(t, err)
	root2, err := ComputeStateRoot(1000, stats2, inferences, types.PhaseActive, nil)
	require.NoError(t, err)
	require.Equal(t, root1, root2)
}
