package broker

import (
	"decentralized-api/apiconfig"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// mockChainBridgeForRegisterNode is a minimal mock implementing BrokerChainBridge
// for use in RegisterNode tests. Only methods needed by the test are implemented.
type mockChainBridgeForRegisterNode struct{}

func (m *mockChainBridgeForRegisterNode) GetHardwareNodes() (*types.QueryHardwareNodesResponse, error) {
	return nil, nil
}

func (m *mockChainBridgeForRegisterNode) SubmitHardwareDiff(diff *types.MsgSubmitHardwareDiff) error {
	return nil
}

func (m *mockChainBridgeForRegisterNode) GetBlockHash(height int64) (string, error) {
	return "", nil
}

func (m *mockChainBridgeForRegisterNode) GetGovernanceModels() (*types.QueryModelsAllResponse, error) {
	// Return an empty governance model list; for the empty-ID case we should
	// fail before we ever consult governance models.
	return &types.QueryModelsAllResponse{Model: []types.Model{}}, nil
}

func (m *mockChainBridgeForRegisterNode) GetCurrentEpochGroupData() (*types.QueryCurrentEpochGroupDataResponse, error) {
	return nil, nil
}

func (m *mockChainBridgeForRegisterNode) GetEpochGroupDataByModelId(pocHeight uint64, modelId string) (*types.QueryGetEpochGroupDataResponse, error) {
	return nil, nil
}

// Ensure mock satisfies BrokerChainBridge.
var _ BrokerChainBridge = (*mockChainBridgeForRegisterNode)(nil)

func TestRegisterNode_EmptyID(t *testing.T) {

	respCh := make(chan *apiconfig.InferenceNodeConfig, 1)
	cmd := RegisterNode{
		Node: apiconfig.InferenceNodeConfig{
			Id: "",
		},
		Response: respCh,
	}

	broker := &Broker{
		chainBridge: &mockChainBridgeForRegisterNode{},
		nodes:       make(map[string]*NodeWithState),
	}

	cmd.Execute(broker)

	res := <-respCh
	require.Nil(t, res, "expected nil response for empty node ID")
	require.Empty(t, broker.nodes, "broker should not register node with empty ID")
}
