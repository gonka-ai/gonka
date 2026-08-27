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

// GasHints is the estimator snapshot from FeeTreeCache.
// StoreCommit and HardwareDiff fields are only read by those estimators.
type GasHints struct {
	FeeTreeLoaded bool
	StoreCommit   StoreCommitGas
	HardwareDiff  HardwareDiffGas
}

// StoreCommitGas is split by estimator path.
// Static fallback uses Padded* + Prev. Measured uses Intrinsic/Entries + Chain*.
type StoreCommitGas struct {
	Prev map[string]uint32 // last committed counts this stage (both paths)

	// Static fallback: DAPI-padded tree leaves (100→150, 500k→600k).
	HasRate    bool
	HasBase    bool
	PaddedRate uint64
	PaddedBase uint64

	// Measured path: once-per-stage dummy Simulate.
	HasMeasured       bool
	MeasuredIntrinsic uint64
	MeasuredEntries   uint // dummy entry count; more entries → static
	ChainRate         uint64
	ChainBase         uint64
}

// HardwareDiffGas is the static extra-bytes formula. A successful Simulate
// does not read these; they are the fallback when Simulate fails.
type HardwareDiffGas struct {
	Prev          []*inferencetypes.HardwareNode
	HasGasPerByte bool
	GasPerByte    uint64
	UnitSize      uint64 // 1 (b), 1000 (kb), or 1_000_000 (mb); 0 treated as 1
}

func (h GasHints) storeCommitStaticRate() uint64 {
	sc := h.StoreCommit
	if sc.HasRate {
		return sc.PaddedRate
	}
	if h.FeeTreeLoaded {
		return 0
	}
	if sc.PaddedRate == 0 {
		return gasPoCV2PerCount
	}
	return sc.PaddedRate
}

func (h GasHints) storeCommitStaticBase() uint64 {
	sc := h.StoreCommit
	if sc.HasBase {
		return sc.PaddedBase
	}
	if h.FeeTreeLoaded {
		return 0
	}
	if sc.PaddedBase == 0 {
		return gasPoCV2Base
	}
	return sc.PaddedBase
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
	hd := hints.HardwareDiff
	if !hd.HasGasPerByte || hd.GasPerByte == 0 {
		return gas
	}
	qty := hardwareDiffByteDelta(m, hd.Prev)
	div := hd.UnitSize
	if div == 0 {
		div = 1
	}
	extra := saturatingMul(qty, hd.GasPerByte) / div
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
	sc := hints.StoreCommit
	if useMeasuredStoreCommitGas(m, hints) {
		extra := storeCommitSurcharge(m, sc.ChainRate, sc.ChainBase, sc.Prev)
		return applySimulateHeadroom(saturatingAdd(sc.MeasuredIntrinsic, extra))
	}
	extra := storeCommitSurcharge(m, hints.storeCommitStaticRate(), hints.storeCommitStaticBase(), sc.Prev)
	if hints.FeeTreeLoaded {
		return saturatingAdd(gasStoreCommitIntrinsic, extra)
	}
	return extra
}

func useMeasuredStoreCommitGas(m *inferencetypes.MsgPoCV2StoreCommit, hints GasHints) bool {
	sc := hints.StoreCommit
	if !sc.HasMeasured {
		return false
	}
	n := storeCommitEntryCount(m)
	return sc.MeasuredEntries > 0 && n <= sc.MeasuredEntries
}

func storeCommitEntryCount(m *inferencetypes.MsgPoCV2StoreCommit) uint {
	if m == nil {
		return 0
	}
	var n uint
	for _, e := range m.Entries {
		if e != nil {
			n++
		}
	}
	return n
}

func storeCommitSurcharge(m *inferencetypes.MsgPoCV2StoreCommit, rate, base uint64, prev map[string]uint32) uint64 {
	var delta uint64
	for _, e := range m.Entries {
		if e == nil {
			continue
		}
		p, ok := prev[e.ModelId]
		if ok {
			if e.Count > p {
				delta += uint64(e.Count - p)
			}
		} else {
			delta += uint64(e.Count)
		}
	}
	extra := saturatingMul(delta, rate)
	// Chain charges period base once per participant+stage
	// (len(existingByModel)==0), not once per new model in this payload.
	if len(prev) == 0 {
		extra = saturatingAdd(extra, base)
	}
	return extra
}

// StoreCommitIntrinsicFromSim peels the dummy commit's chain surcharge
// (period base + rate×dummyCount) off Simulate gasUsed. Dummy is count=1
// per local model on an empty stage. Returns false if the peel would
// underflow — caller keeps the static formula.
func StoreCommitIntrinsicFromSim(simUsed, rate, base, dummyCount uint64) (uint64, bool) {
	if simUsed == 0 || dummyCount == 0 {
		return 0, false
	}
	extra := saturatingAdd(base, saturatingMul(rate, dummyCount))
	if simUsed <= extra {
		return 0, false
	}
	return simUsed - extra, true
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
	if batchUsesMeasuredStoreCommitGas(msgs, h) {
		// Measured intrinsic already includes ante + authz unwrap.
		gas = 0
	}
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

func isStoreCommitOnly(msgs []sdk.Msg) bool {
	if len(msgs) == 0 {
		return false
	}
	for _, m := range msgs {
		if _, ok := m.(*inferencetypes.MsgPoCV2StoreCommit); !ok {
			return false
		}
	}
	return true
}

func batchUsesMeasuredStoreCommitGas(msgs []sdk.Msg, hints GasHints) bool {
	if !isStoreCommitOnly(msgs) {
		return false
	}
	for _, m := range msgs {
		sc, ok := m.(*inferencetypes.MsgPoCV2StoreCommit)
		if !ok || !useMeasuredStoreCommitGas(sc, hints) {
			return false
		}
	}
	return true
}

// applySimulateHeadroom is 1.2× with saturating arithmetic.
func applySimulateHeadroom(v uint64) uint64 {
	return saturatingAdd(v, v/5)
}

// gasWantedFromSimulate pads a successful Simulate with 1.2×. static is
// used only when Simulate returned 0; a working sim is never raised back
// to the static HardwareDiff floor.
//
// TODO(simulate-gas): Re-measure Simulate vs FinalizeBlock gas_used on a small
// HardwareDiff after CountTXSimulateGasDecorator + UnorderedNonceSimGasDecorator.
// HardwareRelabelTests previously needed 1.5× (38_627 sim vs 48_212 deliver).
// If deliver_used still exceeds simulated×1.2, add a fixed pad — do not go back
// to 1.5× on large txs.
func gasWantedFromSimulate(static, simulated uint64) uint64 {
	if simulated == 0 {
		return static
	}
	withHeadroom := applySimulateHeadroom(simulated)
	if withHeadroom > BatchGasLimit {
		return BatchGasLimit
	}
	return withHeadroom
}
