package broker

import (
	"context"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"

	"github.com/productscience/inference/x/inference/types"
)

// StartPoCNodeCommandV1 starts V1 PoC generation on a single node.
// Key V1 difference: MLNode MUST be stopped before state transitions.
type StartPoCNodeCommandV1 struct {
	BlockHeight int64
	BlockHash   string
	PubKey      string
	CallbackUrl string
	TotalNodes  int
	ModelParams *types.PoCModelParams
}

func (c StartPoCNodeCommandV1) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
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

	// Idempotency check using V1 status endpoint
	state, err := worker.GetClient().NodeState(ctx)
	if err == nil && state.State == mlnodeclient.MlNodeState_POW {
		powStatus, powErr := worker.GetClient().GetPowStatusV1(ctx)
		if powErr == nil && powStatus.Status == mlnodeclient.PowStateV1Generating {
			logging.Info("[StartPoCNodeCommandV1] Node already in PoC generating state", types.PoC, "node_id", worker.nodeId)
			result.Succeeded = true
			result.FinalStatus = types.HardwareNodeStatus_POC
			result.FinalPocStatus = PocStatusGenerating
			return result
		}
	}

	// V1: Stop node if needed (required before state transitions)
	if state != nil && state.State != mlnodeclient.MlNodeState_STOPPED {
		if err := worker.GetClient().Stop(ctx); err != nil {
			logging.Error("[StartPoCNodeCommandV1] Failed to stop node for PoC", types.PoC, "node_id", worker.nodeId, "error", err)
			result.Succeeded = false
			result.Error = err.Error()
			result.FinalStatus = types.HardwareNodeStatus_FAILED
			return result
		}
	}

	// Start V1 PoC
	dto := mlnodeclient.BuildInitDtoV1(
		c.BlockHeight, c.PubKey, int64(c.TotalNodes),
		worker.node.Node.NodeNum, c.BlockHash, c.CallbackUrl, c.ModelParams,
	)
	if err := worker.GetClient().InitGenerateV1(ctx, dto); err != nil {
		logging.Error("[StartPoCNodeCommandV1] Failed to start PoC", types.PoC, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
	} else {
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_POC
		result.FinalPocStatus = PocStatusGenerating
		logging.Info("[StartPoCNodeCommandV1] Successfully started PoC on node", types.PoC, "node_id", worker.nodeId)
	}
	return result
}

// InitValidateNodeCommandV1 initiates V1 PoC validation on a single node.
// Key V1 difference: Makes network call to MLNode via InitValidateV1().
type InitValidateNodeCommandV1 struct {
	BlockHeight int64
	BlockHash   string
	PubKey      string
	CallbackUrl string
	TotalNodes  int
	ModelParams *types.PoCModelParams
}

func (c InitValidateNodeCommandV1) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
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
		powStatus, powErr := worker.GetClient().GetPowStatusV1(ctx)
		if powErr == nil && powStatus.Status == mlnodeclient.PowStateV1Validating {
			logging.Info("[InitValidateNodeCommandV1] Node already in PoC validating state", types.PoC, "node_id", worker.nodeId)
			result.Succeeded = true
			result.FinalStatus = types.HardwareNodeStatus_POC
			result.FinalPocStatus = PocStatusValidating
			return result
		}
	}

	// V1: Stop node if needed (but allow POW state to continue)
	if state != nil && state.State != mlnodeclient.MlNodeState_STOPPED && state.State != mlnodeclient.MlNodeState_POW {
		if err := worker.GetClient().Stop(ctx); err != nil {
			logging.Error("[InitValidateNodeCommandV1] Failed to stop node for PoC validation", types.PoC, "node_id", worker.nodeId, "error", err)
			result.Succeeded = false
			result.Error = err.Error()
			result.FinalStatus = types.HardwareNodeStatus_FAILED
			return result
		}
	}

	dto := mlnodeclient.BuildInitDtoV1(
		c.BlockHeight, c.PubKey, int64(c.TotalNodes),
		worker.node.Node.NodeNum, c.BlockHash, c.CallbackUrl, c.ModelParams,
	)

	if err := worker.GetClient().InitValidateV1(ctx, dto); err != nil {
		logging.Error("[InitValidateNodeCommandV1] Failed to transition to PoC validate", types.PoC, "node_id", worker.nodeId, "error", err)
		result.Succeeded = false
		result.Error = err.Error()
		result.FinalStatus = types.HardwareNodeStatus_FAILED
	} else {
		result.Succeeded = true
		result.FinalStatus = types.HardwareNodeStatus_POC
		result.FinalPocStatus = PocStatusValidating
		logging.Info("[InitValidateNodeCommandV1] Successfully transitioned node to PoC init validate stage", types.PoC, "node_id", worker.nodeId)
	}
	return result
}
