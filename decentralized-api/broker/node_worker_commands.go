package broker

import (
	"common/logging"
	"context"
	"decentralized-api/mlnodeclient"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

func encodeCallbackModelID(modelID string) string {
	return url.PathEscape(url.PathEscape(modelID))
}

// NodeWorkerCommand defines the interface for commands executed by NodeWorker
type NodeWorkerCommand interface {
	Execute(ctx context.Context, worker *NodeWorker) NodeResult
}

// StopNodeCommand stops the ML node
type StopNodeCommand struct{}

func (c StopNodeCommand) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
	result := NodeResult{
		OriginalTarget: types.HardwareNodeStatus_STOPPED,
	}

	if ctx.Err() != nil {
		result.Succeeded = false
		result.Error = ctx.Err().Error()
		result.FinalStatus = worker.node.State.CurrentStatus // Status is unchanged
		result.FinalPocStatus = worker.node.State.PocCurrentStatus
		return result
	}

	err := worker.GetClient().Stop(ctx)
	if err != nil {
		logging.Error("Failed to stop node", types.Nodes, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
	} else {
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_STOPPED
		result.FinalPocStatus = PocStatusIdle
	}
	return result
}

// InferenceUpNodeCommand brings up inference on a single node
type InferenceUpNodeCommand struct{}

func (c InferenceUpNodeCommand) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
	result := NodeResult{
		OriginalTarget:    types.HardwareNodeStatus_INFERENCE,
		OriginalPocTarget: PocStatusIdle,
	}
	if ctx.Err() != nil {
		result.Succeeded = false
		result.Error = ctx.Err().Error()
		result.FinalStatus = worker.node.State.CurrentStatus
		result.FinalPocStatus = worker.node.State.PocCurrentStatus
		return result
	}

	client := worker.GetClient()
	state, stateErr := client.NodeState(ctx)
	healthyServing := false
	if stateErr == nil && state.State == mlnodeclient.MlNodeState_INFERENCE {
		healthyServing, _ = client.InferenceHealth(ctx)
	}

	selectedModel, err := selectInferenceModel(worker)
	if err != nil {
		if healthyServing && !worker.node.State.DeploymentUpdatePending {
			logging.Warn("Could not resolve model for healthy node; keeping existing inference deployment", types.Nodes,
				"node_id", worker.nodeId, "error", err)
			return keepHealthyInference(ctx, client, result, worker.nodeId)
		}
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
		logging.Error(result.Error, types.Nodes, "node_id", worker.nodeId)
		return result
	}
	localConfig := worker.node.Node.Models[selectedModel.Id]
	deployment := worker.broker.ResolveModelDeployment(*selectedModel, localConfig)
	if healthyServing {
		modelMatches := true
		if state.LoadedModel != "" && state.LoadedModel != deployment.LoadModel {
			logging.Info("Loaded model source mismatch detected, will redeploy", types.Nodes,
				"node_id", worker.nodeId, "loaded", state.LoadedModel, "expected", deployment.LoadModel)
			modelMatches = false
		}
		if loadedModels, err := client.GetLoadedModels(ctx); err != nil {
			logging.Debug("GetLoadedModels failed, assuming served model match", types.Nodes, "node_id", worker.nodeId, "error", err)
		} else if len(loadedModels) > 0 && !loadedModelsContain(loadedModels, deployment.GovernanceID) {
			logging.Info("Served model mismatch detected, will redeploy", types.Nodes,
				"node_id", worker.nodeId, "loaded", loadedModels, "expected", deployment.GovernanceID)
			modelMatches = false
		}

		if modelMatches && !worker.node.State.DeploymentUpdatePending {
			return keepHealthyInference(ctx, client, result, worker.nodeId)
		}
	}

	if healthyServing && localConfig.ModelOverride != nil {
		if deferred := ensureOverrideReady(ctx, client, deployment, result, worker.nodeId); deferred != nil {
			return *deferred
		}
	}

	if err := client.Stop(ctx); err != nil {
		logging.Error("Failed to stop node for inference up", types.Nodes, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
		return result
	}

	logging.Info("Selected model deployment for inference", types.Nodes,
		"node_id", worker.nodeId, "governance_model", deployment.GovernanceID, "load_model", deployment.LoadModel)
	if err := client.InferenceUp(ctx, deployment.LoadModel, deployment.Args); err != nil {
		logging.Error("Failed to bring up inference", types.Nodes, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
	} else {
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_INFERENCE
		result.FinalPocStatus = PocStatusIdle
		result.DeploymentApplied = true
		result.DeploymentModelID = deployment.GovernanceID
		result.DeploymentUsesOverride = localConfig.ModelOverride != nil
		if result.DeploymentUsesOverride {
			result.DeploymentFingerprint = deployment.Fingerprint()
		}
		logging.Info("Successfully brought up inference on node", types.Nodes, "node_id", worker.nodeId)
	}
	return result
}

func keepHealthyInference(
	ctx context.Context,
	client mlnodeclient.MLNodeClient,
	result NodeResult,
	nodeID string,
) NodeResult {
	if pocStatus, err := client.GetPowStatusV2(ctx); err != nil {
		logging.Debug("GetPowStatusV2 failed during inference transition", types.Nodes, "node_id", nodeID, "error", err)
	} else if pocStatus != nil {
		logging.Debug("GetPowStatusV2 status during inference transition", types.Nodes, "node_id", nodeID, "status", pocStatus.Status)
		if pocStatus.Status == "GENERATING" || pocStatus.Status == "VALIDATING" {
			if _, err := client.StopPowV2(ctx); err != nil {
				logging.Debug("StopPowV2 during inference transition failed", types.Nodes, "node_id", nodeID, "error", err)
			}
		}
	}
	logging.Info("Node already in healthy inference state", types.Nodes, "node_id", nodeID)
	result.Succeeded = true
	result.FinalStatus = types.HardwareNodeStatus_INFERENCE
	result.FinalPocStatus = PocStatusIdle
	return result
}

func selectInferenceModel(worker *NodeWorker) (*types.Model, error) {
	expectedModelID, ok := worker.broker.resolveSupportedNodeModelID(worker.node.State.EpochMLNodes, worker.node.Node.Models)
	if !ok || expectedModelID == "" {
		return nil, errors.New("no epoch models available for this node")
	}
	if model, exists := worker.node.State.EpochModels[expectedModelID]; exists {
		return &model, nil
	}

	govModels, err := worker.broker.chainBridge.GetGovernanceModels()
	if err != nil {
		return nil, fmt.Errorf("failed to get governance models: %w", err)
	}
	for i := range govModels.Model {
		if govModels.Model[i].Id == expectedModelID {
			return &govModels.Model[i], nil
		}
	}
	return nil, errors.New("no epoch models available for this node")
}

func ensureOverrideReady(
	ctx context.Context,
	client mlnodeclient.MLNodeClient,
	deployment ModelDeployment,
	result NodeResult,
	nodeID string,
) *NodeResult {
	var commit *string
	if deployment.LoadCommit != "" {
		commit = &deployment.LoadCommit
	}
	model := mlnodeclient.Model{HfRepo: deployment.LoadModel, HfCommit: commit}
	status, err := client.CheckModelStatus(ctx, model)
	if err != nil {
		var notImplemented *mlnodeclient.ErrAPINotImplemented
		if errors.As(err, &notImplemented) {
			return nil
		}
		logging.Warn("Failed to check override cache readiness; deferring redeploy", types.Nodes,
			"node_id", nodeID, "model", deployment.LoadModel, "error", err)
		return deferredDeploymentResult(result)
	}
	switch status.Status {
	case mlnodeclient.ModelStatusDownloaded:
		return nil
	case mlnodeclient.ModelStatusNotFound, mlnodeclient.ModelStatusPartial:
		if _, err := client.DownloadModel(ctx, model); err != nil {
			logging.Warn("Failed to start override download", types.Nodes,
				"node_id", nodeID, "model", deployment.LoadModel, "error", err)
		}
		return deferredDeploymentResult(result)
	case mlnodeclient.ModelStatusDownloading:
		return deferredDeploymentResult(result)
	default:
		return deferredDeploymentResult(result)
	}
}

func deferredDeploymentResult(result NodeResult) *NodeResult {
	result.Succeeded = true
	result.FinalStatus = types.HardwareNodeStatus_INFERENCE
	result.FinalPocStatus = PocStatusIdle
	result.DeploymentDeferred = true
	result.DeploymentRetryAfter = time.Now().Add(time.Minute)
	return &result
}

// NoOpNodeCommand is a command that does nothing (used as placeholder)
type NoOpNodeCommand struct {
	Message string
}

func (c *NoOpNodeCommand) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
	if c.Message != "" {
		logging.Debug(c.Message, types.Nodes, "node_id", worker.nodeId)
	}
	return NodeResult{
		Succeeded:      true,
		FinalStatus:    worker.node.State.CurrentStatus,
		OriginalTarget: worker.node.State.CurrentStatus,
	}
}

type StartPoCNodeCommandV2 struct {
	BlockHeight    int64
	BlockHash      string
	PubKey         string
	CallbackUrl    string
	TotalNodes     int
	Model          string
	SeqLen         int64
	PocStrongerRng bool
}

func (c StartPoCNodeCommandV2) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
	result := NodeResult{
		OriginalTarget:    types.HardwareNodeStatus_POC,
		OriginalPocTarget: PocStatusGenerating,
	}

	if ctx.Err() != nil {
		result.Succeeded = false
		result.Error = ctx.Err().Error()
		result.FinalStatus = worker.node.State.CurrentStatus
		result.FinalPocStatus = worker.node.State.PocCurrentStatus
		return result
	}

	// Idempotency check - if already generating, skip restart
	// This is safe: any old-epoch generation was stopped during inference transition
	status, err := worker.GetClient().GetPowStatusV2(ctx)
	if err != nil {
		logging.Debug("[StartPoCNodeCommandV2] GetPowStatusV2 failed, proceeding with init", types.PoC, "node_id", worker.nodeId, "error", err)
	} else if status != nil {
		logging.Debug("[StartPoCNodeCommandV2] GetPowStatusV2 status", types.PoC, "node_id", worker.nodeId, "status", status.Status)
		if status.Status == "GENERATING" {
			logging.Info("[StartPoCNodeCommandV2] Already generating, skipping restart", types.PoC, "node_id", worker.nodeId)
			result.Succeeded = true
			result.FinalStatus = types.HardwareNodeStatus_POC
			result.FinalPocStatus = PocStatusGenerating
			return result
		}
	}

	req := mlnodeclient.PoCInitGenerateRequestV2{
		BlockHash:   c.BlockHash,
		BlockHeight: c.BlockHeight,
		PublicKey:   c.PubKey,
		NodeId:      int(worker.node.Node.NodeNum),
		NodeCount:   c.TotalNodes,
		Params: mlnodeclient.PoCParamsV2{
			Model:  c.Model,
			SeqLen: c.SeqLen,
		},
		URL:            c.CallbackUrl + "/" + encodeCallbackModelID(c.Model),
		PocStrongerRng: c.PocStrongerRng,
	}

	if _, err := worker.GetClient().InitGenerateV2(ctx, req); err != nil {
		logging.Error("[StartPoCNodeCommandV2] Failed to start PoC v2", types.PoC, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
	} else {
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_POC
		result.FinalPocStatus = PocStatusGenerating
		logging.Info("[StartPoCNodeCommandV2] Successfully started PoC v2 on node", types.PoC, "node_id", worker.nodeId)
	}
	return result
}

// TransitionPoCToValidatingCommandV2 is a no-network command that transitions the broker's
// internal node state to POC/Validating when PoC v2 is enabled.
// Actual v2 validation is handled by the v2 orchestrator (not the broker), which calls
// StopPowV2 once and then sends GenerateV2 validation requests with artifacts.
// This command ensures broker state consistency without making any v1 PoW API calls.
type TransitionPoCToValidatingCommandV2 struct{}

func (c TransitionPoCToValidatingCommandV2) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
	result := NodeResult{
		OriginalTarget:    types.HardwareNodeStatus_POC,
		OriginalPocTarget: PocStatusValidating,
	}

	if ctx.Err() != nil {
		result.Succeeded = false
		result.Error = ctx.Err().Error()
		result.FinalStatus = worker.node.State.CurrentStatus
		result.FinalPocStatus = worker.node.State.PocCurrentStatus
		return result
	}

	// Validate node is in a state that can transition to POC/Validating.
	// Accept only POC or INFERENCE (matching filterNodesForValidation criteria).
	currentStatus := worker.node.State.CurrentStatus
	if currentStatus != types.HardwareNodeStatus_POC && currentStatus != types.HardwareNodeStatus_INFERENCE {
		result.Succeeded = false
		result.Error = "cannot transition to POC/Validating: node is " + currentStatus.String()
		result.FinalStatus = currentStatus
		result.FinalPocStatus = worker.node.State.PocCurrentStatus
		logging.Warn("[TransitionPoCToValidatingCommandV2] Rejecting transition due to invalid state", types.PoC,
			"node_id", worker.nodeId, "current_status", currentStatus.String())
		return result
	}

	// No network call - just transition broker state.
	// The v2 orchestrator handles StopPowV2 and GenerateV2 validation requests.
	result.Succeeded = true
	result.FinalStatus = types.HardwareNodeStatus_POC
	result.FinalPocStatus = PocStatusValidating
	logging.Info("[TransitionPoCToValidatingCommandV2] Transitioned broker state to POC/Validating (no network call)", types.PoC,
		"node_id", worker.nodeId)
	return result
}
