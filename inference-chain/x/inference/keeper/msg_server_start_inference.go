package keeper

import (
	"context"

	"encoding/base64"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

const DefaultMaxTokens = 5000

func (k msgServer) StartInference(goCtx context.Context, msg *types.MsgStartInference) (*types.MsgStartInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	k.LogInfo("StartInference", types.Inferences, "inferenceId", msg.InferenceId, "creator", msg.Creator, "requestedBy", msg.RequestedBy, "model", msg.Model)

	transferAgent, found := k.GetParticipant(ctx, msg.Creator)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.Creator)
	}
	dev, found := k.GetParticipant(ctx, msg.RequestedBy)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.RequestedBy)
	}

	k.LogInfo("DevPubKey", types.Inferences, "DevPubKey", dev.WorkerPublicKey, "DevAddress", dev.Address)
	k.LogInfo("TransferAgentPubKey", types.Inferences, "TransferAgentPubKey", transferAgent.WorkerPublicKey, "TransferAgentAddress", transferAgent.Address)

	err := k.verifyKeys(ctx, msg, transferAgent, dev)
	if err != nil {
		k.LogError("StartInference: verifyKeys failed", types.Inferences, "error", err)
		return nil, sdkerrors.Wrap(types.ErrInvalidSignature, err.Error())
	}

	existingInference, found := k.GetInference(ctx, msg.InferenceId)

	// Record the current price only if this is the first message (FinishInference not processed yet)
	// This ensures consistent pricing regardless of message arrival order
	if !existingInference.FinishedProcessed() {
		existingInference.Model = msg.Model
		k.RecordInferencePrice(goCtx, &existingInference, msg.InferenceId)
	}

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

func (k msgServer) verifyKeys(ctx context.Context, msg *types.MsgStartInference, agent types.Participant, dev types.Participant) error {
	components := getSignatureComponents(msg)

	// Create SignatureData with the necessary participants and signatures
	sigData := calculations.SignatureData{
		DevSignature:      msg.InferenceId, // Using InferenceId as the dev signature
		TransferSignature: msg.TransferSignature,
		Dev:               &dev,
		TransferAgent:     &agent,
	}

	// Use the generic VerifyKeys function
	err := calculations.VerifyKeys(ctx, components, sigData, k)
	if err != nil {
		k.LogError("StartInference: verifyKeys failed", types.Inferences, "error", err)
		return err
	}

	return nil
}

func (k msgServer) addTimeout(ctx sdk.Context, inference *types.Inference) {
	expirationBlocks := k.GetParams(ctx).ValidationParams.ExpirationBlocks
	expirationHeight := uint64(inference.StartBlockHeight + expirationBlocks)
	k.SetInferenceTimeout(ctx, types.InferenceTimeout{
		ExpirationHeight: expirationHeight,
		InferenceId:      inference.InferenceId,
	})

	k.LogInfo("Inference Timeout Set:", types.Inferences,
		"InferenceId", inference.InferenceId,
		"ExpirationHeight", inference.StartBlockHeight+expirationBlocks)
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
		k.BankKeeper.LogSubAccountTransaction(ctx, executor.Address, types.ModuleName, types.OwedSubAccount, types.GetCoin(executor.CoinBalance), "inference_finished:"+inference.InferenceId)
		k.SetParticipant(ctx, executor)
	}
	return inference, nil

}

func getSignatureComponents(msg *types.MsgStartInference) calculations.SignatureComponents {
	return calculations.SignatureComponents{
		Payload:         msg.OriginalPrompt,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.Creator,
		ExecutorAddress: msg.AssignedTo,
	}
}

func (k msgServer) GetAccountPubKey(ctx context.Context, address string) (string, error) {
	addr, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		k.LogError("getAccountPubKey: Invalid address", types.Participants, "address", address, "error", err)
		return "", err
	}
	acc := k.AccountKeeper.GetAccount(ctx, addr)
	if acc == nil {
		k.LogError("getAccountPubKey: Account not found", types.Participants, "address", address)
		return "", sdkerrors.Wrap(types.ErrParticipantNotFound, address)
	}
	return base64.StdEncoding.EncodeToString(acc.GetPubKey().Bytes()), nil
}

func (k msgServer) GetAccountPubKeysWithGrantees(ctx context.Context, granterAddress string) ([]string, error) {
	grantees, err := k.GranteesByMessageType(ctx, &types.QueryGranteesByMessageTypeRequest{
		GranterAddress: granterAddress,
		MessageTypeUrl: "/inference.inference.MsgStartInference",
	})
	if err != nil {
		return nil, err
	}
	pubKeys := make([]string, len(grantees.Grantees)+1)
	for i, grantee := range grantees.Grantees {
		pubKeys[i] = grantee.PubKey
	}
	granterPubKey, err := k.GetAccountPubKey(ctx, granterAddress)
	if err != nil {
		return nil, err
	}
	pubKeys[len(pubKeys)-1] = granterPubKey
	return pubKeys, nil
}
