package broker

import (
	"context"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"errors"

	"github.com/productscience/inference/x/inference/types"
)

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

	// Idempotency check - skip redeploy if already running correct model
	if state, err := worker.GetClient().NodeState(ctx); err == nil && state.State == mlnodeclient.MlNodeState_INFERENCE {
		if healthy, _ := worker.GetClient().InferenceHealth(ctx); healthy {
			// Check if loaded model matches expected
			modelMatches := true
			var expectedModel string
			for modelId := range worker.node.State.EpochModels {
				expectedModel = modelId
				break
			}
			if expectedModel != "" {
				if loadedModels, err := worker.GetClient().GetLoadedModels(ctx); err != nil {
					logging.Debug("GetLoadedModels failed, assuming model match", types.Nodes, "node_id", worker.nodeId, "error", err)
				} else if len(loadedModels) > 0 && loadedModels[0] != expectedModel {
					logging.Info("Model mismatch detected, will redeploy", types.Nodes,
						"node_id", worker.nodeId, "loaded", loadedModels[0], "expected", expectedModel)
					modelMatches = false
				}
			}

			if modelMatches {
				// Stop any running PoC V2 (runs inside inference/vLLM)
				if pocStatus, err := worker.GetClient().GetPowStatusV2(ctx); err != nil {
					logging.Debug("GetPowStatusV2 failed during inference transition", types.Nodes, "node_id", worker.nodeId, "error", err)
				} else if pocStatus != nil {
					logging.Debug("GetPowStatusV2 status during inference transition", types.Nodes, "node_id", worker.nodeId, "status", pocStatus.Status)
					if pocStatus.Status == "GENERATING" || pocStatus.Status == "VALIDATING" {
						if _, err := worker.GetClient().StopPowV2(ctx); err != nil {
							logging.Debug("StopPowV2 during inference transition failed", types.Nodes, "node_id", worker.nodeId, "error", err)
						}
					}
				}

				logging.Info("Node already in healthy inference state", types.Nodes, "node_id", worker.nodeId)
				result.Succeeded = true
				result.FinalStatus = types.HardwareNodeStatus_INFERENCE
				result.FinalPocStatus = PocStatusIdle
				return result
			}
		}
	}

	// Redeploy: stop and start with correct model

	// Stop node first
	if err := worker.GetClient().Stop(ctx); err != nil {
		logging.Error("Failed to stop node for inference up", types.Nodes, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
		return result
	}

	var selectedModel *types.Model
	if len(worker.node.State.EpochModels) == 0 {
		govModels, err := worker.broker.chainBridge.GetGovernanceModels()
		if err != nil {
			result.Succeeded = false
			result.Error = "Failed to get governance models: " + err.Error()
			result.FinalStatus = types.HardwareNodeStatus_FAILED
			logging.Error(result.Error, types.Nodes, "node_id", worker.nodeId)
			return result
		}

		hasIntersection := false
		for _, govModel := range govModels.Model {
			if _, ok := worker.node.Node.Models[govModel.Id]; ok {
				hasIntersection = true
				selectedModel = &govModel
				break
			}
		}

		if !hasIntersection {
			result.Succeeded = false
			result.Error = "No epoch models available for this node"
			result.FinalStatus = types.HardwareNodeStatus_FAILED
			logging.Error(result.Error, types.Nodes, "node_id", worker.nodeId)
			return result
		}

		logging.Info("No epoch models configured for this node, using a governance model from one the supported by the node", types.Nodes, "node_id", worker.nodeId, "selectedModel", selectedModel)
	} else {
		for _, m := range worker.node.State.EpochModels {
			selectedModel = &m
			break
		}
	}

	if selectedModel == nil || selectedModel.Id == "" {
		result.Succeeded = false
		result.Error = "Could not select a model from epoch models"
		result.FinalStatus = types.HardwareNodeStatus_FAILED
		logging.Error(result.Error, types.Nodes, "node_id", worker.nodeId)
		return result
	}

	logging.Info("Selected model for inference", types.Nodes, "node_id", worker.nodeId, "selectedModel", selectedModel)

	// Merge epoch model args with local ones
	var localArgs []string
	if localModelConfig, ok := worker.node.Node.Models[selectedModel.Id]; ok {
		localArgs = localModelConfig.Args
	}
	mergedArgs := worker.broker.MergeModelArgs(selectedModel.ModelArgs, localArgs)

	if err := worker.GetClient().InferenceUp(ctx, selectedModel.Id, mergedArgs); err != nil {
		logging.Error("Failed to bring up inference", types.Nodes, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
	} else {
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_INFERENCE
		result.FinalPocStatus = PocStatusIdle
		logging.Info("Successfully brought up inference on node", types.Nodes, "node_id", worker.nodeId)
	}
	return result
}

// StartTrainingNodeCommand starts training on a single node
type StartTrainingNodeCommand struct {
	TaskId         uint64
	Participant    string
	MasterNodeAddr string
	NodeRanks      map[string]int
	WorldSize      int
}

func (c StartTrainingNodeCommand) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
	result := NodeResult{
		OriginalTarget: types.HardwareNodeStatus_TRAINING,
	}

	if ctx.Err() != nil {
		result.Succeeded = false
		result.Error = ctx.Err().Error()
		result.FinalStatus = worker.node.State.CurrentStatus
		result.FinalPocStatus = worker.node.State.PocCurrentStatus
		return result
	}

	rank, ok := c.NodeRanks[worker.nodeId]
	if !ok {
		err := errors.New("rank not found for node")
		logging.Error(err.Error(), types.Training, "node_id", worker.nodeId)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
		return result
	}

	// Stop node first
	if err := worker.GetClient().Stop(ctx); err != nil {
		logging.Error("Failed to stop node for training", types.Training, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
		return result
	}

	// Start training
	trainingErr := worker.GetClient().StartTraining(
		ctx, c.TaskId, c.Participant, worker.nodeId,
		c.MasterNodeAddr, rank, c.WorldSize,
	)
	if trainingErr != nil {
		logging.Error("Failed to start training", types.Training, "node_id", worker.nodeId, "error", trainingErr)
		result.Succeeded = false
		result.Error = trainingErr.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
	} else {
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_TRAINING
		result.FinalPocStatus = PocStatusIdle
		logging.Info("Successfully started training on node", types.Training, "node_id", worker.nodeId, "rank", rank, "task_id", c.TaskId)
	}
	return result
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
	BlockHeight int64
	BlockHash   string
	PubKey      string
	CallbackUrl string
	TotalNodes  int
	Model       string
	SeqLen      int64
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
		URL: c.CallbackUrl,
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
