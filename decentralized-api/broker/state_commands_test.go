package broker

import (
	"github.com/stretchr/testify/require"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

func TestStartPocCommand_Success(t *testing.T) {
	node1 := createTestNode("node-1")
	node2 := createTestNode("node-2")

	broker := &Broker{
		nodes: map[string]*NodeWithState{
			"node-1": node1,
			"node-2": node2,
		},
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
	broker := &Broker{
		nodes: map[string]*NodeWithState{
			"node-1": node,
		},
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
	node1.State.AdminState.Epoch = 3

	broker := &Broker{
		nodes: map[string]*NodeWithState{
			"node-1": node1,
			"node-2": node2,
		},
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
