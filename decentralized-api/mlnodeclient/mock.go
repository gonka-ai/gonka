package mlnodeclient

import (
	"context"
	"decentralized-api/logging"
	"sync"
	"testing"

	"github.com/productscience/inference/x/inference/types"
)

// MockClient is a mock implementation of MLNodeClient for testing
type MockClient struct {
	Mu sync.Mutex
	// State tracking
	CurrentState       MLNodeState
	PowStatus          PowState
	InferenceIsHealthy bool

	// GPU state
	GPUDevices []GPUDevice
	DriverInfo *DriverInfo

	// Model management state
	CachedModels      map[string]ModelListItem // key: hf_repo:hf_commit
	DownloadingModels map[string]*DownloadProgress
	DiskSpace         *DiskSpaceInfo

	// Error injection
	StopError             error
	NodeStateError        error
	InferenceHealthError  error
	InferenceUpError      error
	StartTrainingError    error
	GetGPUDevicesError    error
	GetGPUDriverError     error
	CheckModelStatusError error
	DownloadModelError    error
	DeleteModelError      error
	ListModelsError       error
	GetDiskSpaceError     error
	InitGenerateV1Error   error
	InitValidateV1Error   error
	ValidateBatchV1Error  error
	GetPowStatusV1Error   error

	// Call tracking
	StopCalled             int
	NodeStateCalled        int
	InferenceHealthCalled  int
	InferenceUpCalled      int
	StartTrainingCalled    int
	GetGPUDevicesCalled    int
	GetGPUDriverCalled     int
	CheckModelStatusCalled int
	DownloadModelCalled    int
	DeleteModelCalled      int
	ListModelsCalled       int
	GetDiskSpaceCalled     int

	// PoC v1 call tracking
	InitGenerateV1Called  int
	InitValidateV1Called  int
	ValidateBatchV1Called int
	GetPowStatusV1Called  int

	// PoC v2 call tracking
	InitGenerateV2Called int
	GenerateV2Called     int
	GetPowStatusV2Called int
	StopPowV2Called      int

	// Track Init/Validate attempts (for testing)
	InitValidateCalled int

	// PoC v1 state
	PowStatusV1 PowStateV1 // V1 status enum

	// PoC v2 state
	PowStatusV2 string // "IDLE", "GENERATING", etc.

	// Capture parameters
	LastInferenceModel string
	LastInferenceArgs  []string
	LastTrainingParams struct {
		TaskId         uint64
		Participant    string
		NodeId         string
		MasterNodeAddr string
		Rank           int
		WorldSize      int
	}
	LastModelStatusCheck *Model
	LastModelDownload    *Model
	LastModelDelete      *Model
}

// NewMockClient creates a new mock client with default values
func NewMockClient() *MockClient {
	return &MockClient{
		CurrentState:       MlNodeState_STOPPED,
		PowStatus:          POW_STOPPED,
		InferenceIsHealthy: false,
		GPUDevices:         []GPUDevice{},
		CachedModels:       make(map[string]ModelListItem),
		DownloadingModels:  make(map[string]*DownloadProgress),
	}
}

func (m *MockClient) WithTryLock(t *testing.T, f func()) {
	lock := m.Mu.TryLock()
	if !lock {
		t.Fatal("TryLock called more than once")
	} else {
		defer m.Mu.Unlock()
	}

	f()
}

func (m *MockClient) GetInferenceUpCalled() int {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	return m.InferenceUpCalled
}

func (m *MockClient) GetStopCalled() int {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	return m.StopCalled
}

func (m *MockClient) GetNodeStateCalled() int {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	return m.NodeStateCalled
}

func (m *MockClient) GetInferenceHealthCalled() int {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	return m.InferenceHealthCalled
}

