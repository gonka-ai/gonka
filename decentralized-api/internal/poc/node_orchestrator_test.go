package poc

import (
	"context"
	"sync"
	"testing"
	"time"

	"decentralized-api/broker"
	"decentralized-api/mlnodeclient"

	"github.com/productscience/inference/x/inference/types"
)

// fakeNodeClient satisfies mlnodeclient.MLNodeClient but is only used to compile tests.
type fakeNodeClient struct{}

func (f fakeNodeClient) StartTraining(ctx context.Context, taskId uint64, participant string, nodeId string, masterNodeAddr string, rank int, worldSize int) error {
	return nil
}
func (f fakeNodeClient) GetTrainingStatus(ctx context.Context) error { return nil }
func (f fakeNodeClient) Stop(ctx context.Context) error              { return nil }
func (f fakeNodeClient) NodeState(ctx context.Context) (*mlnodeclient.StateResponse, error) {
	return &mlnodeclient.StateResponse{}, nil
}
func (f fakeNodeClient) GetPowStatus(ctx context.Context) (*mlnodeclient.PowStatusResponse, error) {
	return &mlnodeclient.PowStatusResponse{}, nil
}
func (f fakeNodeClient) InitGenerate(ctx context.Context, dto mlnodeclient.InitDto) error { return nil }
func (f fakeNodeClient) InitValidate(ctx context.Context, dto mlnodeclient.InitDto) error { return nil }
func (f fakeNodeClient) ValidateBatch(ctx context.Context, batch mlnodeclient.ProofBatch) error {
	return nil
}
func (f fakeNodeClient) InferenceHealth(ctx context.Context) (bool, error) { return true, nil }
func (f fakeNodeClient) InferenceUp(ctx context.Context, model string, args []string) error {
	return nil
}
func (f fakeNodeClient) GetGPUDevices(ctx context.Context) (*mlnodeclient.GPUDevicesResponse, error) {
	return &mlnodeclient.GPUDevicesResponse{}, nil
}
func (f fakeNodeClient) GetGPUDriver(ctx context.Context) (*mlnodeclient.DriverInfo, error) {
	return &mlnodeclient.DriverInfo{}, nil
}
func (f fakeNodeClient) CheckModelStatus(ctx context.Context, model mlnodeclient.Model) (*mlnodeclient.ModelStatusResponse, error) {
	return &mlnodeclient.ModelStatusResponse{}, nil
}
func (f fakeNodeClient) DownloadModel(ctx context.Context, model mlnodeclient.Model) (*mlnodeclient.DownloadStartResponse, error) {
	return &mlnodeclient.DownloadStartResponse{}, nil
}
func (f fakeNodeClient) DeleteModel(ctx context.Context, model mlnodeclient.Model) (*mlnodeclient.DeleteResponse, error) {
	return &mlnodeclient.DeleteResponse{}, nil
}
func (f fakeNodeClient) ListModels(ctx context.Context) (*mlnodeclient.ModelListResponse, error) {
	return &mlnodeclient.ModelListResponse{}, nil
}
func (f fakeNodeClient) GetDiskSpace(ctx context.Context) (*mlnodeclient.DiskSpaceInfo, error) {
	return &mlnodeclient.DiskSpaceInfo{}, nil
}

type fakeBroker struct {
	mu    sync.Mutex
	nodes []broker.NodeResponse
}

func (f *fakeBroker) setNodes(nodes []broker.NodeResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes = nodes
}

func (f *fakeBroker) GetNodes() ([]broker.NodeResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]broker.NodeResponse, len(f.nodes))
	copy(out, f.nodes)
	return out, nil
}

func (f *fakeBroker) NewNodeClient(node *broker.Node) mlnodeclient.MLNodeClient {
	return fakeNodeClient{}
}

func TestGetNodesForPocValidationWithConfig_RetriesThenSuccess(t *testing.T) {
	fb := &fakeBroker{}

	// Initial state: nodes are not in PoC validation, so filter should return none.
	fb.setNodes([]broker.NodeResponse{
		{
			Node: broker.Node{Id: "node-1"},
			State: broker.NodeState{
				CurrentStatus: types.HardwareNodeStatus_INFERENCE,
			},
		},
	})

	orch := &NodePoCOrchestratorImpl{
		pubKey:     "pk",
		nodeBroker: fb,
	}

	_, err := orch.getNodesForPocValidationWithConfig(100, 2, 10*time.Millisecond)
	if err == nil {
		t.Fatalf("expected error when no nodes are in PoC validation")
	}

	// Update state to PoC validating so the next attempt succeeds.
	fb.setNodes([]broker.NodeResponse{
		{
			Node: broker.Node{Id: "node-1"},
			State: broker.NodeState{
				CurrentStatus:     types.HardwareNodeStatus_POC,
				PocCurrentStatus:  broker.PocStatusValidating,
				PocIntendedStatus: broker.PocStatusValidating,
			},
		},
	})

	nodes, err := orch.getNodesForPocValidationWithConfig(100, 2, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node after enabling validation state, got %d", len(nodes))
	}
}
