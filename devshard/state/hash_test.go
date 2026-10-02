package state

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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
	restHash, err := ComputeRestHashV2(balance, sealedAccBytes32(nil), inferences, nil, types.HeightSyncEscrowCommit{})
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
	restHash, err := ComputeRestHashV2(balance, sealedAccBytes32(nil), inferences, nil, types.HeightSyncEscrowCommit{})
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

	restHash, err := ComputeRestHashV2(balance, sealedAcc, live, nil, types.HeightSyncEscrowCommit{})
	require.NoError(t, err)
	rootZeroAcc, err := ComputeStateRoot(balance, hostStats, live, types.PhaseActive, nil, fees, version)
	require.NoError(t, err)

	hostStatsHash, err := ComputeHostStatsHash(hostStats)
	require.NoError(t, err)
	rootWithAcc := ComputeStateRootFromRestHash(hostStatsHash, restHash, fees, types.PhaseActive, version)
	require.NotEqual(t, rootZeroAcc, rootWithAcc, "non-zero SealedAcc must change rest_hash vs zero-acc helper")

	var otherAcc [32]byte
	otherAcc[31] = 0x01
	restOther, err := ComputeRestHashV2(balance, otherAcc, live, nil, types.HeightSyncEscrowCommit{})
	require.NoError(t, err)
	require.NotEqual(t, restHash, restOther, "sealed accumulator must affect v2 rest hash")
}

func inferencesHashFixture(count int) map[uint64]*types.InferenceRecord {
	emptyInputDigest := sha256.Sum256(nil)
	inferences := make(map[uint64]*types.InferenceRecord, count)
	for index := range count {
		id := uint64(index*3 + 1)
		record := &types.InferenceRecord{
			Status:       types.InferenceStatus(index % 5),
			ExecutorSlot: uint32(index % 7),
			Model:        "loadsim-model",
			PromptHash:   append([]byte{byte(index)}, emptyInputDigest[:]...),
			InputTokens:  uint64(index),
			ActualCost:   uint64(index) * 7,
			StartedAt:    int64(1_700_000_000 + index),
		}
		if index%2 == 0 {
			record.ResponseHash = []byte{byte(index), 1, 2}
			record.ValidatedBy = types.Bitmap128{uint64(index), 1}
		}
		inferences[id] = record
	}
	return inferences
}

// Test flow:
//  1. Build fixed inference sets and records.
//  2. Hash and marshal them with the current code.
//  3. Require the exact bytes the previous encoding produced, so gateways and hosts on either build agree on every state root.
func TestInferenceEncodingMatchesThePreviousRelease(t *testing.T) {
	wantHashes := map[int]string{
		0:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		1:   "e7f5a443cb40f3ffbbf85a2e20c13679a6bdef9de3d0c14f3da040756392f692",
		300: "20878299e101202bdc4f2b86f69b1cf7cc4fff650f3dc51757d9503c04ceb6f2",
	}
	for count, want := range wantHashes {
		got, err := computeInferencesHash(inferencesHashFixture(count))
		require.NoError(t, err)
		require.Equal(t, want, hex.EncodeToString(got), "inference count %d", count)
	}

	wantEntries := map[uint64]string{
		1: "0801220d6c6f616473696d2d6d6f64656c2a2100e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85532030001026880e2cfaa068a011000000000000000000100000000000000",
		4: "080410011801220d6c6f616473696d2d6d6f64656c2a2101e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855480160076881e2cfaa068a011000000000000000000000000000000000",
		7: "080710021802220d6c6f616473696d2d6d6f64656c2a2102e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85532030201024802600e6882e2cfaa068a011002000000000000000100000000000000",
	}
	inferences := inferencesHashFixture(3)
	for id, want := range wantEntries {
		got, err := marshalInferenceEntry(id, inferences[id])
		require.NoError(t, err)
		require.Equal(t, want, hex.EncodeToString(got), "inference %d", id)
	}

	hostStats := map[uint32]*types.HostStats{0: {Cost: 100}, 1: {Cost: 200}}
	root, err := ComputeStateRoot(500, hostStats, inferencesHashFixture(300), types.PhaseActive, nil, 99, types.DevshardStateRootAndProtocolVersion)
	require.NoError(t, err)
	require.Equal(t, "9ef896fc48385ea6bd0f1606cd63325001afe99ff7cbd5ce8da93c504134747d", hex.EncodeToString(root))
}

// Test flow:
//  1. Build a set of a thousand live inferences.
//  2. Hash it repeatedly while counting allocations.
//  3. Require fewer allocations than inferences, so the hash does not allocate per inference.
func TestComputeInferencesHashDoesNotAllocatePerInference(t *testing.T) {
	inferences := inferencesHashFixture(1000)
	allocations := testing.AllocsPerRun(20, func() {
		_, err := computeInferencesHash(inferences)
		require.NoError(t, err)
	})
	require.Less(t, allocations, float64(len(inferences)))
}