func (m *MockClient) Reset() {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	m.CurrentState = MlNodeState_STOPPED
	m.PowStatus = POW_STOPPED
	m.InferenceIsHealthy = false
	m.GPUDevices = []GPUDevice{}
	m.DriverInfo = nil
	m.CachedModels = make(map[string]ModelListItem)
	m.DownloadingModels = make(map[string]*DownloadProgress)
	m.DiskSpace = nil

	m.StopError = nil
	m.NodeStateError = nil
	m.InferenceHealthError = nil
	m.InferenceUpError = nil
	m.StartTrainingError = nil
	m.GetGPUDevicesError = nil
	m.GetGPUDriverError = nil
	m.CheckModelStatusError = nil
	m.DownloadModelError = nil
	m.DeleteModelError = nil
	m.ListModelsError = nil
	m.GetDiskSpaceError = nil
	m.InitGenerateV1Error = nil
	m.InitValidateV1Error = nil
	m.ValidateBatchV1Error = nil
	m.GetPowStatusV1Error = nil

	m.StopCalled = 0
	m.NodeStateCalled = 0
	m.InitValidateCalled = 0
	m.InferenceHealthCalled = 0
	m.InferenceUpCalled = 0
	m.StartTrainingCalled = 0
	m.GetGPUDevicesCalled = 0
	m.GetGPUDriverCalled = 0
	m.CheckModelStatusCalled = 0
	m.DownloadModelCalled = 0
	m.DeleteModelCalled = 0
	m.ListModelsCalled = 0
	m.GetDiskSpaceCalled = 0
	m.InitGenerateV1Called = 0
	m.InitValidateV1Called = 0
	m.ValidateBatchV1Called = 0
	m.GetPowStatusV1Called = 0
	m.InitGenerateV2Called = 0
	m.GenerateV2Called = 0
	m.GetPowStatusV2Called = 0
	m.StopPowV2Called = 0

	m.LastInferenceModel = ""
	m.LastInferenceArgs = nil
	m.LastTrainingParams = struct {
		TaskId         uint64
		Participant    string
		NodeId         string
		MasterNodeAddr string
		Rank           int
		WorldSize      int
	}{}
	m.LastModelStatusCheck = nil
	m.LastModelDownload = nil
	m.LastModelDelete = nil
	m.PowStatusV1 = ""
	m.PowStatusV2 = ""
}

func (m *MockClient) Stop(ctx context.Context) error {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	logging.Info("MockClient. Stop: called", types.Testing)
	m.StopCalled++
	if m.StopError != nil {
		return m.StopError
	}
	m.CurrentState = MlNodeState_STOPPED
	m.PowStatus = POW_STOPPED
	m.InferenceIsHealthy = false
	return nil
}

func (m *MockClient) NodeState(ctx context.Context) (*StateResponse, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.NodeStateCalled++
	if m.NodeStateError != nil {
		return nil, m.NodeStateError
	}
	return &StateResponse{State: m.CurrentState}, nil
}

func (m *MockClient) InferenceHealth(ctx context.Context) (bool, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.InferenceHealthCalled++
	if m.InferenceHealthError != nil {
		return false, m.InferenceHealthError
	}
	return m.InferenceIsHealthy, nil
}

func (m *MockClient) InferenceUp(ctx context.Context, model string, args []string) error {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.InferenceUpCalled++
	m.LastInferenceModel = model
	m.LastInferenceArgs = args
	if m.InferenceUpError != nil {
		return m.InferenceUpError
	}
	m.CurrentState = MlNodeState_INFERENCE
	m.InferenceIsHealthy = true
	return nil
}

func (m *MockClient) GetLoadedModels(ctx context.Context) ([]string, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	// Return the last inference model that was loaded, if any
	if m.LastInferenceModel != "" {
		return []string{m.LastInferenceModel}, nil
	}
	return nil, nil
}

