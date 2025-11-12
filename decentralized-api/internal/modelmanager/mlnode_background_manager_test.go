package modelmanager

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/mlnodeclient"
	"errors"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

// Mock ConfigManager
type mockConfigManager struct {
	nodes              []apiconfig.InferenceNodeConfig
	currentNodeVersion string
	setNodesError      error
}

func (m *mockConfigManager) GetNodes() []apiconfig.InferenceNodeConfig {
	return m.nodes
}

func (m *mockConfigManager) GetCurrentNodeVersion() string {
	return m.currentNodeVersion
}

func (m *mockConfigManager) SetNodes(nodes []apiconfig.InferenceNodeConfig) error {
	if m.setNodesError != nil {
		return m.setNodesError
	}
	m.nodes = nodes
	return nil
}

// Mock Broker
type mockBroker struct {
	queuedCommands []broker.Command
	queueError     error
	executeError   error
}

func (m *mockBroker) QueueMessage(cmd broker.Command) error {
	if m.queueError != nil {
		return m.queueError
	}
	m.queuedCommands = append(m.queuedCommands, cmd)

	// Execute command immediately for testing
	if updateCmd, ok := cmd.(broker.UpdateNodeHardwareCommand); ok {
		if m.executeError != nil {
			updateCmd.Response <- m.executeError
		} else {
			updateCmd.Response <- nil
		}
	}
	return nil
}

// Mock PhaseTracker
type mockPhaseTracker struct {
	epochState *chainphase.EpochState
}

func (m *mockPhaseTracker) GetCurrentEpochState() *chainphase.EpochState {
	return m.epochState
}

// Mock ClientFactory
type mockClientFactory struct {
	client mlnodeclient.MLNodeClient
}

func (m *mockClientFactory) CreateClient(pocUrl, inferenceUrl string, authToken string, baseURL string) mlnodeclient.MLNodeClient {
	return m.client
}

// Custom mock client for testing error handling
type customMockClient struct {
	*mlnodeclient.MockClient
	callCount int
}

func (c *customMockClient) CheckModelStatus(ctx context.Context, model mlnodeclient.Model) (*mlnodeclient.ModelStatusResponse, error) {
	c.callCount++
	if c.callCount == 1 {
		return nil, errors.New("network error")
	}
	return c.MockClient.CheckModelStatus(ctx, model)
}

