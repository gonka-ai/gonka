package keeper

import (
	"context"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) InvalidateInference(ctx context.Context, msg *types.MsgInvalidateInference) (*types.MsgInvalidateInferenceResponse, error) {
	inference, executor, err := k.validateDecisionMessage(ctx, msg)
	if err != nil {
		return nil, err
	}
	// Idempotent, so no error
	if inference.Status == types.InferenceStatus_INVALIDATED {
		k.LogDebug("Inference already invalidated", types.Validation, "inferenceId", msg.InferenceId)
		return nil, nil
	}
	inference.Status = types.InferenceStatus_INVALIDATED
	executor.CurrentEpochStats.InvalidatedInferences++
	executor.ConsecutiveInvalidInferences++
	epochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("Failed to get current epoch group", types.Validation, "error", err)
		return nil, err
	}

	shouldRefund, reason := k.inferenceIsBeforeClaimsSet(ctx, *inference, epochGroup)
	k.LogInfo("Inference refund decision", types.Validation, "inferenceId", inference.InferenceId, "executor", executor.Address, "shouldRefund", shouldRefund, "reason", reason)
	if shouldRefund {
		err := k.refundInvalidatedInference(executor, inference, ctx)
		if err != nil {
			return nil, err
		}
	}

	k.LogInfo("Inference invalidated", types.Inferences, "inferenceId", inference.InferenceId, "executor", executor.Address, "actualCost", inference.ActualCost)

	err = k.SetParticipant(ctx, *executor)
	if err != nil {
		return nil, err
	}

	err = k.SetInference(ctx, *inference)
	if err != nil {
		return nil, err
	}

	return &types.MsgInvalidateInferenceResponse{}, nil
}

func (k msgServer) refundInvalidatedInference(executor *types.Participant, inference *types.Inference, ctx context.Context) error {
	executor.CoinBalance -= inference.ActualCost
	k.SafeLogSubAccountTransaction(ctx, types.ModuleName, executor.Address, types.OwedSubAccount, inference.ActualCost, "inference_invalidated:"+inference.InferenceId)
	k.LogInfo("Invalid Inference subtracted from Executor CoinBalance ", types.Balances, "inferenceId", inference.InferenceId, "executor", executor.Address, "actualCost", inference.ActualCost, "coinBalance", executor.CoinBalance)
	// We need to refund the cost, so we have to lookup the person who paid
	payer, found := k.GetParticipant(ctx, inference.RequestedBy)
	if !found {
		k.LogError("Payer not found", types.Validation, "address", inference.RequestedBy)
		return types.ErrParticipantNotFound
	}
	err := k.IssueRefund(ctx, inference.ActualCost, payer.Address, "invalidated_inference:"+inference.InferenceId)
	if err != nil {
		k.LogError("Refund failed", types.Validation, "error", err)
	}
	return nil
}

type ValidationDecision interface {
	GetInferenceId() string
	GetCreator() string
	GetInvalidator() string
}

func (k msgServer) validateDecisionMessage(ctx context.Context, msg ValidationDecision) (*types.Inference, *types.Participant, error) {
	inference, found := k.GetInference(ctx, msg.GetInferenceId())
	if !found {
		k.LogError("Inference not found", types.Validation, "inferenceId", msg.GetInferenceId())
		return nil, nil, errorsmod.Wrapf(types.ErrInferenceNotFound, "inference with id %s not found", msg.GetInferenceId())
	}

	if msg.GetCreator() != inference.ProposalDetails.PolicyAddress {
		k.LogError("Invalid authority", types.Validation, "expected", inference.ProposalDetails.PolicyAddress, "got", msg.GetCreator())
		return nil, nil, errorsmod.Wrapf(types.ErrInvalidSigner, "invalid authority; expected %s, got %s", inference.ProposalDetails.PolicyAddress, msg.GetCreator())
	}

	addr, err := sdk.AccAddressFromBech32(msg.GetInvalidator())
	if err != nil {
		k.LogError("Invalidator address is invalid", types.Validation, "invalidator", msg.GetInvalidator())
	} else {
		err = k.ActiveInvalidations.Remove(ctx, collections.Join(addr, inference.InferenceId))
		if err != nil {
			k.LogError("Failed to remove active invalidation", types.Validation, "error", err)
		}

	}

	executor, found := k.GetParticipant(ctx, inference.ExecutedBy)
	if !found {
		k.LogError("Executor not found", types.Validation, "address", inference.ExecutedBy)
		return nil, nil, errorsmod.Wrapf(types.ErrParticipantNotFound, "participant with address %s not found", inference.ExecutedBy)
	}
	return &inference, &executor, nil
}