func (m *MockClient) StartTraining(ctx context.Context, taskId uint64, participant string, nodeId string, masterNodeAddr string, rank int, worldSize int) error {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.StartTrainingCalled++
	m.LastTrainingParams.TaskId = taskId
	m.LastTrainingParams.Participant = participant
	m.LastTrainingParams.NodeId = nodeId
	m.LastTrainingParams.MasterNodeAddr = masterNodeAddr
	m.LastTrainingParams.Rank = rank
	m.LastTrainingParams.WorldSize = worldSize
	if m.StartTrainingError != nil {
		return m.StartTrainingError
	}
	m.CurrentState = MlNodeState_TRAIN
	return nil
}

func (m *MockClient) GetTrainingStatus(ctx context.Context) error {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	// Not implemented for now
	return nil
}

// PoC v1 mock methods

func (m *MockClient) InitGenerateV1(ctx context.Context, dto InitDtoV1) error {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	m.InitGenerateV1Called++
	if m.InitGenerateV1Error != nil {
		return m.InitGenerateV1Error
	}

	m.CurrentState = MlNodeState_POW
	m.PowStatusV1 = PowStateV1Generating
	m.InferenceIsHealthy = false
	return nil
}

func (m *MockClient) InitValidateV1(ctx context.Context, dto InitDtoV1) error {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	m.InitValidateV1Called++
	m.InitValidateCalled++
	if m.InitValidateV1Error != nil {
		return m.InitValidateV1Error
	}

	m.CurrentState = MlNodeState_POW
	m.PowStatusV1 = PowStateV1Validating
	return nil
}

func (m *MockClient) ValidateBatchV1(ctx context.Context, batch ProofBatchV1) error {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	m.ValidateBatchV1Called++
	if m.ValidateBatchV1Error != nil {
		return m.ValidateBatchV1Error
	}
	return nil
}

func (m *MockClient) GetPowStatusV1(ctx context.Context) (*PowStatusResponseV1, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	m.GetPowStatusV1Called++
	if m.GetPowStatusV1Error != nil {
		return nil, m.GetPowStatusV1Error
	}

	status := m.PowStatusV1
	if status == "" {
		status = PowStateV1Idle
	}
	return &PowStatusResponseV1{
		Status:             status,
		IsModelInitialized: m.CurrentState == MlNodeState_POW,
	}, nil
}

// GPU operations

func (m *MockClient) GetGPUDevices(ctx context.Context) (*GPUDevicesResponse, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.GetGPUDevicesCalled++
	if m.GetGPUDevicesError != nil {
		return nil, m.GetGPUDevicesError
	}
	return &GPUDevicesResponse{
		Devices: m.GPUDevices,
		Count:   len(m.GPUDevices),
	}, nil
}

func (m *MockClient) GetGPUDriver(ctx context.Context) (*DriverInfo, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.GetGPUDriverCalled++
	if m.GetGPUDriverError != nil {
		return nil, m.GetGPUDriverError
	}
	if m.DriverInfo == nil {
		return &DriverInfo{
			DriverVersion:     "535.104.05",
			CudaDriverVersion: "12.2",
			NvmlVersion:       "12.535.104",
		}, nil
	}
	return m.DriverInfo, nil
}

// Model management operations

func (m *MockClient) CheckModelStatus(ctx context.Context, model Model) (*ModelStatusResponse, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.CheckModelStatusCalled++
	m.LastModelStatusCheck = &model
	if m.CheckModelStatusError != nil {
		return nil, m.CheckModelStatusError
	}

	key := getModelKey(model)

	// Check if downloading
	if progress, ok := m.DownloadingModels[key]; ok {
		return &ModelStatusResponse{
			Model:    model,
			Status:   ModelStatusDownloading,
			Progress: progress,
		}, nil
	}

	// Check if cached
	if item, ok := m.CachedModels[key]; ok {
		return &ModelStatusResponse{
			Model:  model,
			Status: item.Status,
		}, nil
	}

	// Not found
	return &ModelStatusResponse{
		Model:  model,
		Status: ModelStatusNotFound,
	}, nil
}

