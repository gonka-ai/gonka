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

	// Inference lifecycle (bypass-exempt).
	gasStartInference  = uint64(250_000)
	gasFinishInference = uint64(250_000)
	gasValidation      = uint64(1_500_000) // outliers up to 2.1M observed

	// PoC duty messages.
	gasSubmitPocBatch         = uint64(500_000)
	gasSubmitPocValidationsV2 = uint64(250_000)
	gasInvalidateInference    = uint64(500_000)
	gasRevalidateInference    = uint64(500_000)

	// PoCV2StoreCommit linear formula. WARN: gasPoCV2Base mirrors
	// FeeParams.base_validation_gas (default 500K), gasPoCV2PerCount
	// mirrors FeeParams.gas_per_poc_count (default 100). If governance
	// bumps either, retune both — OOG retry will limp along but at the
	// cost of wasted block time. Future work: read FeeParams at startup.
	gasPoCV2Base     = uint64(600_000) // 500K base + 50K sdk + 50% headroom
	gasPoCV2PerCount = uint64(150)     // 100 on-chain + 50%

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

// estimateMsgGas returns the gas estimate for a single message.
func estimateMsgGas(msg sdk.Msg) uint64 {
	v, _ := lookupMsgGas(msg)
	return v
}

// lookupMsgGas returns (estimate, true) for an explicit case in the switch
// or (gasDefaultEstimate, false) otherwise. Tests use the bool to assert
// every msg in InferenceOperationKeyPerms has an explicit case — the value
// alone can't tell us, since several legit estimates equal the default.
func lookupMsgGas(msg sdk.Msg) (uint64, bool) {
	switch m := msg.(type) {
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
	case *inferencetypes.MsgSubmitPocBatch:
		return gasSubmitPocBatch, true
	case *inferencetypes.MsgSubmitPocValidationsV2:
		return gasSubmitPocValidationsV2, true
	case *inferencetypes.MsgPoCV2StoreCommit:
		var totalCount uint64
		for _, e := range m.Entries {
			totalCount += uint64(e.Count)
		}
		return gasPoCV2Base + totalCount*gasPoCV2PerCount, true
	case *inferencetypes.MsgMLNodeWeightDistribution:
		var totalNodes uint64
		for _, e := range m.Entries {
			totalNodes += uint64(len(e.Weights))
		}
		return gasMLNodeBase + totalNodes*gasMLNodePerNode, true
	case *inferencetypes.MsgSubmitHardwareDiff:
		return gasSubmitHardwareDiff, true
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

// estimateBatchGas sums per-msg estimates + tx overhead, then doubles per
// retry attempt (capped at BatchGasLimit) so OOG retries escape the loop.
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
