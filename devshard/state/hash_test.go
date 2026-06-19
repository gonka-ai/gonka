package state

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
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

	root1, err := ComputeStateRoot(500, hostStats, inferences, types.PhaseActive, nil, 99, types.DevshardStateRootAndProtocolVersion)
	require.NoError(t, err)
	root2, err := ComputeStateRoot(500, hostStats, inferences, types.PhaseActive, nil, 99, types.DevshardStateRootAndProtocolVersion)
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

	root1, err := ComputeStateRoot(500, hostStats, inferences, types.PhaseActive, nil, 99, types.DevshardStateRootAndProtocolVersion)
	require.NoError(t, err)
	root2, err := ComputeStateRoot(600, hostStats, inferences, types.PhaseActive, nil, 99, types.DevshardStateRootAndProtocolVersion)
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
	fees := uint64(123)
	version := "dev"

	root, err := ComputeStateRoot(balance, hostStats, inferences, types.PhaseActive, nil, fees, version)
	require.NoError(t, err)

	// Manually recompute and verify structure.
	hostStatsHash, err := ComputeHostStatsHash(hostStats)
	require.NoError(t, err)
	restHash, err := ComputeRestHashV2(balance, sealedAccBytes32(nil), inferences, nil)
	require.NoError(t, err)
	feesBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(feesBytes, fees)

	h := sha256.New()
	h.Write(hostStatsHash)
	h.Write(feesBytes)
	h.Write(restHash)
	h.Write(ComputeVersionHash(version))
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

	root1, err := ComputeStateRoot(1000, stats1, inferences, types.PhaseActive, nil, 0, types.DevshardStateRootAndProtocolVersion)
	require.NoError(t, err)
	root2, err := ComputeStateRoot(1000, stats2, inferences, types.PhaseActive, nil, 0, types.DevshardStateRootAndProtocolVersion)
	require.NoError(t, err)
	require.Equal(t, root1, root2)
}

func TestComputeStateRoot_DifferentVersion(t *testing.T) {
	hostStats := map[uint32]*types.HostStats{
		0: {Cost: 100},
	}
	inferences := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 100},
	}

	root1, err := ComputeStateRoot(500, hostStats, inferences, types.PhaseActive, nil, 99, "v1")
	require.NoError(t, err)
	root2, err := ComputeStateRoot(500, hostStats, inferences, types.PhaseActive, nil, 99, "dev")
	require.NoError(t, err)
	require.NotEqual(t, root1, root2)
}

func TestComputeInferencesHashV2_DeterministicAcrossOrders(t *testing.T) {
	var acc [32]byte
	live1 := map[uint64]*types.InferenceRecord{
		3: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 1},
		1: {Status: types.StatusFinished, ExecutorSlot: 1, ActualCost: 2},
	}
	live2 := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, ExecutorSlot: 1, ActualCost: 2},
		3: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 1},
	}
	h1, err := ComputeInferencesHashV2(acc, live1)
	require.NoError(t, err)
	h2, err := ComputeInferencesHashV2(acc, live2)
	require.NoError(t, err)
	require.Equal(t, h1, h2)
}

func TestStateRoot_ExportedHelper_MatchesRestHashV2WithZeroSealedAcc(t *testing.T) {
	hostStats := map[uint32]*types.HostStats{
		0: {Cost: 50, Missed: 1},
		1: {Cost: 75},
	}
	inferences := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 50},
	}
	balance := uint64(875)
	fees := uint64(123)
	version := "dev"

	root, err := ComputeStateRoot(balance, hostStats, inferences, types.PhaseActive, nil, fees, version)
	require.NoError(t, err)

	hostStatsHash, err := ComputeHostStatsHash(hostStats)
	require.NoError(t, err)
	restHash, err := ComputeRestHashV2(balance, sealedAccBytes32(nil), inferences, nil)
	require.NoError(t, err)
	expected := ComputeStateRootFromRestHash(hostStatsHash, restHash, fees, types.PhaseActive, version)
	require.Equal(t, expected, root)
}

func TestStateRoot_V2_SealedAccChangesRestHash(t *testing.T) {
	hostStats := map[uint32]*types.HostStats{
		0: {Cost: 10},
	}
	live := map[uint64]*types.InferenceRecord{
		7: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 10},
	}
	var sealedAcc [32]byte
	sealedAcc[0] = 0xab
	balance := uint64(1000)
	fees := uint64(5)
	version := "v2"

	restHash, err := ComputeRestHashV2(balance, sealedAcc, live, nil)
	require.NoError(t, err)
	rootZeroAcc, err := ComputeStateRoot(balance, hostStats, live, types.PhaseActive, nil, fees, version)
	require.NoError(t, err)

	hostStatsHash, err := ComputeHostStatsHash(hostStats)
	require.NoError(t, err)
	rootWithAcc := ComputeStateRootFromRestHash(hostStatsHash, restHash, fees, types.PhaseActive, version)
	require.NotEqual(t, rootZeroAcc, rootWithAcc, "non-zero SealedAcc must change rest_hash vs zero-acc helper")

	var otherAcc [32]byte
	otherAcc[31] = 0x01
	restOther, err := ComputeRestHashV2(balance, otherAcc, live, nil)
	require.NoError(t, err)
	require.NotEqual(t, restHash, restOther, "sealed accumulator must affect v2 rest hash")
}
