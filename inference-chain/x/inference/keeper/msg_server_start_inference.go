package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

const DefaultMaxTokens = 5000

func (k msgServer) StartInference(goCtx context.Context, msg *types.MsgStartInference) (*types.MsgStartInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	k.LogInfo("StartInference", types.Inferences, "inferenceId", msg.InferenceId, "creator", msg.Creator, "requestedBy", msg.RequestedBy, "model", msg.Model)

	_, found := k.GetParticipant(ctx, msg.Creator)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.Creator)
	}
	_, found = k.GetParticipant(ctx, msg.RequestedBy)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.RequestedBy)
	}

	existingInference, found := k.GetInference(ctx, msg.InferenceId)

	blockContext := calculations.BlockContext{
		BlockHeight:    ctx.BlockHeight(),
		BlockTimestamp: ctx.BlockTime().UnixMilli(),
	}

	inference, payments, err := calculations.ProcessStartInference(&existingInference, msg, blockContext, k)
	if err != nil {
		return nil, err
	}

	finalInference, err := k.processInferencePayments(ctx, inference, payments)
	if err != nil {
		return nil, err
	}
	k.SetInference(ctx, *finalInference)
	err = k.SetDeveloperStats(ctx, *finalInference)
	if err != nil {
		k.LogError("error setting developer stat", types.Stat, err)
	} else {
		k.LogInfo("updated developer stat", types.Stat, "inference_id", inference.InferenceId, "inference_status", inference.Status.String(), "developer", inference.RequestedBy)
	}

	k.addTimeout(ctx, inference)

	if inference.IsCompleted() {
		err := k.handleInferenceCompleted(ctx, inference)
		if err != nil {
			return nil, err
		}
	}

	return &types.MsgStartInferenceResponse{
		InferenceIndex: msg.InferenceId,
	}, nil
}

func (k msgServer) addTimeout(ctx sdk.Context, inference *types.Inference) {
	expirationBlocks := k.GetParams(ctx).ValidationParams.ExpirationBlocks
	k.SetInferenceTimeout(ctx, types.InferenceTimeout{
		ExpirationHeight: uint64(inference.StartBlockHeight + expirationBlocks),
		InferenceId:      inference.InferenceId,
	})
	k.LogInfo("Inference Timeout Set:", types.Inferences, "InferenceId", inference.InferenceId, "ExpirationHeight", inference.StartBlockHeight+10)
}

func (k msgServer) processInferencePayments(
	ctx sdk.Context,
	inference *types.Inference,
	payments *calculations.Payments,
) (*types.Inference, error) {
	if payments.EscrowAmount > 0 {
		escrowAmount, err := k.PutPaymentInEscrow(ctx, inference, payments.EscrowAmount)
		if err != nil {
			return nil, err
		}
		inference.EscrowAmount = escrowAmount
	}
	if payments.EscrowAmount < 0 {
		err := k.IssueRefund(ctx, uint64(-payments.EscrowAmount), inference.RequestedBy, "inference_refund:"+inference.InferenceId)
		if err != nil {
			k.LogError("Unable to Issue Refund for started inference", types.Payments, err)
		}
	}
	if payments.ExecutorPayment > 0 {
		executedBy := inference.ExecutedBy
		executor, found := k.GetParticipant(ctx, executedBy)
		if !found {
			return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, executedBy)
		}
		executor.CoinBalance += payments.ExecutorPayment
		executor.CurrentEpochStats.EarnedCoins += uint64(payments.ExecutorPayment)
		executor.CurrentEpochStats.InferenceCount++
		executor.LastInferenceTime = inference.EndBlockTimestamp
		k.LogBalance(executor.Address, payments.ExecutorPayment, executor.CoinBalance, "inference_finished:"+inference.InferenceId)
		k.SetParticipant(ctx, executor)
	}
	return inference, nil

}
