package calculations

import (
	sdkerrors "cosmossdk.io/errors"
	"github.com/productscience/inference/x/inference/types"
)

type InferenceMessage interface{}

type StartInferenceMessage struct {
}

const (
	DefaultMaxTokens = 5000
	PerTokenCost     = 1000 // Legacy fallback price
)

type BlockContext struct {
	BlockHeight    int64
	BlockTimestamp int64
}

type Payments struct {
	EscrowAmount    int64
	ExecutorPayment int64
}

func ProcessStartInference(
	currentInference *types.Inference,
	startMessage *types.MsgStartInference,
	blockContext BlockContext,
	logger types.InferenceLogger,
) (*types.Inference, *Payments, error) {
	// nil should not happen, but we should always check to avoid panics
	if currentInference == nil {
		return nil, nil, sdkerrors.Wrap(types.ErrInferenceNotFound, startMessage.InferenceId)
	}
	if currentInference.InferenceId != "" && !finishedProcessed(currentInference) {
		// We already have an inference with this ID (but it wasn't created by FinishInference)
		return nil, nil, sdkerrors.Wrap(types.ErrInferenceIdExists, currentInference.InferenceId)
	}
	payments := &Payments{}
	if currentInference.InferenceId == "" {
		logger.LogInfo(
			"New Inference started",
			types.Inferences,
			"inferenceId",
			startMessage.InferenceId,
			"creator",
			startMessage.Creator,
			"requestedBy",
			startMessage.RequestedBy,
			"model",
			startMessage.Model,
			"assignedTo",
			startMessage.AssignedTo,
		)
		// Preserve the PerTokenPrice that was set by RecordInferencePrice
		existingPerTokenPrice := currentInference.PerTokenPrice
		currentInference = &types.Inference{
			Index:         startMessage.InferenceId,
			InferenceId:   startMessage.InferenceId,
			Status:        types.InferenceStatus_STARTED,
			PerTokenPrice: existingPerTokenPrice,
		}
	}
	// Works if FinishInference came before
	currentInference.RequestTimestamp = startMessage.RequestTimestamp
	currentInference.TransferredBy = startMessage.Creator
	currentInference.TransferSignature = startMessage.TransferSignature
	currentInference.PromptHash = startMessage.PromptHash
	if currentInference.PromptTokenCount == 0 {
		currentInference.PromptTokenCount = startMessage.PromptTokenCount
	}
	currentInference.RequestedBy = startMessage.RequestedBy
	currentInference.Model = startMessage.Model
	currentInference.StartBlockHeight = blockContext.BlockHeight
	currentInference.StartBlockTimestamp = blockContext.BlockTimestamp
	currentInference.MaxTokens = getMaxTokens(startMessage)
	currentInference.AssignedTo = startMessage.AssignedTo
	currentInference.NodeVersion = startMessage.NodeVersion

	if currentInference.EscrowAmount == 0 {
		if startMessage.PromptTokenCount == 0 {
			logger.LogWarn("PromptTokens is 0 when StartInference is called!", types.Inferences, "inferenceId", startMessage.InferenceId)
		}
		escrowAmount := CalculateEscrow(currentInference, startMessage.PromptTokenCount)
		// NOTE: inference.EscrowAmount is not set here. It will be set later, after escrow
		// has SUCCESSFULLY been transferred
		if finishedProcessed(currentInference) {
			setEscrowForFinished(currentInference, escrowAmount, payments)
		} else {
			payments.EscrowAmount = escrowAmount
		}
	}

	return currentInference, payments, nil
}

func setEscrowForFinished(currentInference *types.Inference, escrowAmount int64, payments *Payments) {
	actualCost := CalculateCost(currentInference)
	amountToPay := min(actualCost, escrowAmount)
	// ActualCost is used for refunds of invalid inferences and for sharing the cost with validators. It needs
	// to be the same as the amount actually paid, not the cost of the inference by itself.
	currentInference.ActualCost = amountToPay
	payments.EscrowAmount = amountToPay
	payments.ExecutorPayment = amountToPay
}

