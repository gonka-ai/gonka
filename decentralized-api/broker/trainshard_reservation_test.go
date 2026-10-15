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

func TestEnsureReservedNodesCached_HoldsEveryNodeTheChainStillReserves(t *testing.T) {
	ours := createTestNodeWithStatus("ours", types.HardwareNodeStatus_INFERENCE)
	ours.State.LockCount = 1
	returning := createTestNodeWithStatus("returning", types.HardwareNodeStatus_INFERENCE)
	free := createTestNodeWithStatus("free", types.HardwareNodeStatus_INFERENCE)
	bridge := &MockBrokerChainBridge{}
	bridge.On("GetTrainingNodeReservations", reservingHost).Return(&types.QueryTrainingNodeReservationsResponse{
		NodeIds: []string{"ours", "returning", "not-ours"},
	}, nil)
	broker := reservationBroker(bridge, ours, returning, free)

	require.NoError(t, broker.EnsureReservedNodesCached())

	for _, held := range []*NodeWithState{ours, returning} {
		assert.True(t, held.State.Reserved, held.Node.Id)
		assert.Equal(t, types.HardwareNodeStatus_STOPPED, held.State.IntendedStatus, held.Node.Id)
	}
	assert.False(t, free.State.Reserved)
	assert.Equal(t, types.HardwareNodeStatus_INFERENCE, free.State.IntendedStatus)
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

func TestEpochCommands_HandOutNothingUntilTheReservationsAreReadAfterAStart(t *testing.T) {
	cases := []struct {
		name    string
		phase   types.EpochPhase
		execute func(*Broker)
		want    types.HardwareNodeStatus
	}{
		{
			name:    "start poc",
			phase:   types.PoCGeneratePhase,
			execute: func(b *Broker) { NewStartPocCommand().Execute(b) },
			want:    types.HardwareNodeStatus_POC,
		},
		{
			name:    "init validate",
			phase:   types.PoCGenerateWindDownPhase,
			execute: func(b *Broker) { NewInitValidateCommand().Execute(b) },
			want:    types.HardwareNodeStatus_POC,
		},
		{
			name:    "inference up",
			phase:   types.InferencePhase,
			execute: func(b *Broker) { NewInferenceUpAllCommand().Execute(b) },
			want:    types.HardwareNodeStatus_INFERENCE,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leased := createTestNodeWithStatus("leased", types.HardwareNodeStatus_STOPPED)
			leased.State.IntendedStatus = types.HardwareNodeStatus_UNKNOWN
			free := createTestNode("free")
			bridge := &MockBrokerChainBridge{}
			bridge.On("GetTrainingNodeReservations", reservingHost).Return(nil, errors.New("connection refused")).Once()
			bridge.On("GetTrainingNodeReservations", reservingHost).Return(&types.QueryTrainingNodeReservationsResponse{
				NodeIds: []string{"leased"},
			}, nil)
			broker := reservationBroker(bridge, leased, free)
			broker.phaseTracker = newPhaseTrackerWithPhase(t, tc.phase)
			broker.awaitingReservations.Store(true)

			require.Error(t, broker.EnsureReservedNodesCached())
			tc.execute(broker)

			assert.Equal(t, types.HardwareNodeStatus_UNKNOWN, leased.State.IntendedStatus, "a node the chain may still lease gets no work")
			assert.Equal(t, types.HardwareNodeStatus_UNKNOWN, free.State.IntendedStatus)

			require.NoError(t, broker.EnsureReservedNodesCached())
			tc.execute(broker)

			assert.Equal(t, types.HardwareNodeStatus_STOPPED, leased.State.IntendedStatus)
			assert.Equal(t, tc.want, free.State.IntendedStatus)
		})
	}
}

func TestEnsureReservedNodesCached_ReturnsANodeOnlyWhenTheOperatorDoesNotHoldIt(t *testing.T) {
	free := createTestNodeWithStatus("free", types.HardwareNodeStatus_STOPPED)
	free.State.Reserved = true
	held := createTestNodeWithStatus("held", types.HardwareNodeStatus_STOPPED)
	held.State.Reserved = true
	held.State.AdminState.Stopped = true
	bridge := &MockBrokerChainBridge{}
	bridge.On("GetTrainingNodeReservations", reservingHost).Return(&types.QueryTrainingNodeReservationsResponse{}, nil)
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
	bridge.On("GetTrainingNodeReservations", reservingHost).Return(nil, errors.New("connection refused"))
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

func TestSetNodeStoppedCommand_LiftingBeforeTheReservationsAreReadKeepsTheNodeStopped(t *testing.T) {
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_STOPPED)
	node.State.AdminState.Stopped = true
	broker := &Broker{nodes: map[string]*NodeWithState{"node-1": node}, reconcileTrigger: make(chan struct{}, 1)}
	broker.awaitingReservations.Store(true)

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

func TestStartPocCommand_UnderChallengeKeepsAReservedNodeStopped(t *testing.T) {
	reserved := createTestNodeWithStatus("reserved", types.HardwareNodeStatus_STOPPED)
	reserved.State.Reserved = true
	free := createTestNode("free")
	free.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE
	withOverlay(t, stubChallengeOverlay{
		self: "me",
		ch:   testOpenCh("me", 500, 900, true, 1),
	})
	broker := &Broker{
		nodes:        map[string]*NodeWithState{"reserved": reserved, "free": free},
		phaseTracker: newPhaseTrackerWithPhase(t, types.InferencePhase),
	}

	cmd := NewStartPocCommand()
	cmd.Execute(broker)

	assert.Equal(t, types.HardwareNodeStatus_STOPPED, reserved.State.IntendedStatus)
	assert.False(t, challengeGenerateNeedsDispatch(reserved, *broker.phaseTracker.GetCurrentEpochState()))
	assert.Equal(t, types.HardwareNodeStatus_POC, free.State.IntendedStatus)
	assert.Equal(t, PocStatusGenerating, free.State.PocIntendedStatus)
}
