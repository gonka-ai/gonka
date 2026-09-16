package poc

import (
	"context"
	"testing"

	"decentralized-api/broker"
	"decentralized-api/mlnodeclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func preservedValidationNode(status types.HardwareNodeStatus, capability bool) broker.NodeResponse {
	return broker.NodeResponse{
		Node: broker.Node{
			Id:      "preserved",
			Host:    "127.0.0.1",
			NodeNum: 1,
			Models: map[string]broker.ModelArgs{
				"test-model": {},
			},
		},
		State: broker.NodeState{
			CurrentStatus:          status,
			AdminState:             broker.AdminState{Enabled: true},
			PoCValidationInference: capability,
			PreservedModels:        map[string]bool{"test-model": true},
			EpochMLNodes: map[string]types.MLNodeInfo{
				"test-model": {NodeId: "preserved"},
			},
		},
	}
}

func TestFilterNodesForValidation_PreservedConcurrentCapability(t *testing.T) {
	tests := []struct {
		name       string
		status     types.HardwareNodeStatus
		capability bool
		want       bool
	}{
		{name: "explicitly qualified while serving inference", status: types.HardwareNodeStatus_INFERENCE, capability: true, want: true},
		{name: "explicit false", status: types.HardwareNodeStatus_INFERENCE, capability: false, want: false},
		{name: "missing decodes false", status: types.HardwareNodeStatus_INFERENCE, want: false},
		{name: "capability true but not serving inference", status: types.HardwareNodeStatus_POC, capability: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterNodesForValidation(
				[]broker.NodeResponse{preservedValidationNode(tt.status, tt.capability)},
				1,
				types.InferencePhase,
			)
			if tt.want {
				require.Len(t, got, 1)
				require.Equal(t, types.HardwareNodeStatus_INFERENCE, got[0].State.CurrentStatus)
				return
			}
			require.Empty(t, got)
		})
	}
}

func TestDispatchToMLNode_UsesQualifiedPreservedExecutor(t *testing.T) {
	mockClient := mlnodeclient.NewMockClient()
	validator := &OffChainValidator{
		nodeBroker:  &stubNodeBroker{client: mockClient},
		callbackUrl: "http://callback",
	}
	nodes := filterNodesForValidation(
		[]broker.NodeResponse{preservedValidationNode(types.HardwareNodeStatus_INFERENCE, true)},
		1,
		types.InferencePhase,
	)
	work := participantWork{
		address: "participant",
		modelId: "test-model",
		pubKey:  "pubkey",
		verified: []VerifiedArtifact{
			{Nonce: 1, VectorB64: "vector"},
		},
	}
	params := &types.PocParams{Models: []*types.PoCModelConfig{{ModelId: "test-model", SeqLen: 128}}}
	nodeCounter := 0

	result := validator.dispatchToMLNode(context.Background(), &work, nodes, &nodeCounter, 10, "start-hash", params)

	require.Equal(t, validateDispatched, result)
	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	require.Equal(t, 1, mockClient.GenerateV2Called)
	require.Equal(t, 1, mockClient.LastGenerateV2Req.NodeCount)
	require.Equal(t, 1, mockClient.LastGenerateV2Req.NodeId)
}

func TestStopGenerationOnAllNodes_PreservesInferenceReservation(t *testing.T) {
	mockClient := mlnodeclient.NewMockClient()
	validator := &OffChainValidator{nodeBroker: &stubNodeBroker{client: mockClient}}

	validator.stopGenerationOnAllNodes([]broker.NodeResponse{
		preservedValidationNode(types.HardwareNodeStatus_INFERENCE, true),
	})

	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	require.Zero(t, mockClient.StopPowV2Called)
}

func TestStopGenerationOnAllNodes_StillStopsUnreservedExecutor(t *testing.T) {
	mockClient := mlnodeclient.NewMockClient()
	validator := &OffChainValidator{nodeBroker: &stubNodeBroker{client: mockClient}}
	node := preservedValidationNode(types.HardwareNodeStatus_POC, false)
	node.State.PreservedModels = nil

	validator.stopGenerationOnAllNodes([]broker.NodeResponse{node})

	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	require.Equal(t, 1, mockClient.StopPowV2Called)
}
