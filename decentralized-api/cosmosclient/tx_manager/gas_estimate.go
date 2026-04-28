package tx_manager

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	blstypes "github.com/productscience/inference/x/bls/types"
	collateraltypes "github.com/productscience/inference/x/collateral/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

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
	// txOverheadGas covers signature verification, fee deduction, and the
	// other ante-handler decorators that run regardless of payload.
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
	switch m := msg.(type) {
	// Inference lifecycle.
	case *inferencetypes.MsgStartInference:
		return gasStartInference
	case *inferencetypes.MsgFinishInference:
		return gasFinishInference
	case *inferencetypes.MsgValidation:
		return gasValidation
	case *inferencetypes.MsgInvalidateInference:
		return gasInvalidateInference
	case *inferencetypes.MsgRevalidateInference:
		return gasRevalidateInference

	// PoC duty.
	case *inferencetypes.MsgSubmitPocBatch:
		return gasSubmitPocBatch
	case *inferencetypes.MsgSubmitPocValidationsV2:
		return gasSubmitPocValidationsV2

	// PoCV2StoreCommit: linear in summed Count across entries.
	case *inferencetypes.MsgPoCV2StoreCommit:
		var totalCount uint64
		for _, e := range m.Entries {
			totalCount += uint64(e.Count)
		}
		return gasPoCV2Base + totalCount*gasPoCV2PerCount

	// MLNodeWeightDistribution: linear in total node entries across models.
	case *inferencetypes.MsgMLNodeWeightDistribution:
		var totalNodes uint64
		for _, e := range m.Entries {
			totalNodes += uint64(len(e.Weights))
		}
		return gasMLNodeBase + totalNodes*gasMLNodePerNode

	// Routine host duties (bypass-exempt).
	case *inferencetypes.MsgSubmitHardwareDiff:
		return gasSubmitHardwareDiff
	case *inferencetypes.MsgClaimRewards:
		return gasClaimRewards

	// Other host operations.
	case *inferencetypes.MsgSubmitSeed:
		return gasSubmitSeed
	case *inferencetypes.MsgSubmitNewParticipant:
		return gasSubmitNewParticipant
	case *inferencetypes.MsgSubmitNewUnfundedParticipant:
		return gasSubmitNewUnfundedParticipant
	case *inferencetypes.MsgBridgeExchange:
		return gasBridgeExchange

	// BLS DKG.
	case *blstypes.MsgSubmitDealerPart:
		return gasSubmitDealerPart
	case *blstypes.MsgSubmitVerificationVector:
		return gasSubmitVerificationVector
	case *blstypes.MsgSubmitGroupKeyValidationSignature:
		return gasSubmitGroupKeyValidationSignature
	case *blstypes.MsgRespondDealerComplaints:
		return gasRespondDealerComplaints
	case *blstypes.MsgRequestThresholdSignature:
		return gasRequestThresholdSignature
	case *blstypes.MsgSubmitPartialSignature:
		return gasSubmitPartialSignature

	// Collateral.
	case *collateraltypes.MsgDepositCollateral:
		return gasDepositCollateral

	default:
		return gasDefaultEstimate
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