// Test isInDownloadWindow
func TestIsInDownloadWindow(t *testing.T) {
	manager := &MLNodeBackgroundManager{}

	t.Run("nil epoch state", func(t *testing.T) {
		if manager.isInDownloadWindow(nil) {
			t.Error("expected false for nil epoch state")
		}
	})

	t.Run("not synced", func(t *testing.T) {
		epochState := &chainphase.EpochState{
			IsSynced: false,
		}
		if manager.isInDownloadWindow(epochState) {
			t.Error("expected false for not synced state")
		}
	})

	t.Run("not in inference phase", func(t *testing.T) {
		epochState := &chainphase.EpochState{
			IsSynced:     true,
			CurrentPhase: types.PoCGeneratePhase,
			LatestEpoch: types.EpochContext{
				EpochIndex:          1,
				PocStartBlockHeight: 1000,
				EpochParams: types.EpochParams{
					PocStageDuration:          100,
					PocValidationDuration:     100,
					InferenceValidationCutoff: 200,
					SetNewValidatorsDelay:     50,
				},
			},
			CurrentBlock: chainphase.BlockInfo{Height: 1100},
		}
		if manager.isInDownloadWindow(epochState) {
			t.Error("expected false for non-inference phase")
		}
	})

	t.Run("before window start", func(t *testing.T) {
		epochParams := types.EpochParams{
			PocStageDuration:          1000,
			PocValidationDuration:     1000,
			InferenceValidationCutoff: 200,
			SetNewValidatorsDelay:     100,
		}
		epochState := &chainphase.EpochState{
			IsSynced:     true,
			CurrentPhase: types.InferencePhase,
			LatestEpoch: types.NewEpochContext(
				types.Epoch{
					Index:               1,
					PocStartBlockHeight: 10000,
				},
				epochParams,
			),
			CurrentBlock: chainphase.BlockInfo{Height: 11100}, // Before windowStart (11130)
		}
		if manager.isInDownloadWindow(epochState) {
			t.Error("expected false for block before window start")
		}
	})

	t.Run("after window end", func(t *testing.T) {
		epochParams := types.EpochParams{
			PocStageDuration:          1000,
			PocValidationDuration:     1000,
			InferenceValidationCutoff: 200,
			SetNewValidatorsDelay:     100,
		}
		ec := types.NewEpochContext(
			types.Epoch{
				Index:               1,
				PocStartBlockHeight: 10000,
			},
			epochParams,
		)
		// NextPoCStart = 10000 + 1000 + 1000 = 12000
		// InferenceValidationCutoff = 12000 - 200 = 11800
		// windowEnd = 11800 - 200 = 11600
		epochState := &chainphase.EpochState{
			IsSynced:     true,
			CurrentPhase: types.InferencePhase,
			LatestEpoch:  ec,
			CurrentBlock: chainphase.BlockInfo{Height: 11700}, // After windowEnd (11600)
		}
		if manager.isInDownloadWindow(epochState) {
			t.Error("expected false for block after window end")
		}
	})

	t.Run("inside window", func(t *testing.T) {
		epochParams := types.EpochParams{
			EpochLength:               10000,
			PocStageDuration:          1000,
			PocExchangeDuration:       100,
			PocValidationDelay:        100,
			PocValidationDuration:     1000,
			SetNewValidatorsDelay:     100,
			InferenceValidationCutoff: 500,
		}
		ec := types.NewEpochContext(
			types.Epoch{
				Index:               1,
				PocStartBlockHeight: 10000,
			},
			epochParams,
		)
		// Calculation:
		// getPocAnchor = 10000
		// GetEndOfPoCStage = 0 + 1000 = 1000
		// GetStartOfPoCValidationStage = 1000 + 100 = 1100
		// GetEndOfPoCValidationStage = 1100 + 1000 = 2100
		// GetSetNewValidatorsStage = 2100 + 100 = 2200
		// SetNewValidators = 10000 + 2200 = 12200
		// windowStart = 12200 + 30 = 12230
		// NextPoCStart = 10000 + 10000 = 20000
		// InferenceValidationCutoff = 20000 - 500 = 19500
		// windowEnd = 19500 - 200 = 19300
		epochState := &chainphase.EpochState{
			IsSynced:     true,
			CurrentPhase: types.InferencePhase,
			LatestEpoch:  ec,
			CurrentBlock: chainphase.BlockInfo{Height: 15000}, // Inside window [12230, 19300]
		}
		if !manager.isInDownloadWindow(epochState) {
			t.Error("expected true for block inside window")
		}
	})
}

