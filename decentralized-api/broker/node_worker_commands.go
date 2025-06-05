package broker

import (
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"errors"

	"github.com/productscience/inference/x/inference/types"
)

// NodeWorkerCommand defines the interface for commands executed by NodeWorker
type NodeWorkerCommand interface {
	Execute(worker *NodeWorker) error
}

// StopNodeCommand stops the ML node
type StopNodeCommand struct{}

func (c StopNodeCommand) Execute(worker *NodeWorker) error {
	err := worker.mlClient.Stop()
	if err != nil {
		logging.Error("Failed to stop node", types.Nodes,
			"node_id", worker.nodeId, "error", err)
		worker.node.State.Failure("Failed to stop")
		return err
	}
	worker.node.State.UpdateStatusNow(types.HardwareNodeStatus_STOPPED)
	return nil
}

// StartPoCNodeCommand starts PoC on a single node
type StartPoCNodeCommand struct {
	BlockHeight int64
	BlockHash   string
	PubKey      string
	CallbackUrl string
	TotalNodes  int
}

func (c StartPoCNodeCommand) Execute(worker *NodeWorker) error {
	// Check if already in PoC state (idempotent)
	status, err := worker.mlClient.GetPowStatus()
	if err == nil && status.Status == mlnodeclient.POW_GENERATING {
		logging.Info("Node already in PoC state", types.PoC, "node_id", worker.nodeId)
		worker.node.State.UpdateStatusNow(types.HardwareNodeStatus_POC)
		return nil
	}

	// Stop node if needed
	nodeState, _ := worker.mlClient.NodeState()
	if nodeState != nil && nodeState.State != mlnodeclient.MlNodeState_STOPPED {
		err := worker.mlClient.Stop()
		if err != nil {
			logging.Error("Failed to stop node for PoC", types.PoC,
				"node_id", worker.nodeId, "error", err)
			worker.node.State.Failure("Failed to stop for PoC")
			return err
		}
		worker.node.State.UpdateStatusNow(types.HardwareNodeStatus_STOPPED)
	}

	// Start PoC
	dto := mlnodeclient.BuildInitDto(
		c.BlockHeight,
		c.PubKey,
		int64(c.TotalNodes),
		int64(worker.node.Node.NodeNum),
		c.BlockHash,
		c.CallbackUrl,
	)
	err = worker.mlClient.InitGenerate(dto)
	if err != nil {
		logging.Error("Failed to start PoC", types.PoC,
			"node_id", worker.nodeId, "error", err)
		worker.node.State.Failure("Failed to start PoC")
		return err
	}

	worker.node.State.UpdateStatusNow(types.HardwareNodeStatus_POC)
	logging.Info("Successfully started PoC on node", types.PoC, "node_id", worker.nodeId)
	return nil
}

type InitValidateNodeCommand struct {
	BlockHeight int64
	BlockHash   string
	PubKey      string
	CallbackUrl string
	TotalNodes  int
}

func (c InitValidateNodeCommand) Execute(worker *NodeWorker) error {
	// Check if already in PoC state (idempotent)
	status, err := worker.mlClient.GetPowStatus()
	if err == nil && status.Status == mlnodeclient.POW_VALIDATING {
		logging.Info("Node already in POW_VALIDATING state", types.PoC, "node_id", worker.nodeId)
		worker.node.State.UpdateStatusNow(types.HardwareNodeStatus_POC)
		return nil
	}

	dto := mlnodeclient.BuildInitDto(
		c.BlockHeight,
		c.PubKey,
		int64(c.TotalNodes),
		int64(worker.node.Node.NodeNum),
		c.BlockHash,
		c.CallbackUrl,
	)

	err = worker.mlClient.InitValidate(dto)
	if err != nil {
		logging.Error("Failed to transition node to PoC init validate stage", types.PoC,
			"node_id", worker.nodeId, "error", err)
		worker.node.State.Failure("Failed to transitions PoC to init validate stage")
		return err
	}

	worker.node.State.UpdateStatusNow(types.HardwareNodeStatus_POC)
	logging.Info("Successfully transitioned node to PoC init validate stage", types.PoC, "node_id", worker.nodeId)
	return nil
}

