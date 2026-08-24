package tx_manager

import (
	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"

	blstypes "github.com/productscience/inference/x/bls/types"
	collateraltypes "github.com/productscience/inference/x/collateral/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// applyGasAndFee writes gasWanted (capped at BatchGasLimit) onto the tx
// builder and computes the matching fee from minGasPriceNgonka. Extracted
// for unit testing without keyring/signing setup.
func applyGasAndFee(tx client.TxBuilder, gasWanted uint64, minGasPriceNgonka int64) {
	if gasWanted == 0 || gasWanted > BatchGasLimit {
		gasWanted = BatchGasLimit
	}
	tx.SetGasLimit(gasWanted)
	if minGasPriceNgonka > 0 {
		feeAmount := math.NewIntFromUint64(gasWanted).MulRaw(minGasPriceNgonka)
		tx.SetFeeAmount(sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, feeAmount)))
	} else {
		tx.SetFeeAmount(sdk.Coins{})
	}
}

// Per-message gas estimates. Cosmos charges fees on gasWanted (not gasUsed),
// so over-sizing inflates routine costs and under-sizing causes OOG.
//
// Numbers are p99 of observed gasUsed × ~1.5 from a 24h mainnet sample
// (see /tmp/gonka-gas-analysis/report.md). The headroom expects ~1% of txs
// to OOG; estimateBatchGas doubles per attempt to escape the retry loop.
//
// Two messages have linear-scaling formulas mirroring their on-chain
// ConsumeGas: MsgPoCV2StoreCommit (per-Count) and MsgMLNodeWeightDistribution
// (per-entry). Re-tune from a fresh sample after a handler change, a new
// msg type, or a FeeParams.{base_validation_gas,gas_per_poc_count} change.
const (
	// Tx-level fixed cost: ante decorators + authz MsgExec unwrap. Mainnet
	// hosts almost always run in authz mode, so this absorbs the wrap cost.
	txOverheadGas = uint64(50_000)

	// Doubled per retry attempt so OOG-on-underestimate eventually fits.
	gasRetryMultiplier = 2.0

	// PoC duty messages.
	gasSubmitPocBatch         = uint64(500_000)
	gasSubmitPocValidationsV2 = uint64(250_000)

	// PoCV2StoreCommit linear formula used when the fee tree has not been
	// loaded yet. WARN: gasPoCV2Base mirrors FeeParams.base_validation_gas
	// (default 500K), gasPoCV2PerCount mirrors FeeParams.gas_per_poc_count
	// (default 100). If governance bumps either, retune both — OOG retry
	// will limp along but at the cost of wasted block time.
	gasPoCV2Base     = uint64(600_000) // 500K base + 50K sdk + 50% headroom
	gasPoCV2PerCount = uint64(150)     // 100 on-chain + 50%

	// Intrinsic StoreCommit/authz floor when the fee tree is loaded. The
	// tree period-base and rate×delta are a surcharge on top of this, not
	// a replacement. A warm-key MsgExec+feegrant CheckTx used ~64k before
	// the handler; 200k covers ante + handler with headroom. Zeroing the
	// leaf must not collapse gasWanted below this floor. estimateBatchGas
	// still adds txOverheadGas separately.
	gasStoreCommitIntrinsic = uint64(200_000)

	// MLNodeWeightDistribution: linear in total node entries.
	gasMLNodeBase    = uint64(100_000)
	gasMLNodePerNode = uint64(50_000)

	// Routine host duties (bypass-exempt).
	gasSubmitHardwareDiff = uint64(500_000) // observed max 435K
	gasClaimRewards       = uint64(700_000) // scales w/ epoch inferences

	// Other host operations.
	gasSubmitSeed                   = uint64(80_000)
	gasSubmitNewParticipant         = uint64(150_000)
	gasSubmitNewUnfundedParticipant = uint64(150_000)
	gasBridgeExchange               = uint64(500_000)

	// BLS DKG (bypass-exempt). Sized at observed max + 30% to absorb
	// network-size growth without OOG-retry storms during DKG.
	gasSubmitDealerPart                  = uint64(140_000_000)
	gasSubmitVerificationVector          = uint64(140_000_000)
	gasSubmitGroupKeyValidationSignature = uint64(160_000_000) // max 116M, p99 33M
	gasRespondDealerComplaints           = uint64(150_000_000)
	gasRequestThresholdSignature         = uint64(2_000_000)
	gasSubmitPartialSignature            = uint64(5_000_000)

	// Cosmos-SDK / cosmwasm.
	gasBankSend          = uint64(150_000)
	gasGovVote           = uint64(80_000)
	gasDepositCollateral = uint64(100_000)
	gasWasmExecute       = uint64(300_000)

	// Catch-all for unrecognized types. OOG retry covers the under-estimated case.
	gasDefaultEstimate = uint64(500_000)
)

