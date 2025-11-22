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

// StartPoCNodeCommand starts PoC on a single node
type StartPoCNodeCommand struct {
	BlockHeight int64
	BlockHash   string
	PubKey      string
	CallbackUrl string
	TotalNodes  int
}

func (c StartPoCNodeCommand) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
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

	// Idempotency check
	state, err := worker.GetClient().NodeState(ctx)
	if err == nil && state.State == mlnodeclient.MlNodeState_POW {
		powStatus, powErr := worker.GetClient().GetPowStatus(ctx)
		if powErr == nil && powStatus.Status == mlnodeclient.POW_GENERATING {
			logging.Info("[StartPoCNodeCommand] Node already in PoC generating state", types.PoC, "node_id", worker.nodeId)
			result.Succeeded = true
			result.FinalStatus = types.HardwareNodeStatus_POC
			result.FinalPocStatus = PocStatusGenerating
			return result
		}
	}

	// Stop node if needed
	if state != nil && state.State != mlnodeclient.MlNodeState_STOPPED {
		if err := worker.GetClient().Stop(ctx); err != nil {
			logging.Error("[StartPoCNodeCommand] Failed to stop node for PoC", types.PoC, "node_id", worker.nodeId, "error", err)
			result.Succeeded = false
			result.Error = err.Error()
			result.FinalStatus = types.HardwareNodeStatus_FAILED
			return result
		}
	}

	// Start PoC
	dto := mlnodeclient.BuildInitDto(
		c.BlockHeight, c.PubKey, int64(c.TotalNodes),
		worker.node.Node.NodeNum, c.BlockHash, c.CallbackUrl,
	)
	if err := worker.GetClient().InitGenerate(ctx, dto); err != nil {
		logging.Error("[StartPoCNodeCommand] Failed to start PoC", types.PoC, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
	} else {
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_POC
		result.FinalPocStatus = PocStatusGenerating
		logging.Info("[StartPoCNodeCommand] Successfully started PoC on node", types.PoC, "node_id", worker.nodeId)
	}
	return result
}

type InitValidateNodeCommand struct {
	BlockHeight int64
	BlockHash   string
	PubKey      string
	CallbackUrl string
	TotalNodes  int
}

func (c InitValidateNodeCommand) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
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

	// Idempotency check
	state, err := worker.GetClient().NodeState(ctx)
	if err == nil && state.State == mlnodeclient.MlNodeState_POW {
		powStatus, powErr := worker.GetClient().GetPowStatus(ctx)
		if powErr == nil && powStatus.Status == mlnodeclient.POW_VALIDATING {
			logging.Info("Node already in PoC validating state", types.PoC, "node_id", worker.nodeId)
			result.Succeeded = true
			result.FinalStatus = types.HardwareNodeStatus_POC
			result.FinalPocStatus = PocStatusValidating
			return result
		}
	}

	// Stop node if needed
	if state != nil && state.State != mlnodeclient.MlNodeState_STOPPED && state.State != mlnodeclient.MlNodeState_POW {
		if err := worker.GetClient().Stop(ctx); err != nil {
			logging.Error("Failed to stop node for PoC validation", types.PoC, "node_id", worker.nodeId, "error", err)
			result.Succeeded = false
			result.Error = err.Error()
			result.FinalStatus = types.HardwareNodeStatus_FAILED
			return result
		}
	}

	dto := mlnodeclient.BuildInitDto(
		c.BlockHeight, c.PubKey, int64(c.TotalNodes),
		worker.node.Node.NodeNum, c.BlockHash, c.CallbackUrl,
	)

	if err := worker.GetClient().InitValidate(ctx, dto); err != nil {
		logging.Error("Failed to transition to PoC validate", types.PoC, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
	} else {
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_POC
		result.FinalPocStatus = PocStatusValidating
		logging.Info("Successfully transitioned node to PoC init validate stage", types.PoC, "node_id", worker.nodeId)
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

	// Idempotency check
	state, err := worker.GetClient().NodeState(ctx)
	if err == nil && state.State == mlnodeclient.MlNodeState_INFERENCE {
		if healthy, _ := worker.GetClient().InferenceHealth(ctx); healthy {
			logging.Info("Node already in healthy inference state", types.Nodes, "node_id", worker.nodeId)
			result.Succeeded = true
			result.FinalStatus = types.HardwareNodeStatus_INFERENCE
			result.FinalPocStatus = PocStatusIdle
			return result
		}
	}

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