// InferenceUpNodeCommand brings up inference on a single node
type InferenceUpNodeCommand struct{}

func (c InferenceUpNodeCommand) Execute(worker *NodeWorker) error {
	// Check if already in inference state (idempotent)
	state, err := worker.mlClient.NodeState()
	if err == nil && state.State == mlnodeclient.MlNodeState_INFERENCE {
		healthy, _ := worker.mlClient.InferenceHealth()
		if healthy {
			logging.Info("Node already in healthy inference state", types.Nodes, "node_id", worker.nodeId)
			worker.node.State.UpdateStatusNow(types.HardwareNodeStatus_INFERENCE)
			return nil
		}
	}

	// Stop node first
	err = worker.mlClient.Stop()
	if err != nil {
		logging.Error("Failed to stop node for inference up", types.Nodes,
			"node_id", worker.nodeId, "error", err)
		worker.node.State.Failure("Failed to stop for inference")
		return err
	}
	worker.node.State.UpdateStatusNow(types.HardwareNodeStatus_STOPPED)

	// Start inference
	if len(worker.node.Node.Models) == 0 {
		logging.Error("No models found for node", types.Nodes,
			"node_id", worker.nodeId)
		worker.node.State.Failure("No models available")
		return err
	}

	// Use first available model
	var model string
	var modelArgs []string
	for modelName, args := range worker.node.Node.Models {
		model = modelName
		modelArgs = args.Args
		break
	}

	if model == "" {
		logging.Error("No inference model set in config", types.Nodes,
			"node_id", worker.nodeId)
		worker.node.State.Failure("No inference model set in config")
		return errors.New("no model available for inference")
	}

	err = worker.mlClient.InferenceUp(model, modelArgs)
	if err != nil {
		logging.Error("Failed to bring up inference", types.Nodes,
			"node_id", worker.nodeId, "error", err)
		worker.node.State.Failure("Failed to start inference")
		return err
	}

	worker.node.State.UpdateStatusNow(types.HardwareNodeStatus_INFERENCE)
	logging.Info("Successfully brought up inference on node", types.Nodes, "node_id", worker.nodeId)
	return nil
}

// StartTrainingNodeCommand starts training on a single node
type StartTrainingNodeCommand struct {
	TaskId         uint64
	Participant    string
	MasterNodeAddr string
	NodeRanks      map[string]int
	WorldSize      int
}

func (c StartTrainingNodeCommand) Execute(worker *NodeWorker) error {
	rank, ok := c.NodeRanks[worker.nodeId]
	if !ok {
		logging.Error("Rank not found for node in StartTrainingNodeCommand", types.Training, "node_id", worker.nodeId)
		return errors.New("rank not found for node")
	}

	// Stop node first
	err := worker.mlClient.Stop()
	if err != nil {
		logging.Error("Failed to stop node for training", types.Training,
			"node_id", worker.nodeId, "error", err)
		worker.node.State.Failure("Failed to stop for training")
		return err
	}
	worker.node.State.UpdateStatusNow(types.HardwareNodeStatus_STOPPED)

	// Start training
	err = worker.mlClient.StartTraining(
		c.TaskId,
		c.Participant,
		worker.nodeId,
		c.MasterNodeAddr,
		rank,
		c.WorldSize,
	)
	if err != nil {
		logging.Error("Failed to start training", types.Training,
			"node_id", worker.nodeId, "error", err)
		worker.node.State.Failure("Failed to start training")
		return err
	}

	worker.node.State.UpdateStatusNow(types.HardwareNodeStatus_TRAINING)
	logging.Info("Successfully started training on node", types.Training,
		"node_id", worker.nodeId, "rank", rank, "task_id", c.TaskId)
	return nil
}

// NoOpNodeCommand is a command that does nothing (used as placeholder)
type NoOpNodeCommand struct {
	Message string
}

func (c *NoOpNodeCommand) Execute(worker *NodeWorker) error {
	if c.Message != "" {
		logging.Debug(c.Message, types.Nodes, "node_id", worker.nodeId)
	}
	return nil
}
