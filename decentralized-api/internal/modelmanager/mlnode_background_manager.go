package modelmanager

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

// NodesConfigManagerInterface defines the minimal interface needed from ConfigManager
type NodesConfigManagerInterface interface {
	GetNodes() []apiconfig.InferenceNodeConfig
	GetCurrentNodeVersion() string
	SetNodes(nodes []apiconfig.InferenceNodeConfig) error
}

// PhaseTrackerInterface defines the minimal interface needed from PhaseTracker
type PhaseTrackerInterface interface {
	GetCurrentEpochState() *chainphase.EpochState
}

// BrokerInterface defines minimal interface for broker operations
type BrokerInterface interface {
	QueueMessage(command broker.Command) error
}

// MLNodeBackgroundManager handles background operations for MLNodes:
// - Model pre-downloading for upcoming epochs
// - GPU hardware detection and updates
type MLNodeBackgroundManager struct {
	configManager       NodesConfigManagerInterface
	phaseTracker        PhaseTrackerInterface
	broker              BrokerInterface
	mlNodeClientFactory mlnodeclient.ClientFactory
	checkInterval       time.Duration
}

// NewMLNodeBackgroundManager creates a new MLNode background manager
func NewMLNodeBackgroundManager(
	configManager NodesConfigManagerInterface,
	phaseTracker PhaseTrackerInterface,
	broker BrokerInterface,
	clientFactory mlnodeclient.ClientFactory,
	checkInterval time.Duration,
) *MLNodeBackgroundManager {
	return &MLNodeBackgroundManager{
		configManager:       configManager,
		phaseTracker:        phaseTracker,
		broker:              broker,
		mlNodeClientFactory: clientFactory,
		checkInterval:       checkInterval,
	}
}

// Start begins the periodic background tasks loop
func (m *MLNodeBackgroundManager) Start(ctx context.Context) {
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()

	logging.Info("MLNodeBackgroundManager started", types.System, "check_interval", m.checkInterval)

	for {
		select {
		case <-ticker.C:
			m.checkAndDownloadModels(ctx)
			m.checkAndUpdateGPUs(ctx)
		case <-ctx.Done():
			logging.Info("MLNodeBackgroundManager stopped", types.System)
			return
		}
	}
}

// checkAndDownloadModels performs the periodic check and triggers downloads if needed
func (m *MLNodeBackgroundManager) checkAndDownloadModels(ctx context.Context) {
	epochState := m.phaseTracker.GetCurrentEpochState()
	if !m.isInDownloadWindow(epochState) {
		return
	}

	logging.Info("Starting model pre-download check",
		types.System,
		"block", epochState.CurrentBlock.Height,
		"phase", epochState.CurrentPhase)

	nodes := m.configManager.GetNodes()
	for _, node := range nodes {
		m.checkNodeModels(node)
	}
}

// isInDownloadWindow checks if we're in a safe window to download models
func (m *MLNodeBackgroundManager) isInDownloadWindow(epochState *chainphase.EpochState) bool {
	if epochState.IsNilOrNotSynced() {
		return false
	}

	if epochState.CurrentPhase != types.InferencePhase {
		return false
	}

	currentBlock := epochState.CurrentBlock.Height
	setNewValidators := epochState.LatestEpoch.SetNewValidators()
	inferenceValidationCutoff := epochState.LatestEpoch.InferenceValidationCutoff()

	// Window: [SetNewValidators + 30, InferenceValidationCutoff - 200]
	windowStart := setNewValidators + 30
	windowEnd := inferenceValidationCutoff - 200

	if currentBlock < windowStart || currentBlock > windowEnd {
		return false
	}

	return true
}

