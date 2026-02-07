package inference

import (
	"context"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Debug Logger to print logs to stdout during tests
type DebugLogger struct {
	t *testing.T
}

func (l DebugLogger) LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	l.t.Logf("[INFO] %s: %v", msg, keyvals)
}
func (l DebugLogger) LogError(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	l.t.Logf("[ERROR] %s: %v", msg, keyvals)
}
func (l DebugLogger) LogWarn(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	l.t.Logf("[WARN] %s: %v", msg, keyvals)
}
func (l DebugLogger) LogDebug(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	l.t.Logf("[DEBUG] %s: %v", msg, keyvals)
}

// setupTestEnvironment creates a fresh mock keeper and participants for each test run
// Adds a small "validation node" to bypass voting constraints (34% rule)
func setupTestEnvironment(t *testing.T) (*types.ActiveParticipant, *types.ActiveParticipant, *mockKeeperForModelAssigner) {
	t.Helper()
	modelID := "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"

	pA := &types.ActiveParticipant{
		Index:  "ParticipantA",
		Models: []string{modelID},
		MlNodes: []*types.ModelMLNodes{
			{
				MlNodes: []*types.MLNodeInfo{
					{NodeId: "A-Node", PocWeight: 100},
					{NodeId: "A-Val1", PocWeight: 90},
					{NodeId: "A-Val2", PocWeight: 90},
					{NodeId: "A-Val3", PocWeight: 90},
					{NodeId: "A-Val4", PocWeight: 90},
					{NodeId: "A-Val5", PocWeight: 90},
				},
			},
		},
	}

	pZ := &types.ActiveParticipant{
		Index:  "ParticipantZ",
		Models: []string{modelID},
		MlNodes: []*types.ModelMLNodes{
			{
				MlNodes: []*types.MLNodeInfo{
					{NodeId: "Z-Node", PocWeight: 100},
					{NodeId: "Z-Val1", PocWeight: 90},
					{NodeId: "Z-Val2", PocWeight: 90},
					{NodeId: "Z-Val3", PocWeight: 90},
					{NodeId: "Z-Val4", PocWeight: 90},
					{NodeId: "Z-Val5", PocWeight: 90},
				},
			},
		},
	}

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{{Id: modelID}},
		hardwareNodes: map[string]*types.HardwareNodes{
			"ParticipantA": {
				Participant: "ParticipantA",
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "A-Node", Models: []string{modelID}},
					{LocalId: "A-Val", Models: []string{modelID}},
				},
			},
			"ParticipantZ": {
				Participant: "ParticipantZ",
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "Z-Node", Models: []string{modelID}},
					{LocalId: "Z-Val", Models: []string{modelID}},
				},
			},
		},
		epochGroupData: map[string]map[uint64]types.EpochGroupData{},
		params: &types.Params{
			EpochParams: &types.EpochParams{
				// Target Weight = 5% of 1100 = 55.
				// A-Val1 (90) satisfies it immediately.
				// Result: Only FIRST participant gets allocated.
				PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -2},
			},
		},
	}
	
	return pA, pZ, mockKeeper
}

func getAllocatedParticipant(
	t *testing.T,
	ctx context.Context,
	assigner *ModelAssigner,
	mockKeeper *mockKeeperForModelAssigner,
	pA, pZ *types.ActiveParticipant,
	epochIdx uint64,
	modelID string,
) string {
	t.Helper()
	// Setup previous epoch data to ensure they pass "History" filter
	prevData := types.EpochGroupData{
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress: "ParticipantA",
				MlNodes: []*types.MLNodeInfo{{NodeId: "A-Node", PocWeight: 100}},
			},
			{
				MemberAddress: "ParticipantZ",
				MlNodes: []*types.MLNodeInfo{{NodeId: "Z-Node", PocWeight: 105}},
			},
		},
	}
	if mockKeeper.epochGroupData == nil {
		mockKeeper.epochGroupData = make(map[string]map[uint64]types.EpochGroupData)
	}
	if mockKeeper.epochGroupData[modelID] == nil {
		mockKeeper.epochGroupData[modelID] = make(map[uint64]types.EpochGroupData)
	}
	mockKeeper.epochGroupData[modelID][epochIdx-1] = prevData

	// Reset nodes state
	pA.MlNodes[0].MlNodes[0].TimeslotAllocation = []bool{true, false}
	pA.MlNodes[0].MlNodes[1].TimeslotAllocation = []bool{true, false}
	pZ.MlNodes[0].MlNodes[0].TimeslotAllocation = []bool{true, false}
	pZ.MlNodes[0].MlNodes[1].TimeslotAllocation = []bool{true, false}

	// Re-calculate weights
	pA.Weight = 550 // 100 + 5*90
	pZ.Weight = 550 // 100 + 5*90

	upcomingEpoch := types.Epoch{Index: epochIdx}
	// Reset allocation state for participants before each trial
	for _, mn := range pA.MlNodes[0].MlNodes {
		mn.TimeslotAllocation = []bool{true, false}
	}
	for _, mn := range pZ.MlNodes[0].MlNodes {
		mn.TimeslotAllocation = []bool{true, false}
	}

	assigner.AllocateMLNodesForPoC(ctx, upcomingEpoch, []*types.ActiveParticipant{pA, pZ})

	// Check who got ANY slot by checking updated structs (now that state propagates back)
	aSelected := false
	for _, mn := range pA.MlNodes[0].MlNodes {
		if len(mn.TimeslotAllocation) > 1 && mn.TimeslotAllocation[1] {
			aSelected = true
			break
		}
	}

	zSelected := false
	for _, mn := range pZ.MlNodes[0].MlNodes {
		if len(mn.TimeslotAllocation) > 1 && mn.TimeslotAllocation[1] {
			zSelected = true
			break
		}
	}

	if aSelected && zSelected { return "BOTH" }
	if aSelected { return "A" }
	if zSelected { return "Z" }
	return "NONE"
}

// TestAllocateMLNodesForPoC_DeterministicShuffle verifies that logic is FAIR (Shuffle Enabled)
func TestAllocateMLNodesForPoC_DeterministicShuffle(t *testing.T) {
	ctx := context.Background()
	pA, pZ, mockKeeper := setupTestEnvironment(t)
	assigner := NewModelAssigner(mockKeeper, DebugLogger{t: t})

	zWins := 0
	aWins := 0

	for i := uint64(1); i <= 20; i++ {
		winner := getAllocatedParticipant(t, ctx, assigner, mockKeeper, pA, pZ, i, "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8")
		if winner == "Z" { zWins++ }
		if winner == "A" { aWins++ }
	}

	t.Logf("[NEW LOGIC] Results (20 Epochs): A wins: %d, Z wins: %d", aWins, zWins)

	require.Greater(t, zWins, 0, "ParticipantZ should win at least once (Shuffle verification)")
	require.Greater(t, aWins, 0, "ParticipantA should win at least once (Shuffle verification)")

	// Determinism Check
	w1 := getAllocatedParticipant(t, ctx, assigner, mockKeeper, pA, pZ, 5, "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8")
	w2 := getAllocatedParticipant(t, ctx, assigner, mockKeeper, pA, pZ, 5, "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8")
	require.Equal(t, w1, w2)
}

