package broker

import (
	"decentralized-api/chainphase"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

// newPhaseTrackerWithPhase creates a broker with a phase tracker initialized to a specific phase.
func newPhaseTrackerWithPhase(t *testing.T, phase types.EpochPhase) *chainphase.ChainPhaseTracker {
	tracker := chainphase.NewChainPhaseTracker()

	// These params will result in the following phases for given block heights:
	epoch := &types.Epoch{Index: 1, PocStartBlockHeight: 100}
	params := &types.EpochParams{
		EpochLength:           100,
		EpochMultiplier:       1,
		EpochShift:            0,
		PocStageDuration:      20,
		PocExchangeDuration:   1,
		PocValidationDelay:    2,
		PocValidationDuration: 10,
	}

	var blockHeight int64
	switch phase {
	case types.PoCGeneratePhase:
		blockHeight = 105
	case types.PoCGenerateWindDownPhase:
		blockHeight = 122
	case types.PoCValidatePhase:
		blockHeight = 130
	case types.PoCValidateWindDownPhase:
		blockHeight = 137
	case types.InferencePhase:
		blockHeight = 145 // After all PoC phases
	default:
		// A phase that isn't one of the main ones, e.g., Prepare
		blockHeight = 95
	}

	block := chainphase.BlockInfo{Height: blockHeight}
	tracker.Update(block, epoch, params, true)

	return tracker
}

func TestStartPocCommand_Success(t *testing.T) {
	node1 := createTestNode("node-1")
	node2 := createTestNode("node-2")

	tracker := newPhaseTrackerWithPhase(t, types.PoCGeneratePhase)
	require.Equal(t, types.PoCGeneratePhase, tracker.GetCurrentEpochState().CurrentPhase)

	broker := &Broker{
		nodes: map[string]*NodeWithState{
			"node-1": node1,
			"node-2": node2,
		},
		phaseTracker: tracker,
	}

	cmd := StartPocCommand{
		Response: make(chan bool, 1),
	}

	cmd.Execute(broker)

	success := <-cmd.Response
	assert.True(t, success, "Command should succeed")

	assert.Equal(t, types.HardwareNodeStatus_POC, node1.State.IntendedStatus)
	assert.Equal(t, types.HardwareNodeStatus_POC, node2.State.IntendedStatus)
}

func TestStartPocCommand_AlreadyInPoC(t *testing.T) {
	node := createTestNode("node-1")

	tracker := newPhaseTrackerWithPhase(t, types.PoCGeneratePhase)
	require.Equal(t, types.PoCGeneratePhase, tracker.GetCurrentEpochState().CurrentPhase)

	broker := &Broker{
		nodes: map[string]*NodeWithState{
			"node-1": node,
		},
		phaseTracker: tracker,
	}

	// Execute StartPocCommand
	cmd := StartPocCommand{
		Response: make(chan bool, 1),
	}

	cmd.Execute(broker)

	require.Equal(t, types.HardwareNodeStatus_POC, node.State.IntendedStatus)
	require.Equal(t, PocStatusGenerating, node.State.PocIntendedStatus)
}

func TestStartPocCommand_AdminDisabled(t *testing.T) {
	node1 := createTestNode("node-1")
	node2 := createTestNode("node-2")
	node1.State.AdminState.Enabled = false
	node1.State.AdminState.Epoch = 0

	tracker := newPhaseTrackerWithPhase(t, types.PoCGeneratePhase)
	require.Equal(t, uint64(1), tracker.GetCurrentEpochState().LatestEpoch.EpochIndex)
	require.Equal(t, types.PoCGeneratePhase, tracker.GetCurrentEpochState().CurrentPhase)

	broker := &Broker{
		nodes: map[string]*NodeWithState{
			"node-1": node1,
			"node-2": node2,
		},
		phaseTracker: tracker,
	}

	cmd := StartPocCommand{
		Response: make(chan bool, 1),
	}

	cmd.Execute(broker)

	success := <-cmd.Response
	require.True(t, success, "Command should succeed")

	require.Equal(t, node1.State.IntendedStatus, types.HardwareNodeStatus_STOPPED)
	require.Equal(t, node2.State.IntendedStatus, types.HardwareNodeStatus_POC)
	require.Equal(t, node2.State.PocIntendedStatus, PocStatusGenerating)
}