// Test checkNodeModels
func TestCheckNodeModels(t *testing.T) {
	t.Run("model not found triggers download", func(t *testing.T) {
		mockClient := mlnodeclient.NewMockClient()
		// Don't add to CachedModels - it will return NOT_FOUND by default

		configMgr := &mockConfigManager{
			nodes: []apiconfig.InferenceNodeConfig{
				{
					Id:               "node1",
					Host:             "localhost",
					PoCPort:          8080,
					PoCSegment:       "/api",
					InferencePort:    8081,
					InferenceSegment: "/inference",
					Models: map[string]apiconfig.ModelConfig{
						"test-model": {Args: []string{}},
					},
				},
			},
			currentNodeVersion: "",
		}

		factory := &mockClientFactory{client: mockClient}

		manager := NewMLNodeBackgroundManager(
			configMgr,
			nil,
			&mockBroker{},
			factory,
			30*time.Minute,
		)

		manager.checkNodeModels(configMgr.nodes[0])

		if mockClient.CheckModelStatusCalled != 1 {
			t.Errorf("expected CheckModelStatus to be called once, got %d", mockClient.CheckModelStatusCalled)
		}

		if mockClient.DownloadModelCalled != 1 {
			t.Errorf("expected DownloadModel to be called once, got %d", mockClient.DownloadModelCalled)
		}
	})

	t.Run("partial model triggers download", func(t *testing.T) {
		mockClient := mlnodeclient.NewMockClient()
		mockClient.CachedModels["test-model:latest"] = mlnodeclient.ModelListItem{
			Model: mlnodeclient.Model{
				HfRepo:   "test-model",
				HfCommit: nil,
			},
			Status: mlnodeclient.ModelStatusPartial,
		}

		configMgr := &mockConfigManager{
			nodes: []apiconfig.InferenceNodeConfig{
				{
					Id:               "node1",
					Host:             "localhost",
					PoCPort:          8080,
					PoCSegment:       "/api",
					InferencePort:    8081,
					InferenceSegment: "/inference",
					Models: map[string]apiconfig.ModelConfig{
						"test-model": {Args: []string{}},
					},
				},
			},
			currentNodeVersion: "",
		}

		factory := &mockClientFactory{client: mockClient}

		manager := NewMLNodeBackgroundManager(
			configMgr,
			nil,
			&mockBroker{},
			factory,
			30*time.Minute,
		)

		manager.checkNodeModels(configMgr.nodes[0])

		if mockClient.DownloadModelCalled != 1 {
			t.Errorf("expected DownloadModel to be called once, got %d", mockClient.DownloadModelCalled)
		}
	})

	t.Run("already downloading skips download", func(t *testing.T) {
		mockClient := mlnodeclient.NewMockClient()
		// Add to DownloadingModels to simulate downloading state
		mockClient.DownloadingModels["test-model:latest"] = &mlnodeclient.DownloadProgress{
			StartTime:      1234567890,
			ElapsedSeconds: 100,
		}

		configMgr := &mockConfigManager{
			nodes: []apiconfig.InferenceNodeConfig{
				{
					Id:               "node1",
					Host:             "localhost",
					PoCPort:          8080,
					PoCSegment:       "/api",
					InferencePort:    8081,
					InferenceSegment: "/inference",
					Models: map[string]apiconfig.ModelConfig{
						"test-model": {Args: []string{}},
					},
				},
			},
			currentNodeVersion: "",
		}

		factory := &mockClientFactory{client: mockClient}

		manager := NewMLNodeBackgroundManager(
			configMgr,
			nil,
			&mockBroker{},
			factory,
			30*time.Minute,
		)

		manager.checkNodeModels(configMgr.nodes[0])

		if mockClient.CheckModelStatusCalled != 1 {
			t.Errorf("expected CheckModelStatus to be called once, got %d", mockClient.CheckModelStatusCalled)
		}

		if mockClient.DownloadModelCalled != 0 {
			t.Errorf("expected DownloadModel not to be called, got %d", mockClient.DownloadModelCalled)
		}
	})

	t.Run("already downloaded skips download", func(t *testing.T) {
		mockClient := mlnodeclient.NewMockClient()
		mockClient.CachedModels["test-model:latest"] = mlnodeclient.ModelListItem{
			Model: mlnodeclient.Model{
				HfRepo:   "test-model",
				HfCommit: nil,
			},
			Status: mlnodeclient.ModelStatusDownloaded,
		}

		configMgr := &mockConfigManager{
			nodes: []apiconfig.InferenceNodeConfig{
				{
					Id:               "node1",
					Host:             "localhost",
					PoCPort:          8080,
					PoCSegment:       "/api",
					InferencePort:    8081,
					InferenceSegment: "/inference",
					Models: map[string]apiconfig.ModelConfig{
						"test-model": {Args: []string{}},
					},
				},
			},
			currentNodeVersion: "",
		}

		factory := &mockClientFactory{client: mockClient}

		manager := NewMLNodeBackgroundManager(
			configMgr,
			nil,
			&mockBroker{},
			factory,
			30*time.Minute,
		)

		manager.checkNodeModels(configMgr.nodes[0])

		if mockClient.CheckModelStatusCalled != 1 {
			t.Errorf("expected CheckModelStatus to be called once, got %d", mockClient.CheckModelStatusCalled)
		}

		if mockClient.DownloadModelCalled != 0 {
			t.Errorf("expected DownloadModel not to be called, got %d", mockClient.DownloadModelCalled)
		}
	})

	t.Run("endpoint not implemented stops checking node", func(t *testing.T) {
		mockClient := mlnodeclient.NewMockClient()
		mockClient.CheckModelStatusError = &mlnodeclient.ErrAPINotImplemented{
			Endpoint:   "/api/v1/models/status",
			StatusCode: 404,
		}

		configMgr := &mockConfigManager{
			nodes: []apiconfig.InferenceNodeConfig{
				{
					Id:               "node1",
					Host:             "localhost",
					PoCPort:          8080,
					PoCSegment:       "/api",
					InferencePort:    8081,
					InferenceSegment: "/inference",
					Models: map[string]apiconfig.ModelConfig{
						"model1": {Args: []string{}},
						"model2": {Args: []string{}},
					},
				},
			},
			currentNodeVersion: "",
		}

		factory := &mockClientFactory{client: mockClient}

		manager := NewMLNodeBackgroundManager(
			configMgr,
			nil,
			&mockBroker{},
			factory,
			30*time.Minute,
		)

		manager.checkNodeModels(configMgr.nodes[0])

		// Should only check once and then stop
		if mockClient.CheckModelStatusCalled != 1 {
			t.Errorf("expected CheckModelStatus to be called once, got %d", mockClient.CheckModelStatusCalled)
		}

		if mockClient.DownloadModelCalled != 0 {
			t.Errorf("expected DownloadModel not to be called, got %d", mockClient.DownloadModelCalled)
		}
	})

	t.Run("network error continues to next model", func(t *testing.T) {
		// Create a custom mock that will fail only on first call
		mockClient := &customMockClient{
			MockClient: mlnodeclient.NewMockClient(),
			callCount:  0,
		}

		configMgr := &mockConfigManager{
			nodes: []apiconfig.InferenceNodeConfig{
				{
					Id:               "node1",
					Host:             "localhost",
					PoCPort:          8080,
					PoCSegment:       "/api",
					InferencePort:    8081,
					InferenceSegment: "/inference",
					Models: map[string]apiconfig.ModelConfig{
						"model1": {Args: []string{}},
						"model2": {Args: []string{}},
					},
				},
			},
			currentNodeVersion: "",
		}

		factory := &mockClientFactory{client: mockClient}

		manager := NewMLNodeBackgroundManager(
			configMgr,
			nil,
			&mockBroker{},
			factory,
			30*time.Minute,
		)

		manager.checkNodeModels(configMgr.nodes[0])

		// Should try checking both models despite first error
		if mockClient.callCount != 2 {
			t.Errorf("expected CheckModelStatus to be called twice, got %d", mockClient.callCount)
		}
	})

	t.Run("multiple models in config", func(t *testing.T) {
		mockClient := mlnodeclient.NewMockClient()
		// model1: NOT_FOUND (not in CachedModels)
		// model2: DOWNLOADED
		mockClient.CachedModels["model2:latest"] = mlnodeclient.ModelListItem{
			Model: mlnodeclient.Model{
				HfRepo:   "model2",
				HfCommit: nil,
			},
			Status: mlnodeclient.ModelStatusDownloaded,
		}
		// model3: NOT_FOUND (not in CachedModels)

		configMgr := &mockConfigManager{
			nodes: []apiconfig.InferenceNodeConfig{
				{
					Id:               "node1",
					Host:             "localhost",
					PoCPort:          8080,
					PoCSegment:       "/api",
					InferencePort:    8081,
					InferenceSegment: "/inference",
					Models: map[string]apiconfig.ModelConfig{
						"model1": {Args: []string{}},
						"model2": {Args: []string{}},
						"model3": {Args: []string{}},
					},
				},
			},
			currentNodeVersion: "",
		}

		factory := &mockClientFactory{client: mockClient}

		manager := NewMLNodeBackgroundManager(
			configMgr,
			nil,
			&mockBroker{},
			factory,
			30*time.Minute,
		)

		manager.checkNodeModels(configMgr.nodes[0])

		if mockClient.CheckModelStatusCalled != 3 {
			t.Errorf("expected CheckModelStatus to be called 3 times, got %d", mockClient.CheckModelStatusCalled)
		}

		// Should download 2 models (model1 and model3 are NOT_FOUND)
		if mockClient.DownloadModelCalled != 2 {
			t.Errorf("expected DownloadModel to be called twice, got %d", mockClient.DownloadModelCalled)
		}
	})
}

