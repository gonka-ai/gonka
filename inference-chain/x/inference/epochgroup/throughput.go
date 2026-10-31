package epochgroup

import (
	"context"
	"math"
	"math/big"

	"github.com/productscience/inference/x/inference/types"
)

// computeMLNodeThroughput returns tokens/second for a node given its PoC weight and
// the governance model's throughput parameters.
//
// UNITS ASSUMPTION: throughput_per_nonce is compute-units/second per nonce of PoC
// weight; units_of_compute_per_token converts that to tokens/second:
//
//	Throughput = PocWeight * ThroughputPerNonce / UnitsOfComputePerToken
//
// Pure integer arithmetic (consensus-critical). Zero inputs yield 0. Products that
// overflow int64 are truncated to math.MaxInt64.
func computeMLNodeThroughput(pocWeight int64, throughputPerNonce, unitsOfComputePerToken uint64) int64 {
	if pocWeight <= 0 || throughputPerNonce == 0 || unitsOfComputePerToken == 0 {
		return 0
	}

	num := new(big.Int).Mul(big.NewInt(pocWeight), new(big.Int).SetUint64(throughputPerNonce))
	den := new(big.Int).SetUint64(unitsOfComputePerToken)
	result := new(big.Int).Quo(num, den)
	if !result.IsInt64() {
		if result.Sign() < 0 {
			return 0
		}
		return math.MaxInt64
	}
	return result.Int64()
}

// populateNodeThroughputs sets MLNodeInfo.Throughput from the governance model
// for this subgroup. Called immediately before TotalThroughput is summed so the
// capacity path can prefer real tokens/s over the TotalWeight proxy.
func (eg *EpochGroup) populateNodeThroughputs(ctx context.Context, mlNodes []*types.MLNodeInfo, modelId string) {
	// Resolve model params up front; leave them at zero when the model is
	// missing / has no keeper / has zero params. computeMLNodeThroughput then
	// yields 0 for every node in those cases.
	var throughputPerNonce, unitsOfComputePerToken uint64
	if modelId != "" && eg.ModelKeeper != nil {
		if model, found := eg.ModelKeeper.GetGovernanceModel(ctx, modelId); found && model != nil {
			throughputPerNonce = model.ThroughputPerNonce
			unitsOfComputePerToken = model.UnitsOfComputePerToken
		}
	}

	// Always overwrite Throughput so a node preserved from a prior epoch (whose
	// pointer/value carried last epoch's computed throughput) never leaks a stale
	// nonzero value into TotalThroughput — that would defeat the TotalWeight
	// fallback for zero-param / legacy models.
	for _, node := range mlNodes {
		if node == nil {
			continue
		}
		node.Throughput = computeMLNodeThroughput(node.PocWeight, throughputPerNonce, unitsOfComputePerToken)
	}
}
