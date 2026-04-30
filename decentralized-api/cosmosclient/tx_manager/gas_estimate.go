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
// builder and computes the matching fee amount from minGasPriceNgonka.
// Extracted from getSignedBytes so it's directly unit-testable without
// requiring keyring + signing setup.
//
// Pass minGasPriceNgonka=0 (current v0.2.12 mainnet config) to set zero
// fees regardless of gasWanted; the network-duty bypass also produces
// zero-fee txs but at a different layer.
func applyGasAndFee(tx client.TxBuilder, gasWanted uint64, minGasPriceNgonka int64) {
	if gasWanted == 0 || gasWanted > BatchGasLimit {
		gasWanted = BatchGasLimit
	}
	tx.SetGasLimit(gasWanted)
	if minGasPriceNgonka > 0 {
		feeAmount := math.NewIntFromUint64(gasWanted).MulRaw(minGasPriceNgonka)
		tx.SetFeeAmount(sdk.NewCoins(sdk.NewCoin("ngonka", feeAmount)))
	} else {
		tx.SetFeeAmount(sdk.Coins{})
	}
}

// Per-message-type gas estimates for sizing the batch's gasWanted before
// broadcast. Cosmos charges fees on gasWanted, not gasUsed, so over-sizing
// inflates routine costs while under-sizing causes OOG failures.
//
// Numbers are derived from a 24-hour mainnet sample (~385K txs, see
// /tmp/gonka-gas-analysis/report.md): p99 of observed gasUsed × ~1.5
// headroom (Option B). The headroom expects ~1% of txs to OOG and trigger
// a retry at higher gas; estimateBatchGas applies a multiplier per attempt
// to escape the OOG loop.
//
// The two known linear-scaling messages mirror their on-chain ConsumeGas
// formula instead of using a flat number, so the estimate tracks payload
// size correctly:
//   - MsgPoCV2StoreCommit: base + sum(entry.Count) * per_count
//   - MsgMLNodeWeightDistribution: base + total_node_entries * per_node
//
// Re-tune from a fresh mainnet sample after any of:
//   - new message type added to the protocol
//   - significant change to a message handler's state operations
//   - re-enabling MinGasPriceNgonka after a period of disabled fees (the
//     observed numbers in this table assume PoCV2 base/count gas is being
//     metered; if FeeParams changes the formula constants, refresh)
const (
	// txOverheadGas covers signature verification, fee deduction, the
	// other ante-handler decorators that run regardless of payload, AND
	// the cost of unwrapping the authz MsgExec wrapper that the warm key
	// uses to sign on behalf of the cold account. Mainnet hosts almost
	// always run in authz mode (warm key signs, cold pays), so anyone
	// retuning this number should keep the wrap cost in mind — direct
	// signing (no MsgExec) would consume slightly less.
	txOverheadGas = uint64(50_000)

	// gasRetryMultiplier is applied per retry attempt to escape the OOG
	// loop when the static estimate underestimates real gas. Doubling
	// reaches BatchGasLimit (1B) within 6 retries from any starting point
	// in the table, after which BatchGasLimit caps further growth.
	gasRetryMultiplier = 2.0

	// Per-message gas estimates. See file header for derivation.
	// Inference lifecycle (bypass-exempt, but sized for completeness).
	gasStartInference  = uint64(250_000)
	gasFinishInference = uint64(250_000)
	gasValidation      = uint64(1_500_000) // outliers up to 2.1M observed

	// PoC duty messages (MsgValidation handled above).
	gasSubmitPocBatch          = uint64(500_000)
	gasSubmitPocValidationsV2  = uint64(250_000)
	gasInvalidateInference     = uint64(500_000)
	gasRevalidateInference     = uint64(500_000)

	// PoCV2StoreCommit linear-scaling formula (mirrors on-chain
	// chargePoCV2StoreCommitGas in msg_server_poc_v2_commit.go).
	//
	// IMPORTANT: these mirror governance-tunable params:
	//   - gasPoCV2Base  ≈ FeeParams.base_validation_gas (default 500K) + sdk overhead
	//   - gasPoCV2PerCount ≈ FeeParams.gas_per_poc_count (default 100)
	// If governance bumps either FeeParams field, this estimator silently
	// underestimates and PoCV2 batches start OOG-retrying. The OOG retry
	// path will eventually succeed via the per-attempt doubling, so this
	// degrades gracefully — but it costs extra block time. Re-tune these
	// constants in the same governance window that changes FeeParams, or
	// (better, future work) read FeeParams at DAPI startup and use those
	// values dynamically with a fixed headroom multiplier.
	gasPoCV2Base    = uint64(600_000) // base + sdk overhead + 50% headroom
	gasPoCV2PerCount = uint64(150)    // 100 on-chain + 50% headroom

	// MLNodeWeightDistribution linear in entries.
	gasMLNodeBase    = uint64(100_000)
	gasMLNodePerNode = uint64(50_000)

	// Routine host duties (now bypass-exempt — see ante_fee.go isExemptMessageType).
	gasSubmitHardwareDiff = uint64(500_000) // observed max 435K, room to grow
	gasClaimRewards       = uint64(700_000) // scales with epoch inferences

	// Other host operations.
	gasSubmitSeed                 = uint64(80_000)
	gasSubmitNewParticipant       = uint64(150_000)
	gasSubmitNewUnfundedParticipant = uint64(150_000)
	gasBridgeExchange             = uint64(500_000)

	// BLS DKG (bypass-exempt). High variance — sized at observed max + 30%
	// to absorb network-size growth without OOG-retry storms during DKG.
	gasSubmitDealerPart                  = uint64(140_000_000)
	gasSubmitVerificationVector          = uint64(140_000_000)
	gasSubmitGroupKeyValidationSignature = uint64(160_000_000) // max 116M, p99 33M
	gasRespondDealerComplaints           = uint64(150_000_000)
	gasRequestThresholdSignature         = uint64(2_000_000)
	gasSubmitPartialSignature            = uint64(5_000_000)

	// Other Cosmos-SDK / cosmwasm message defaults.
	gasBankSend          = uint64(150_000)
	gasGovVote           = uint64(80_000)
	gasDepositCollateral = uint64(100_000)
	gasWasmExecute       = uint64(300_000)

	// Catch-all for unrecognized message types. Conservative enough to cover
	// most well-behaved messages; under-estimated cases trigger OOG retry.
	gasDefaultEstimate = uint64(500_000)
)