// Test URL formatting
func TestURLFormatting(t *testing.T) {
	node := apiconfig.InferenceNodeConfig{
		Host:             "localhost",
		PoCPort:          8080,
		PoCSegment:       "/api/v1",
		InferencePort:    8081,
		InferenceSegment: "/inference",
	}

	t.Run("PoC URL without version", func(t *testing.T) {
		url := getPoCUrl(node)
		expected := "http://localhost:8080/api/v1"
		if url != expected {
			t.Errorf("expected %s, got %s", expected, url)
		}
	})

	t.Run("PoC URL with version", func(t *testing.T) {
		url := getPoCUrlVersioned(node, "v2")
		expected := "http://localhost:8080/v2/api/v1"
		if url != expected {
			t.Errorf("expected %s, got %s", expected, url)
		}
	})

	t.Run("Inference URL without version", func(t *testing.T) {
		url := getInferenceUrl(node)
		expected := "http://localhost:8081/inference"
		if url != expected {
			t.Errorf("expected %s, got %s", expected, url)
		}
	})

	t.Run("Inference URL with version", func(t *testing.T) {
		url := getInferenceUrlVersioned(node, "v2")
		expected := "http://localhost:8081/v2/inference"
		if url != expected {
			t.Errorf("expected %s, got %s", expected, url)
		}
	})

	t.Run("URL with version helper", func(t *testing.T) {
		url := getPoCUrlWithVersion(node, "v2")
		expected := "http://localhost:8080/v2/api/v1"
		if url != expected {
			t.Errorf("expected %s, got %s", expected, url)
		}
	})

	t.Run("URL without version helper (empty string)", func(t *testing.T) {
		url := getPoCUrlWithVersion(node, "")
		expected := "http://localhost:8080/api/v1"
		if url != expected {
			t.Errorf("expected %s, got %s", expected, url)
		}
	})
}