// GasHints carries cached fee-tree rates and StoreCommit lastCommitted
// counts into the estimator. Empty hints keep first-of-stage (total count) math.
type GasHints struct {
	StoreCommitPrev    map[string]uint32
	StoreCommitRate    uint64
	StoreCommitBase    uint64
	HasStoreCommitRate bool
	HasStoreCommitBase bool
	HDGasPerByte       uint64
	HDUnitSize         uint64 // 1 (b), 1000 (kb), or 1_000_000 (mb); 0 treated as 1
	HasHDGasPerByte    bool
	FeeTreeLoaded      bool
	HardwarePrev       []*inferencetypes.HardwareNode
}

func (h GasHints) pocRate() uint64 {
	if h.HasStoreCommitRate {
		return h.StoreCommitRate
	}
	if h.FeeTreeLoaded {
		return 0
	}
	if h.StoreCommitRate == 0 {
		return gasPoCV2PerCount
	}
	return h.StoreCommitRate
}

func (h GasHints) pocBase() uint64 {
	if h.HasStoreCommitBase {
		return h.StoreCommitBase
	}
	if h.FeeTreeLoaded {
		return 0
	}
	if h.StoreCommitBase == 0 {
		return gasPoCV2Base
	}
	return h.StoreCommitBase
}

// estimateMsgGas returns the gas estimate for a single message.
func estimateMsgGas(msg sdk.Msg) uint64 {
	v, _ := lookupMsgGas(msg)
	return v
}

func estimateMsgGasHinted(msg sdk.Msg, hints GasHints) uint64 {
	v, _ := lookupMsgGasHinted(msg, hints)
	return v
}

// lookupMsgGas returns (estimate, true) for an explicit case in the switch
// or (gasDefaultEstimate, false) otherwise. Tests use the bool to assert
// every msg in InferenceOperationKeyPerms has an explicit case — the value
// alone can't tell us, since several legit estimates equal the default.
func lookupMsgGas(msg sdk.Msg) (uint64, bool) {
	return lookupMsgGasHinted(msg, GasHints{})
}

func lookupMsgGasHinted(msg sdk.Msg, hints GasHints) (uint64, bool) {
	switch m := msg.(type) {
	case *inferencetypes.MsgSubmitPocBatch:
		return gasSubmitPocBatch, true
	case *inferencetypes.MsgSubmitPocValidationsV2:
		return gasSubmitPocValidationsV2, true
	case *inferencetypes.MsgPoCV2StoreCommit:
		return estimateStoreCommitGas(m, hints), true
	case *inferencetypes.MsgMLNodeWeightDistribution:
		var totalNodes uint64
		for _, e := range m.Entries {
			totalNodes += uint64(len(e.Weights))
		}
		return gasMLNodeBase + totalNodes*gasMLNodePerNode, true
	case *inferencetypes.MsgSubmitHardwareDiff:
		return estimateHardwareDiffGas(m, hints), true
	case *inferencetypes.MsgClaimRewards:
		return gasClaimRewards, true
	case *inferencetypes.MsgSubmitSeed:
		return gasSubmitSeed, true
	case *inferencetypes.MsgSubmitNewParticipant:
		return gasSubmitNewParticipant, true
	case *inferencetypes.MsgSubmitNewUnfundedParticipant:
		return gasSubmitNewUnfundedParticipant, true
	case *inferencetypes.MsgBridgeExchange:
		return gasBridgeExchange, true
	// both write a couple of small entries; left at the default until a run measures them
	case *inferencetypes.MsgRefreshTrainingNodeOptIn:
		return gasDefaultEstimate, true
	case *inferencetypes.MsgAutokickTrainshardNode:
		return gasDefaultEstimate, true
	case *blstypes.MsgSubmitDealerPart:
		return gasSubmitDealerPart, true
	case *blstypes.MsgSubmitVerificationVector:
		return gasSubmitVerificationVector, true
	case *blstypes.MsgSubmitGroupKeyValidationSignature:
		return gasSubmitGroupKeyValidationSignature, true
	case *blstypes.MsgRespondDealerComplaints:
		return gasRespondDealerComplaints, true
	case *blstypes.MsgRequestThresholdSignature:
		return gasRequestThresholdSignature, true
	case *blstypes.MsgSubmitPartialSignature:
		return gasSubmitPartialSignature, true
	case *collateraltypes.MsgDepositCollateral:
		return gasDepositCollateral, true
	default:
		return gasDefaultEstimate, false
	}
}