func ProcessFinishInference(
	currentInference *types.Inference,
	finishMessage *types.MsgFinishInference,
	blockContext BlockContext,
	logger types.InferenceLogger,
) (*types.Inference, *Payments) {
	payments := Payments{}
	logger.LogInfo("FinishInference being processed", types.Inferences)
	if currentInference.InferenceId == "" {
		logger.LogInfo(
			"FinishInference received before StartInference",
			types.Inferences,
			"inference_id",
			finishMessage.InferenceId,
		)
		// Preserve the PerTokenPrice that was set by RecordInferencePrice
		existingPerTokenPrice := currentInference.PerTokenPrice
		currentInference = &types.Inference{
			Index:         finishMessage.InferenceId,
			InferenceId:   finishMessage.InferenceId,
			Model:         finishMessage.Model,
			PerTokenPrice: existingPerTokenPrice,
		}
	}
	currentInference.Status = types.InferenceStatus_FINISHED
	currentInference.ResponseHash = finishMessage.ResponseHash
	// PromptTokenCount for Finish can be set to 0 if the inference was streamed and interrupted
	// before the end of the response. Then we should default to the value set in StartInference.
	logger.LogDebug("FinishInference with prompt token count", types.Inferences, "inference_id", finishMessage.InferenceId, "prompt_token_count", finishMessage.PromptTokenCount)
	if finishMessage.PromptTokenCount != 0 {
		currentInference.PromptTokenCount = finishMessage.PromptTokenCount
	}
	currentInference.RequestTimestamp = finishMessage.RequestTimestamp
	currentInference.TransferredBy = finishMessage.TransferredBy
	currentInference.TransferSignature = finishMessage.TransferSignature
	currentInference.ExecutionSignature = finishMessage.ExecutorSignature
	currentInference.CompletionTokenCount = finishMessage.CompletionTokenCount
	currentInference.ExecutedBy = finishMessage.ExecutedBy
	currentInference.EndBlockHeight = blockContext.BlockHeight
	currentInference.EndBlockTimestamp = blockContext.BlockTimestamp

	if currentInference.PromptTokenCount == 0 {
		logger.LogWarn("PromptTokens is 0 when FinishInference is called!", types.Inferences, "inferenceId", currentInference.InferenceId)
	}
	if currentInference.CompletionTokenCount == 0 {
		logger.LogWarn("CompletionTokens is 0 when FinishInference is called!", types.Inferences, "inferenceId", currentInference.InferenceId)
	}
	currentInference.ActualCost = CalculateCost(currentInference)
	if startProcessed(currentInference) {
		escrowAmount := currentInference.EscrowAmount
		if currentInference.ActualCost >= escrowAmount {
			payments.ExecutorPayment = escrowAmount
		} else {
			payments.ExecutorPayment = currentInference.ActualCost
			// Will be a negative number, meaning a refund
			payments.EscrowAmount = currentInference.ActualCost - escrowAmount
		}
	}
	return currentInference, &payments
}

func startProcessed(inference *types.Inference) bool {
	return inference.PromptHash != ""
}

func finishedProcessed(inference *types.Inference) bool {
	return inference.ExecutedBy != ""
}

func getMaxTokens(msg *types.MsgStartInference) uint64 {
	if msg.MaxTokens > 0 {
		return msg.MaxTokens
	}
	return DefaultMaxTokens
}

func CalculateCost(inference *types.Inference) int64 {
	// Simply use the per-token price stored in the inference
	// RecordInferencePrice ensures this is always set to the correct value:
	// - Dynamic price from BeginBlocker (including 0 for grace period)
	// - Legacy fallback price (1000) if dynamic pricing unavailable
	return int64(inference.CompletionTokenCount*inference.PerTokenPrice + inference.PromptTokenCount*inference.PerTokenPrice)
}

func CalculateEscrow(inference *types.Inference, promptTokens uint64) int64 {
	// Simply use the per-token price stored in the inference
	// RecordInferencePrice ensures this is always set to the correct value:
	// - Dynamic price from BeginBlocker (including 0 for grace period)
	// - Legacy fallback price (1000) if dynamic pricing unavailable
	return int64((inference.MaxTokens + promptTokens) * inference.PerTokenPrice)
}
