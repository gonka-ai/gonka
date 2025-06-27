package mlnodeclient

import (
	"context"
	"decentralized-api/logging"
	"errors"
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

	// Error injection
	StopError            error
	NodeStateError       error
	GetPowStatusError    error
	InitGenerateError    error
	InitValidateError    error
	ValiateBatchError    error
	InferenceHealthError error
	InferenceUpError     error
	StartTrainingError   error

	// Call tracking
	StopCalled            int
	NodeStateCalled       int
	GetPowStatusCalled    int
	InitGenerateCalled    int
	InitValidateCalled    int
	ValidateBatchCalled   int
	InferenceHealthCalled int
	InferenceUpCalled     int
	StartTrainingCalled   int

	// Capture parameters
	LastInitDto         *InitDto
	LastInitValidateDto *InitDto
	LastValidateBatch   ProofBatch
	LastInferenceModel  string
	LastInferenceArgs   []string
	LastTrainingParams  struct {
		TaskId         uint64
		Participant    string
		NodeId         string
		MasterNodeAddr string
		Rank           int
		WorldSize      int
	}
}

// NewMockClient creates a new mock client with default values
func NewMockClient() *MockClient {
	return &MockClient{
		CurrentState:       MlNodeState_STOPPED,
		PowStatus:          POW_STOPPED,
		InferenceIsHealthy: false,
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

func (m *MockClient) GetPowStatus(ctx context.Context) (*PowStatusResponse, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.GetPowStatusCalled++
	if m.GetPowStatusError != nil {
		return nil, m.GetPowStatusError
	}
	return &PowStatusResponse{
		Status:             m.PowStatus,
		IsModelInitialized: m.PowStatus == POW_GENERATING,
	}, nil
}

func (m *MockClient) InitGenerate(ctx context.Context, dto InitDto) error {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	if m.CurrentState != MlNodeState_STOPPED {
		return errors.New("InitGenerate called with invalid state. Expected STOPPED. Actual: currentState =" + string(m.CurrentState))
	}

	logging.Info("MockClient. InitGenerate: called", types.Testing)
	m.InitGenerateCalled++
	m.LastInitDto = &dto
	if m.InitGenerateError != nil {
		return m.InitGenerateError
	}
	m.CurrentState = MlNodeState_POW
	m.PowStatus = POW_GENERATING
	return nil
}

func (m *MockClient) InitValidate(ctx context.Context, dto InitDto) error {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	if m.CurrentState != MlNodeState_POW ||
		m.PowStatus != POW_GENERATING {
		return errors.New("InitValidate called with invalid state. Expected MlNodeState_POW and POW_GENERATING. Actual: currentState = " + string(m.CurrentState) + ". powStatus =" + string(m.PowStatus))
	}

	logging.Info("MockClient. InitValidate: called", types.Testing)
	m.InitValidateCalled++
	m.LastInitValidateDto = &dto
	if m.InitValidateError != nil {
		return m.InitValidateError
	}
	m.CurrentState = MlNodeState_POW
	m.PowStatus = POW_VALIDATING
	return nil
}

func (m *MockClient) ValidateBatch(ctx context.Context, batch ProofBatch) error {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	if m.CurrentState != MlNodeState_POW ||
		m.PowStatus != POW_VALIDATING {
		return errors.New("ValidateBatch called with invalid state. Expected MlNodeState_POW and POW_VALIDATING. Actual: currentState = " + string(m.CurrentState) + ". powStatus =" + string(m.PowStatus))
	}

	m.ValidateBatchCalled++
	m.LastValidateBatch = batch
	if m.ValiateBatchError != nil {
		return m.ValiateBatchError
	}
	m.CurrentState = MlNodeState_POW
	m.PowStatus = POW_VALIDATING
	return nil
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

// Ensure MockClient implements MLNodeClient
var _ MLNodeClient = (*MockClient)(nil)