// Test GPU transformation
func TestTransformGPUDevicesToHardware(t *testing.T) {
	t.Run("identical GPUs grouped", func(t *testing.T) {
		totalMem := int(24576) // 24GB in MB
		devices := []mlnodeclient.GPUDevice{
			{
				Index:         0,
				Name:          "NVIDIA RTX 3090",
				TotalMemoryMB: &totalMem,
				IsAvailable:   true,
				ErrorMessage:  nil,
			},
			{
				Index:         1,
				Name:          "NVIDIA RTX 3090",
				TotalMemoryMB: &totalMem,
				IsAvailable:   true,
				ErrorMessage:  nil,
			},
		}

		hardware := transformGPUDevicesToHardware(devices)

		if len(hardware) != 1 {
			t.Errorf("expected 1 hardware entry, got %d", len(hardware))
		}

		if hardware[0].Type != "NVIDIA RTX 3090 | 24GB" {
			t.Errorf("expected 'NVIDIA RTX 3090 | 24GB', got %s", hardware[0].Type)
		}

		if hardware[0].Count != 2 {
			t.Errorf("expected count 2, got %d", hardware[0].Count)
		}
	})

	t.Run("mixed GPU types", func(t *testing.T) {
		mem3090 := int(24576)
		mem4090 := int(24576)
		devices := []mlnodeclient.GPUDevice{
			{Name: "NVIDIA RTX 3090", TotalMemoryMB: &mem3090, IsAvailable: true},
			{Name: "NVIDIA RTX 3090", TotalMemoryMB: &mem3090, IsAvailable: true},
			{Name: "NVIDIA RTX 4090", TotalMemoryMB: &mem4090, IsAvailable: true},
		}

		hardware := transformGPUDevicesToHardware(devices)

		if len(hardware) != 2 {
			t.Errorf("expected 2 hardware entries, got %d", len(hardware))
		}

		// Check sorting
		if hardware[0].Type != "NVIDIA RTX 3090 | 24GB" {
			t.Errorf("expected first entry to be RTX 3090, got %s", hardware[0].Type)
		}
		if hardware[1].Type != "NVIDIA RTX 4090 | 24GB" {
			t.Errorf("expected second entry to be RTX 4090, got %s", hardware[1].Type)
		}
	})

	t.Run("skip unavailable GPUs", func(t *testing.T) {
		mem := int(24576)
		devices := []mlnodeclient.GPUDevice{
			{Name: "NVIDIA RTX 3090", TotalMemoryMB: &mem, IsAvailable: false},
			{Name: "NVIDIA RTX 4090", TotalMemoryMB: &mem, IsAvailable: true},
		}

		hardware := transformGPUDevicesToHardware(devices)

		if len(hardware) != 1 {
			t.Errorf("expected 1 hardware entry, got %d", len(hardware))
		}

		if hardware[0].Type != "NVIDIA RTX 4090 | 24GB" {
			t.Errorf("expected RTX 4090, got %s", hardware[0].Type)
		}
	})

	t.Run("skip GPUs with error", func(t *testing.T) {
		mem := int(24576)
		errMsg := "NVML error"
		devices := []mlnodeclient.GPUDevice{
			{Name: "NVIDIA RTX 3090", TotalMemoryMB: &mem, IsAvailable: true, ErrorMessage: &errMsg},
			{Name: "NVIDIA RTX 4090", TotalMemoryMB: &mem, IsAvailable: true, ErrorMessage: nil},
		}

		hardware := transformGPUDevicesToHardware(devices)

		if len(hardware) != 1 {
			t.Errorf("expected 1 hardware entry, got %d", len(hardware))
		}

		if hardware[0].Type != "NVIDIA RTX 4090 | 24GB" {
			t.Errorf("expected RTX 4090, got %s", hardware[0].Type)
		}
	})

	t.Run("skip GPUs without memory info", func(t *testing.T) {
		mem := int(24576)
		devices := []mlnodeclient.GPUDevice{
			{Name: "NVIDIA RTX 3090", TotalMemoryMB: nil, IsAvailable: true},
			{Name: "NVIDIA RTX 4090", TotalMemoryMB: &mem, IsAvailable: true},
		}

		hardware := transformGPUDevicesToHardware(devices)

		if len(hardware) != 1 {
			t.Errorf("expected 1 hardware entry, got %d", len(hardware))
		}
	})

	t.Run("memory conversion MB to GB", func(t *testing.T) {
		mem := int(16384) // 16GB in MB
		devices := []mlnodeclient.GPUDevice{
			{Name: "NVIDIA RTX 3080", TotalMemoryMB: &mem, IsAvailable: true},
		}

		hardware := transformGPUDevicesToHardware(devices)

		if hardware[0].Type != "NVIDIA RTX 3080 | 16GB" {
			t.Errorf("expected 'NVIDIA RTX 3080 | 16GB', got %s", hardware[0].Type)
		}
	})

	t.Run("empty device list", func(t *testing.T) {
		hardware := transformGPUDevicesToHardware([]mlnodeclient.GPUDevice{})

		if len(hardware) != 0 {
			t.Errorf("expected empty hardware list, got %d", len(hardware))
		}
	})
}