func estimateHardwareDiffGas(m *inferencetypes.MsgSubmitHardwareDiff, hints GasHints) uint64 {
	gas := gasSubmitHardwareDiff
	if !hints.HasHDGasPerByte || hints.HDGasPerByte == 0 {
		return gas
	}
	qty := hardwareDiffByteDelta(m, hints.HardwarePrev)
	div := hints.HDUnitSize
	if div == 0 {
		div = 1
	}
	extra := saturatingMul(qty, hints.HDGasPerByte) / div
	extra = saturatingAdd(extra, extra/2) // 1.5× headroom, same as *3/2 without overflow
	return saturatingAdd(gas, extra)
}

// hardwareDiffByteDelta mirrors chain extra gas: max(0, size(after)−size(before))
// of the merged inventory, never N × size(new_or_modified).
func hardwareDiffByteDelta(m *inferencetypes.MsgSubmitHardwareDiff, prev []*inferencetypes.HardwareNode) uint64 {
	nodeMap := make(map[string]*inferencetypes.HardwareNode, len(prev)+len(m.GetNewOrModified()))
	for _, n := range prev {
		if n != nil {
			nodeMap[n.LocalId] = n
		}
	}
	before := 0
	if len(nodeMap) > 0 {
		before = hardwareNodesSize(m.GetCreator(), nodeMap)
	}
	for _, n := range m.GetRemoved() {
		if n != nil {
			delete(nodeMap, n.LocalId)
		}
	}
	for _, n := range m.GetNewOrModified() {
		if n != nil {
			nodeMap[n.LocalId] = n
		}
	}
	after := hardwareNodesSize(m.GetCreator(), nodeMap)
	if after > before {
		return uint64(after - before)
	}
	return 0
}

func hardwareNodesSize(participant string, nodeMap map[string]*inferencetypes.HardwareNode) int {
	nodes := make([]*inferencetypes.HardwareNode, 0, len(nodeMap))
	for _, n := range nodeMap {
		nodes = append(nodes, n)
	}
	wrap := &inferencetypes.HardwareNodes{
		Participant:   participant,
		HardwareNodes: nodes,
	}
	return wrap.Size()
}

func estimateStoreCommitGas(m *inferencetypes.MsgPoCV2StoreCommit, hints GasHints) uint64 {
	rate := hints.pocRate()
	base := hints.pocBase()
	var delta uint64
	anyPrev := false
	for _, e := range m.Entries {
		if e == nil {
			continue
		}
		prev, ok := hints.StoreCommitPrev[e.ModelId]
		if ok {
			anyPrev = true
			if e.Count > prev {
				delta += uint64(e.Count - prev)
			}
		} else {
			delta += uint64(e.Count)
		}
	}
	extra := saturatingMul(delta, rate)
	if !anyPrev {
		extra = saturatingAdd(extra, base)
	}
	if hints.FeeTreeLoaded {
		return saturatingAdd(gasStoreCommitIntrinsic, extra)
	}
	return extra
}

func saturatingMul(a, b uint64) uint64 {
	if a == 0 || b == 0 {
		return 0
	}
	prod := a * b
	if prod/a != b {
		return ^uint64(0)
	}
	return prod
}

func saturatingAdd(a, b uint64) uint64 {
	if a > ^uint64(0)-b {
		return ^uint64(0)
	}
	return a + b
}

// estimateBatchGas sums per-msg estimates + tx overhead, then doubles per
// retry attempt (capped at BatchGasLimit) so OOG retries escape the loop.
func estimateBatchGas(msgs []sdk.Msg, attempt int, hints ...GasHints) uint64 {
	var h GasHints
	if len(hints) > 0 {
		h = hints[0]
	}
	gas := txOverheadGas
	for _, m := range msgs {
		gas = saturatingAdd(gas, estimateMsgGasHinted(m, h))
	}
	for i := 0; i < attempt; i++ {
		gas = uint64(float64(gas) * gasRetryMultiplier)
		if gas > BatchGasLimit {
			break
		}
	}
	if gas > BatchGasLimit {
		return BatchGasLimit
	}
	return gas
}

func isHardwareDiffOnly(msgs []sdk.Msg) bool {
	if len(msgs) != 1 {
		return false
	}
	_, ok := msgs[0].(*inferencetypes.MsgSubmitHardwareDiff)
	return ok
}

// gasWantedFromSimulate takes max(static, simulated×1.5) so a fat same-size
// HardwareDiff rewrite is sized from real KV gas, not only stored_bytes growth.
func gasWantedFromSimulate(static, simulated uint64) uint64 {
	if simulated == 0 {
		return static
	}
	withHeadroom := saturatingAdd(simulated, simulated/2)
	if withHeadroom < static {
		withHeadroom = static
	}
	if withHeadroom > BatchGasLimit {
		return BatchGasLimit
	}
	return withHeadroom
}