func (m *MockClient) DownloadModel(ctx context.Context, model Model) (*DownloadStartResponse, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.DownloadModelCalled++
	m.LastModelDownload = &model
	if m.DownloadModelError != nil {
		return nil, m.DownloadModelError
	}

	key := getModelKey(model)

	// Start download
	m.DownloadingModels[key] = &DownloadProgress{
		StartTime:      float64(1728565234),
		ElapsedSeconds: 0,
	}

	return &DownloadStartResponse{
		TaskId: key,
		Status: ModelStatusDownloading,
		Model:  model,
	}, nil
}

func (m *MockClient) DeleteModel(ctx context.Context, model Model) (*DeleteResponse, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.DeleteModelCalled++
	m.LastModelDelete = &model
	if m.DeleteModelError != nil {
		return nil, m.DeleteModelError
	}

	key := getModelKey(model)
	status := "deleted"

	// Check if downloading and cancel
	if _, ok := m.DownloadingModels[key]; ok {
		delete(m.DownloadingModels, key)
		status = "cancelled"
	}

	// Remove from cache
	delete(m.CachedModels, key)

	return &DeleteResponse{
		Status: status,
		Model:  model,
	}, nil
}

func (m *MockClient) ListModels(ctx context.Context) (*ModelListResponse, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.ListModelsCalled++
	if m.ListModelsError != nil {
		return nil, m.ListModelsError
	}

	models := make([]ModelListItem, 0, len(m.CachedModels))
	for _, item := range m.CachedModels {
		models = append(models, item)
	}

	return &ModelListResponse{
		Models: models,
	}, nil
}

func (m *MockClient) GetDiskSpace(ctx context.Context) (*DiskSpaceInfo, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.GetDiskSpaceCalled++
	if m.GetDiskSpaceError != nil {
		return nil, m.GetDiskSpaceError
	}

	if m.DiskSpace == nil {
		return &DiskSpaceInfo{
			CacheSizeGB: 13.0,
			AvailableGB: 465.66,
			CachePath:   "/root/.cache/hub",
		}, nil
	}

	return m.DiskSpace, nil
}

// Helper function to generate model key
func getModelKey(model Model) string {
	if model.HfCommit != nil && *model.HfCommit != "" {
		return model.HfRepo + ":" + *model.HfCommit
	}
	return model.HfRepo + ":latest"
}

// PoC v2 mock methods

func (m *MockClient) InitGenerateV2(ctx context.Context, req PoCInitGenerateRequestV2) (*PoCInitGenerateResponseV2, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	m.InitGenerateV2Called++

	// Update mock state: node is now in PoC generation mode, not inference
	m.CurrentState = MlNodeState_POW
	m.InferenceIsHealthy = false

	// Default success response
	return &PoCInitGenerateResponseV2{
		Status:   "OK",
		Backends: 1,
		NGroups:  1,
	}, nil
}

func (m *MockClient) GenerateV2(ctx context.Context, req PoCGenerateRequestV2) (*PoCGenerateResponseV2, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	m.GenerateV2Called++

	// Default success response
	return &PoCGenerateResponseV2{
		Status:    "queued",
		RequestId: "mock-request-id",
	}, nil
}

func (m *MockClient) GetPowStatusV2(ctx context.Context) (*PoCStatusResponseV2, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	m.GetPowStatusV2Called++

	// Use configured status or default to IDLE
	status := m.PowStatusV2
	if status == "" {
		status = "IDLE"
	}
	return &PoCStatusResponseV2{
		Status: status,
		Backends: []BackendStatusV2{
			{Port: 8000, Status: status},
		},
	}, nil
}

func (m *MockClient) StopPowV2(ctx context.Context) (*PoCStopResponseV2, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	m.StopPowV2Called++

	// Default success response
	return &PoCStopResponseV2{
		Status: "OK",
		Results: []BackendResult{
			{Port: 8000, Status: "stopped"},
		},
	}, nil
}

// SetV2Status sets the v2 status for testing
func (m *MockClient) SetV2Status(status string) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.PowStatusV2 = status
}

// Ensure MockClient implements MLNodeClient
var _ MLNodeClient = (*MockClient)(nil)