// checkNodeModels checks and downloads models for a specific node
func (m *MLNodeBackgroundManager) checkNodeModels(node apiconfig.InferenceNodeConfig) {
	version := m.configManager.GetCurrentNodeVersion()
	pocUrl := getPoCUrlWithVersion(node, version)
	inferenceUrl := getInferenceUrlWithVersion(node, version)
	client := m.mlNodeClientFactory.CreateClient(pocUrl, inferenceUrl, node.AuthToken)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	endpointAvailable := true

	for modelId := range node.Models {
		model := mlnodeclient.Model{
			HfRepo:   modelId,
			HfCommit: nil, // nil = latest
		}

		statusResp, err := client.CheckModelStatus(ctx, model)
		if err != nil {
			var apiNotImplemented *mlnodeclient.ErrAPINotImplemented
			if errors.As(err, &apiNotImplemented) {
				if endpointAvailable {
					logging.Info("Model pre-download endpoint not available",
						types.System,
						"node_id", node.Id)
					endpointAvailable = false
				}
				break
			}

			logging.Warn("Failed to check model status",
				types.System,
				"node_id", node.Id,
				"model", modelId,
				"error", err.Error())
			continue
		}

		switch statusResp.Status {
		case mlnodeclient.ModelStatusNotFound, mlnodeclient.ModelStatusPartial:
			logging.Info("Pre-downloading model",
				types.System,
				"model", modelId,
				"node_id", node.Id)

			_, err := client.DownloadModel(ctx, model)
			if err != nil {
				logging.Warn("Failed to start model download",
					types.System,
					"node_id", node.Id,
					"model", modelId,
					"error", err.Error())
			}

		case mlnodeclient.ModelStatusDownloading:
			logging.Debug("Model already downloading",
				types.System,
				"model", modelId,
				"node_id", node.Id)

		case mlnodeclient.ModelStatusDownloaded:
			logging.Debug("Model already downloaded",
				types.System,
				"model", modelId,
				"node_id", node.Id)
		}
	}
}

func getPoCUrlWithVersion(node apiconfig.InferenceNodeConfig, version string) string {
	if version == "" {
		return getPoCUrl(node)
	}
	return getPoCUrlVersioned(node, version)
}

func getInferenceUrlWithVersion(node apiconfig.InferenceNodeConfig, version string) string {
	if version == "" {
		return getInferenceUrl(node)
	}
	return getInferenceUrlVersioned(node, version)
}

func getPoCUrl(node apiconfig.InferenceNodeConfig) string {
	if node.BaseURL != "" {
		return formatBaseURL(node.BaseURL, node.PoCSegment)
	}
	return formatURL(node.Host, node.PoCPort, node.PoCSegment)
}

func getPoCUrlVersioned(node apiconfig.InferenceNodeConfig, version string) string {
	if node.BaseURL != "" {
		return formatBaseURLWithVersion(node.BaseURL, version, node.PoCSegment)
	}
	return formatURLWithVersion(node.Host, node.PoCPort, version, node.PoCSegment)
}

func getInferenceUrl(node apiconfig.InferenceNodeConfig) string {
	if node.BaseURL != "" {
		return formatBaseURL(node.BaseURL, node.InferenceSegment)
	}
	return formatURL(node.Host, node.InferencePort, node.InferenceSegment)
}

func getInferenceUrlVersioned(node apiconfig.InferenceNodeConfig, version string) string {
	if node.BaseURL != "" {
		return formatBaseURLWithVersion(node.BaseURL, version, node.InferenceSegment)
	}
	return formatURLWithVersion(node.Host, node.InferencePort, version, node.InferenceSegment)
}

func formatURL(host string, port int, segment string) string {
	return fmt.Sprintf("http://%s:%d%s", host, port, segment)
}

func formatURLWithVersion(host string, port int, version string, segment string) string {
	return fmt.Sprintf("http://%s:%d/%s%s", host, port, version, segment)
}

func formatBaseURL(baseURL string, segment string) string {
	// seg := segment
	// if seg == "" {
	// 	seg = "/"
	// }
	base := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf("%s%s", base, segment)
}

