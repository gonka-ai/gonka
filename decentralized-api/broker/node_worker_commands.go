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
		return result
	}

	err := worker.mlClient.Stop()
	if err != nil {
		logging.Error("Failed to stop node", types.Nodes, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
	} else {
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_STOPPED
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
		OriginalTarget: types.HardwareNodeStatus_POC,
	}

	if ctx.Err() != nil {
		result.Succeeded = false
		result.Error = ctx.Err().Error()
		result.FinalStatus = worker.node.State.CurrentStatus
		return result
	}

	// Idempotency check
	status, err := worker.mlClient.GetPowStatus()
	if err == nil && status.Status == mlnodeclient.POW_GENERATING {
		logging.Info("Node already in PoC state", types.PoC, "node_id", worker.nodeId)
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_POC
		return result
	}

	// Stop node if needed
	nodeState, _ := worker.mlClient.NodeState()
	if nodeState != nil && nodeState.State != mlnodeclient.MlNodeState_STOPPED {
		if err := worker.mlClient.Stop(); err != nil {
			logging.Error("Failed to stop node for PoC", types.PoC, "node_id", worker.nodeId, "error", err)
			result.Succeeded = false
			result.Error = err.Error()
			result.FinalStatus = types.HardwareNodeStatus_FAILED
			return result
		}
	}

	// Start PoC
	dto := mlnodeclient.BuildInitDto(
		c.BlockHeight, c.PubKey, int64(c.TotalNodes),
		int64(worker.node.Node.NodeNum), c.BlockHash, c.CallbackUrl,
	)
	if err := worker.mlClient.InitGenerate(dto); err != nil {
		logging.Error("Failed to start PoC", types.PoC, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
	} else {
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_POC
		logging.Info("Successfully started PoC on node", types.PoC, "node_id", worker.nodeId)
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
		OriginalTarget: types.HardwareNodeStatus_POC,
	}

	if ctx.Err() != nil {
		result.Succeeded = false
		result.Error = ctx.Err().Error()
		result.FinalStatus = worker.node.State.CurrentStatus
		return result
	}

	// Idempotency check
	status, err := worker.mlClient.GetPowStatus()
	if err == nil && status.Status == mlnodeclient.POW_VALIDATING {
		logging.Info("Node already in POW_VALIDATING state", types.PoC, "node_id", worker.nodeId)
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_POC
		return result
	}

	dto := mlnodeclient.BuildInitDto(
		c.BlockHeight, c.PubKey, int64(c.TotalNodes),
		int64(worker.node.Node.NodeNum), c.BlockHash, c.CallbackUrl,
	)

	if err := worker.mlClient.InitValidate(dto); err != nil {
		logging.Error("Failed to transition to PoC validate", types.PoC, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
	} else {
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_POC
		logging.Info("Successfully transitioned node to PoC init validate stage", types.PoC, "node_id", worker.nodeId)
	}
	return result
}

// InferenceUpNodeCommand brings up inference on a single node
type InferenceUpNodeCommand struct{}

func (c InferenceUpNodeCommand) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
	result := NodeResult{
		OriginalTarget: types.HardwareNodeStatus_INFERENCE,
	}
	if ctx.Err() != nil {
		result.Succeeded = false
		result.Error = ctx.Err().Error()
		result.FinalStatus = worker.node.State.CurrentStatus
		return result
	}

	// Idempotency check
	state, err := worker.mlClient.NodeState()
	if err == nil && state.State == mlnodeclient.MlNodeState_INFERENCE {
		if healthy, _ := worker.mlClient.InferenceHealth(); healthy {
			logging.Info("Node already in healthy inference state", types.Nodes, "node_id", worker.nodeId)
			result.Succeeded = true
			result.FinalStatus = types.HardwareNodeStatus_INFERENCE
			return result
		}
	}

	// Stop node first
	if err := worker.mlClient.Stop(); err != nil {
		logging.Error("Failed to stop node for inference up", types.Nodes, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
		return result
	}

	// Start inference
	if len(worker.node.Node.Models) == 0 {
		result.Succeeded = false
		result.Error = "No models available"
		result.FinalStatus = types.HardwareNodeStatus_FAILED
		logging.Error(result.Error, types.Nodes, "node_id", worker.nodeId)
		return result
	}

	var model string
	var modelArgs []string
	for modelName, args := range worker.node.Node.Models {
		model = modelName
		modelArgs = args.Args
		break
	}

	if model == "" {
		result.Succeeded = false
		result.Error = "No inference model set in config"
		result.FinalStatus = types.HardwareNodeStatus_FAILED
		logging.Error(result.Error, types.Nodes, "node_id", worker.nodeId)
		return result
	}

	if err := worker.mlClient.InferenceUp(model, modelArgs); err != nil {
		logging.Error("Failed to bring up inference", types.Nodes, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
	} else {
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_INFERENCE
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
	if err := worker.mlClient.Stop(); err != nil {
		logging.Error("Failed to stop node for training", types.Training, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
		return result
	}

	// Start training
	trainingErr := worker.mlClient.StartTraining(
		c.TaskId, c.Participant, worker.nodeId,
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