// Test GPU check and update
func TestCheckAndUpdateGPUs(t *testing.T) {
	t.Run("successful GPU fetch and update", func(t *testing.T) {
		mem := int(24576)
		mockClient := &mlnodeclient.MockClient{
			GPUDevices: []mlnodeclient.GPUDevice{
				{Name: "NVIDIA RTX 3090", TotalMemoryMB: &mem, IsAvailable: true},
			},
		}

		configMgr := &mockConfigManager{
			nodes: []apiconfig.InferenceNodeConfig{
				{
					Id:               "node1",
					Host:             "localhost",
					PoCPort:          8080,
					PoCSegment:       "/api",
					InferencePort:    8081,
					InferenceSegment: "/inference",
				},
			},
		}

		broker := &mockBroker{}
		factory := &mockClientFactory{client: mockClient}

		manager := NewMLNodeBackgroundManager(
			configMgr,
			nil,
			broker,
			factory,
			30*time.Minute,
		)

		ctx := context.Background()
		manager.checkAndUpdateGPUs(ctx)

		// Verify broker was called
		if len(broker.queuedCommands) != 1 {
			t.Errorf("expected 1 broker command, got %d", len(broker.queuedCommands))
		}

		// Verify config was updated
		if len(configMgr.nodes[0].Hardware) != 1 {
			t.Errorf("expected 1 hardware entry, got %d", len(configMgr.nodes[0].Hardware))
		}

		if configMgr.nodes[0].Hardware[0].Type != "NVIDIA RTX 3090 | 24GB" {
			t.Errorf("unexpected hardware type: %s", configMgr.nodes[0].Hardware[0].Type)
		}
	})

	t.Run("ErrAPINotImplemented handling", func(t *testing.T) {
		mockClient := &mlnodeclient.MockClient{
			GetGPUDevicesError: &mlnodeclient.ErrAPINotImplemented{Endpoint: "/api/v1/gpu/devices"},
		}

		configMgr := &mockConfigManager{
			nodes: []apiconfig.InferenceNodeConfig{
				{Id: "node1", Host: "localhost", PoCPort: 8080, PoCSegment: "/api"},
			},
		}

		broker := &mockBroker{}
		factory := &mockClientFactory{client: mockClient}

		manager := NewMLNodeBackgroundManager(
			configMgr,
			nil,
			broker,
			factory,
			30*time.Minute,
		)

		ctx := context.Background()
		manager.checkAndUpdateGPUs(ctx)

		// Should not call broker
		if len(broker.queuedCommands) != 0 {
			t.Errorf("expected 0 broker commands, got %d", len(broker.queuedCommands))
		}

		// Should not update config
		if len(configMgr.nodes[0].Hardware) != 0 {
			t.Errorf("expected 0 hardware entries, got %d", len(configMgr.nodes[0].Hardware))
		}
	})

	t.Run("network error handling", func(t *testing.T) {
		mockClient := &mlnodeclient.MockClient{
			GetGPUDevicesError: errors.New("network error"),
		}

		configMgr := &mockConfigManager{
			nodes: []apiconfig.InferenceNodeConfig{
				{Id: "node1", Host: "localhost", PoCPort: 8080, PoCSegment: "/api"},
			},
		}

		broker := &mockBroker{}
		factory := &mockClientFactory{client: mockClient}

		manager := NewMLNodeBackgroundManager(
			configMgr,
			nil,
			broker,
			factory,
			30*time.Minute,
		)

		ctx := context.Background()
		manager.checkAndUpdateGPUs(ctx)

		// Should not call broker
		if len(broker.queuedCommands) != 0 {
			t.Errorf("expected 0 broker commands, got %d", len(broker.queuedCommands))
		}
	})

	t.Run("broker queue failure", func(t *testing.T) {
		mem := int(24576)
		mockClient := &mlnodeclient.MockClient{
			GPUDevices: []mlnodeclient.GPUDevice{
				{Name: "NVIDIA RTX 3090", TotalMemoryMB: &mem, IsAvailable: true},
			},
		}

		configMgr := &mockConfigManager{
			nodes: []apiconfig.InferenceNodeConfig{
				{Id: "node1", Host: "localhost", PoCPort: 8080, PoCSegment: "/api"},
			},
		}

		broker := &mockBroker{queueError: errors.New("queue full")}
		factory := &mockClientFactory{client: mockClient}

		manager := NewMLNodeBackgroundManager(
			configMgr,
			nil,
			broker,
			factory,
			30*time.Minute,
		)

		ctx := context.Background()
		manager.checkAndUpdateGPUs(ctx)

		// Config should still be updated
		if len(configMgr.nodes[0].Hardware) != 1 {
			t.Errorf("expected 1 hardware entry, got %d", len(configMgr.nodes[0].Hardware))
		}
	})

	t.Run("config save failure", func(t *testing.T) {
		mem := int(24576)
		mockClient := &mlnodeclient.MockClient{
			GPUDevices: []mlnodeclient.GPUDevice{
				{Name: "NVIDIA RTX 3090", TotalMemoryMB: &mem, IsAvailable: true},
			},
		}

		configMgr := &mockConfigManager{
			nodes: []apiconfig.InferenceNodeConfig{
				{Id: "node1", Host: "localhost", PoCPort: 8080, PoCSegment: "/api"},
			},
			setNodesError: errors.New("disk full"),
		}

		broker := &mockBroker{}
		factory := &mockClientFactory{client: mockClient}

		manager := NewMLNodeBackgroundManager(
			configMgr,
			nil,
			broker,
			factory,
			30*time.Minute,
		)

		ctx := context.Background()
		manager.checkAndUpdateGPUs(ctx)

		// Broker should still be called
		if len(broker.queuedCommands) != 1 {
			t.Errorf("expected 1 broker command, got %d", len(broker.queuedCommands))
		}
	})

	t.Run("empty GPU list", func(t *testing.T) {
		mockClient := &mlnodeclient.MockClient{
			GPUDevices: []mlnodeclient.GPUDevice{},
		}

		configMgr := &mockConfigManager{
			nodes: []apiconfig.InferenceNodeConfig{
				{Id: "node1", Host: "localhost", PoCPort: 8080, PoCSegment: "/api"},
			},
		}

		broker := &mockBroker{}
		factory := &mockClientFactory{client: mockClient}

		manager := NewMLNodeBackgroundManager(
			configMgr,
			nil,
			broker,
			factory,
			30*time.Minute,
		)

		ctx := context.Background()
		manager.checkAndUpdateGPUs(ctx)

		// Should not call broker for empty GPU list
		if len(broker.queuedCommands) != 0 {
			t.Errorf("expected 0 broker commands, got %d", len(broker.queuedCommands))
		}
	})
}