// estimateMsgGas returns the gas estimate for a single message. The estimate
// is intended to be the first-attempt gasWanted; OOG retries use a
// multiplier (see estimateBatchGas).
func estimateMsgGas(msg sdk.Msg) uint64 {
	v, _ := lookupMsgGas(msg)
	return v
}

// lookupMsgGas is the internal worker behind estimateMsgGas. It returns
// (estimate, true) if the message type has an explicit case in the switch,
// or (gasDefaultEstimate, false) if it falls through to the default. The
// boolean lets tests assert that every message type a host might broadcast
// is explicitly handled — the value alone can't tell us, since several
// legitimate estimates happen to coincide with the default.
func lookupMsgGas(msg sdk.Msg) (uint64, bool) {
	switch m := msg.(type) {
	// Inference lifecycle.
	case *inferencetypes.MsgStartInference:
		return gasStartInference, true
	case *inferencetypes.MsgFinishInference:
		return gasFinishInference, true
	case *inferencetypes.MsgValidation:
		return gasValidation, true
	case *inferencetypes.MsgInvalidateInference:
		return gasInvalidateInference, true
	case *inferencetypes.MsgRevalidateInference:
		return gasRevalidateInference, true

	// PoC duty.
	case *inferencetypes.MsgSubmitPocBatch:
		return gasSubmitPocBatch, true
	case *inferencetypes.MsgSubmitPocValidationsV2:
		return gasSubmitPocValidationsV2, true

	// PoCV2StoreCommit: linear in summed Count across entries.
	case *inferencetypes.MsgPoCV2StoreCommit:
		var totalCount uint64
		for _, e := range m.Entries {
			totalCount += uint64(e.Count)
		}
		return gasPoCV2Base + totalCount*gasPoCV2PerCount, true

	// MLNodeWeightDistribution: linear in total node entries across models.
	case *inferencetypes.MsgMLNodeWeightDistribution:
		var totalNodes uint64
		for _, e := range m.Entries {
			totalNodes += uint64(len(e.Weights))
		}
		return gasMLNodeBase + totalNodes*gasMLNodePerNode, true

	// Routine host duties (bypass-exempt).
	case *inferencetypes.MsgSubmitHardwareDiff:
		return gasSubmitHardwareDiff, true
	case *inferencetypes.MsgClaimRewards:
		return gasClaimRewards, true

	// Other host operations.
	case *inferencetypes.MsgSubmitSeed:
		return gasSubmitSeed, true
	case *inferencetypes.MsgSubmitNewParticipant:
		return gasSubmitNewParticipant, true
	case *inferencetypes.MsgSubmitNewUnfundedParticipant:
		return gasSubmitNewUnfundedParticipant, true
	case *inferencetypes.MsgBridgeExchange:
		return gasBridgeExchange, true

	// BLS DKG.
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

	// Collateral.
	case *collateraltypes.MsgDepositCollateral:
		return gasDepositCollateral, true

	default:
		return gasDefaultEstimate, false
	}
}

// estimateBatchGas returns the gasWanted for a batch of messages. The first
// attempt (attempt=0) sums per-message estimates plus tx-level overhead.
// Each subsequent attempt multiplies by gasRetryMultiplier to escape OOG
// loops when the static estimate underestimates real gas. The result is
// capped at BatchGasLimit so we never request more gas than the chain's
// network-duty bypass cap can accommodate.
func estimateBatchGas(msgs []sdk.Msg, attempt int) uint64 {
	gas := txOverheadGas
	for _, m := range msgs {
		gas += estimateMsgGas(m)
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
