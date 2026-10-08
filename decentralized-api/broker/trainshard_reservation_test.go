package broker

import (
	"decentralized-api/participant"
	"errors"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const reservingHost = "gonka1host"

func reservationBroker(bridge *MockBrokerChainBridge, nodes ...*NodeWithState) *Broker {
	byID := make(map[string]*NodeWithState, len(nodes))
	for _, node := range nodes {
		byID[node.Node.Id] = node
	}
	return &Broker{
		nodes:            byID,
		chainBridge:      bridge,
		participantInfo:  &participant.CosmosInfo{Address: reservingHost},
		reconcileTrigger: make(chan struct{}, 1),
	}
}

func activeShard(status types.TrainshardStatus, nodes ...*types.TrainshardReservedNode) *types.Trainshard {
	return &types.Trainshard{Status: status, Nodes: nodes}
}

func reservedNode(participant, nodeID string, status types.TrainshardNodeStatus) *types.TrainshardReservedNode {
	return &types.TrainshardReservedNode{Participant: participant, NodeId: nodeID, Status: status}
}

func TestEnsureReservedNodesCached_HoldsOnlyThisHostsActiveReservations(t *testing.T) {
	const (
		shardActive = types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE
		shardOver   = types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED
		nodeActive  = types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE
		nodeKicked  = types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_AUTOKICKED
	)
	ours := createTestNodeWithStatus("ours", types.HardwareNodeStatus_INFERENCE)
	ours.State.LockCount = 1
	sameIDElsewhere := createTestNodeWithStatus("same-id", types.HardwareNodeStatus_INFERENCE)
	kicked := createTestNodeWithStatus("kicked", types.HardwareNodeStatus_INFERENCE)
	settled := createTestNodeWithStatus("settled", types.HardwareNodeStatus_INFERENCE)
	bridge := &MockBrokerChainBridge{}
	bridge.On("GetActiveTrainshards").Return(&types.QueryActiveTrainshardsResponse{Trainshards: []*types.Trainshard{
		activeShard(shardActive,
			reservedNode(reservingHost, "ours", nodeActive),
			reservedNode("gonka1other", "same-id", nodeActive),
			reservedNode(reservingHost, "kicked", nodeKicked)),
		activeShard(shardOver, reservedNode(reservingHost, "settled", nodeActive)),
	}}, nil)
	broker := reservationBroker(bridge, ours, sameIDElsewhere, kicked, settled)

	require.NoError(t, broker.EnsureReservedNodesCached())

	assert.True(t, ours.State.Reserved)
	assert.Equal(t, types.HardwareNodeStatus_STOPPED, ours.State.IntendedStatus, "reserved: out of inference at once, the stop waits for the in-flight request")
	for _, free := range []*NodeWithState{sameIDElsewhere, kicked, settled} {
		assert.False(t, free.State.Reserved, free.Node.Id)
		assert.Equal(t, types.HardwareNodeStatus_INFERENCE, free.State.IntendedStatus, free.Node.Id)
	}
	select {
	case <-broker.reconcileTrigger:
	default:
		t.Fatal("a new reservation must wake the reconciler so the node is stopped before the next phase command")
	}
}

func TestEpochCommands_KeepAReservedNodeOutOfPoCEvenWhenPreserved(t *testing.T) {
	reserved := createTestNodeWithStatus("reserved", types.HardwareNodeStatus_STOPPED)
	reserved.State.Reserved = true
	reserved.State.PreservedModels = map[string]bool{"model-a": true}
	other := createTestNode("other")
	broker := &Broker{
		nodes:        map[string]*NodeWithState{"reserved": reserved, "other": other},
		phaseTracker: newPhaseTrackerWithPhase(t, types.PoCGeneratePhase),
	}

	cmd := StartPocCommand{Response: make(chan bool, 1)}
	cmd.Execute(broker)

	assert.True(t, <-cmd.Response)
	assert.Equal(t, types.HardwareNodeStatus_STOPPED, reserved.State.IntendedStatus)
	assert.Equal(t, types.HardwareNodeStatus_POC, other.State.IntendedStatus)
}

func TestEnsureReservedNodesCached_ReturnsANodeOnlyWhenTheOperatorDoesNotHoldIt(t *testing.T) {
	free := createTestNodeWithStatus("free", types.HardwareNodeStatus_STOPPED)
	free.State.Reserved = true
	held := createTestNodeWithStatus("held", types.HardwareNodeStatus_STOPPED)
	held.State.Reserved = true
	held.State.AdminState.Stopped = true
	bridge := &MockBrokerChainBridge{}
	bridge.On("GetActiveTrainshards").Return(&types.QueryActiveTrainshardsResponse{}, nil)
	broker := reservationBroker(bridge, free, held)

	require.NoError(t, broker.EnsureReservedNodesCached())

	assert.False(t, free.State.Reserved)
	assert.Equal(t, types.HardwareNodeStatus_INFERENCE, free.State.IntendedStatus)
	assert.False(t, held.State.Reserved)
	assert.Equal(t, types.HardwareNodeStatus_STOPPED, held.State.IntendedStatus, "the daemon still cleans up after the run")
}

func TestEnsureReservedNodesCached_KeepsTheLastAnswerWhenTheChainCannotBeRead(t *testing.T) {
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_STOPPED)
	node.State.Reserved = true
	bridge := &MockBrokerChainBridge{}
	bridge.On("GetActiveTrainshards").Return(nil, errors.New("connection refused"))
	broker := reservationBroker(bridge, node)

	assert.Error(t, broker.EnsureReservedNodesCached())

	assert.True(t, node.State.Reserved)
	assert.Equal(t, types.HardwareNodeStatus_STOPPED, node.State.IntendedStatus)
}

func TestSetNodeStoppedCommand_LiftingKeepsAReservedNodeStopped(t *testing.T) {
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_STOPPED)
	node.State.AdminState.Stopped = true
	node.State.Reserved = true
	broker := &Broker{nodes: map[string]*NodeWithState{"node-1": node}}

	start := SetNodeStoppedCommand{NodeId: "node-1", Stopped: false, Response: make(chan error, 1)}
	start.Execute(broker)

	require.NoError(t, <-start.Response)
	assert.False(t, node.State.AdminState.Stopped)
	assert.Equal(t, types.HardwareNodeStatus_STOPPED, node.State.IntendedStatus)
}

func TestGetCommandForState_StopsAReservedNode(t *testing.T) {
	broker := NewTestBroker()
	reserved := &NodeState{IntendedStatus: types.HardwareNodeStatus_STOPPED, Reserved: true}

	_, ok := broker.getCommandForState("node-1", reserved, nil, nil, nil, 1, nil).(StopNodeCommand)

	assert.True(t, ok)
}
