package mlnodeclient

// MockClient is a mock implementation of MLNodeClient for testing
type MockClient struct {
	// State tracking
	CurrentState MLNodeState
	PowStatus    PowState
	IsHealthy    bool

	// Error injection
	StopError            error
	NodeStateError       error
	GetPowStatusError    error
	InitGenerateError    error
	InferenceHealthError error
	InferenceUpError     error
	StartTrainingError   error

	// Call tracking
	StopCalled            int
	NodeStateCalled       int
	GetPowStatusCalled    int
	InitGenerateCalled    int
	InferenceHealthCalled int
	InferenceUpCalled     int
	StartTrainingCalled   int

	// Capture parameters
	LastInitDto        *InitDto
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
}

// NewMockClient creates a new mock client with default values
func NewMockClient() *MockClient {
	return &MockClient{
		CurrentState: MlNodeState_STOPPED,
		PowStatus:    POW_IDLE,
		IsHealthy:    true,
	}
}

func (m *MockClient) Stop() error {
	m.StopCalled++
	if m.StopError != nil {
		return m.StopError
	}
	m.CurrentState = MlNodeState_STOPPED
	return nil
}

func (m *MockClient) NodeState() (*StateResponse, error) {
	m.NodeStateCalled++
	if m.NodeStateError != nil {
		return nil, m.NodeStateError
	}
	return &StateResponse{State: m.CurrentState}, nil
}

func (m *MockClient) GetPowStatus() (*PowStatusResponse, error) {
	m.GetPowStatusCalled++
	if m.GetPowStatusError != nil {
		return nil, m.GetPowStatusError
	}
	return &PowStatusResponse{
		Status:             m.PowStatus,
		IsModelInitialized: m.PowStatus == POW_GENERATING,
	}, nil
}

func (m *MockClient) InitGenerate(dto InitDto) error {
	m.InitGenerateCalled++
	m.LastInitDto = &dto
	if m.InitGenerateError != nil {
		return m.InitGenerateError
	}
	m.CurrentState = MlNodeState_POW
	m.PowStatus = POW_GENERATING
	return nil
}

func (m *MockClient) InferenceHealth() (bool, error) {
	m.InferenceHealthCalled++
	if m.InferenceHealthError != nil {
		return false, m.InferenceHealthError
	}
	return m.IsHealthy, nil
}

func (m *MockClient) InferenceUp(model string, args []string) error {
	m.InferenceUpCalled++
	m.LastInferenceModel = model
	m.LastInferenceArgs = args
	if m.InferenceUpError != nil {
		return m.InferenceUpError
	}
	m.CurrentState = MlNodeState_INFERENCE
	m.IsHealthy = true
	return nil
}

func (m *MockClient) StartTraining(taskId uint64, participant string, nodeId string, masterNodeAddr string, rank int, worldSize int) error {
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

func (m *MockClient) GetTrainingStatus() error {
	// Not implemented for now
	return nil
}

// Ensure MockClient implements MLNodeClient
var _ MLNodeClient = (*MockClient)(nil)