func formatBaseURLWithVersion(baseURL string, version string, segment string) string {
	// seg := segment
	// if seg == "" {
	// 	seg = "/"
	// }
	base := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf("%s/%s%s", base, version, segment)
}

// checkAndUpdateGPUs fetches GPU info from all nodes and updates hardware
func (m *MLNodeBackgroundManager) checkAndUpdateGPUs(ctx context.Context) {
	nodes := m.configManager.GetNodes()
	updatedNodes := make([]apiconfig.InferenceNodeConfig, 0, len(nodes))

	for _, node := range nodes {
		updatedNodes = append(updatedNodes, node.DeepCopy())
	}

	for i := range updatedNodes {
		node := &updatedNodes[i]

		hardware, err := m.fetchNodeGPUHardware(ctx, node)
		if err != nil {
			var apiNotImplemented *mlnodeclient.ErrAPINotImplemented
			if errors.As(err, &apiNotImplemented) {
				logging.Info("GPU endpoint not available for node", types.Nodes, "node_id", node.Id)
			} else {
				logging.Warn("Failed to fetch GPU info for node", types.Nodes, "node_id", node.Id, "error", err.Error())
			}
			continue
		}

		if len(hardware) == 0 {
			continue
		}

		// Update config
		node.Hardware = hardware

		// Update broker (for immediate chain sync)
		responseChan := make(chan error, 1)
		cmd := broker.UpdateNodeHardwareCommand{
			NodeId:   node.Id,
			Hardware: hardware,
			Response: responseChan,
		}

		if err := m.broker.QueueMessage(cmd); err != nil {
			logging.Warn("Failed to queue hardware update", types.Nodes, "node_id", node.Id, "error", err.Error())
			continue
		}

		if err := <-responseChan; err != nil {
			logging.Warn("Failed to update broker hardware", types.Nodes, "node_id", node.Id, "error", err.Error())
		} else {
			logging.Info("Updated GPU hardware", types.Nodes, "node_id", node.Id, "hardware_count", len(hardware))
		}
	}

	// Persist all changes to config
	if err := m.configManager.SetNodes(updatedNodes); err != nil {
		logging.Error("Failed to persist GPU hardware to config", types.Nodes, "error", err.Error())
	}
}

// fetchNodeGPUHardware fetches GPU devices and transforms to Hardware entries
func (m *MLNodeBackgroundManager) fetchNodeGPUHardware(ctx context.Context, node *apiconfig.InferenceNodeConfig) ([]apiconfig.Hardware, error) {
	version := m.configManager.GetCurrentNodeVersion()
	pocUrl := getPoCUrlWithVersion(*node, version)
	inferenceUrl := getInferenceUrlWithVersion(*node, version)
	client := m.mlNodeClientFactory.CreateClient(pocUrl, inferenceUrl, node.AuthToken)

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := client.GetGPUDevices(timeoutCtx)
	if err != nil {
		return nil, err
	}

	return transformGPUDevicesToHardware(resp.Devices), nil
}

// transformGPUDevicesToHardware groups GPUs by type and memory, returns Hardware list
func transformGPUDevicesToHardware(devices []mlnodeclient.GPUDevice) []apiconfig.Hardware {
	groupCounts := make(map[string]uint32)

	for _, device := range devices {
		// Skip unavailable, errored, or GPUs without memory info
		if !device.IsAvailable || device.ErrorMessage != nil || device.TotalMemoryMB == nil {
			continue
		}

		memoryGB := *device.TotalMemoryMB / 1024
		key := fmt.Sprintf("%s | %dGB", device.Name, int(memoryGB))
		groupCounts[key]++
	}

	hardware := make([]apiconfig.Hardware, 0, len(groupCounts))
	for gpuType, count := range groupCounts {
		hardware = append(hardware, apiconfig.Hardware{
			Type:  gpuType,
			Count: count,
		})
	}

	// Sort for consistent ordering
	sort.Slice(hardware, func(i, j int) bool {
		return hardware[i].Type < hardware[j].Type
	})

	return hardware
}
