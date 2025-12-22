package keeper

import (
	"context"
	"time"

	"encoding/base64"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) StartInference(goCtx context.Context, msg *types.MsgStartInference) (*types.MsgStartInferenceResponse, error) {
	var ctx sdk.Context = sdk.UnwrapSDKContext(goCtx)
	k.LogInfo("StartInference", types.Inferences, "inferenceId", msg.InferenceId, "creator", msg.Creator, "requestedBy", msg.RequestedBy, "model", msg.Model)

	transferAgent, found := k.GetParticipant(ctx, msg.Creator)
	if !found {
		k.LogError("Creator not found", types.Inferences, "creator", msg.Creator, "msg", "StartInference")
		return failedStart(ctx, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.Creator), msg), nil
	}
	dev, found := k.GetParticipant(ctx, msg.RequestedBy)
	if !found {
		k.LogError("RequestedBy not found", types.Inferences, "requestedBy", msg.RequestedBy, "msg", "StartInference")
		return failedStart(ctx, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.RequestedBy), msg), nil
	}

	k.LogInfo("DevPubKey", types.Inferences, "DevPubKey", dev.WorkerPublicKey, "DevAddress", dev.Address)
	k.LogInfo("TransferAgentPubKey", types.Inferences, "TransferAgentPubKey", transferAgent.WorkerPublicKey, "TransferAgentAddress", transferAgent.Address)

	err := k.verifyKeys(ctx, msg, transferAgent, dev)
	if err != nil {
		k.LogError("StartInference: verifyKeys failed", types.Inferences, "error", err)
		return failedStart(ctx, sdkerrors.Wrap(types.ErrInvalidSignature, err.Error()), msg), nil
	}

	existingInference, found := k.GetInference(ctx, msg.InferenceId)

	if found && existingInference.StartProcessed() {
		k.LogError("StartInference: inference already started", types.Inferences, "inferenceId", msg.InferenceId)
		return failedStart(ctx, sdkerrors.Wrap(types.ErrInferenceStartProcessed, "inference has already start processed"), msg), nil
	}

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
		return failedStart(ctx, err, msg), nil
	}

	finalInference, err := k.processInferencePayments(ctx, inference, payments)
	if err != nil {
		return failedStart(ctx, err, msg), nil
	}
	err = k.SetInference(ctx, *finalInference)
	if err != nil {
		return failedStart(ctx, err, msg), nil
	}
	k.addTimeout(ctx, inference)

	if inference.IsCompleted() {
		err := k.handleInferenceCompleted(ctx, inference)
		if err != nil {
			return failedStart(ctx, err, msg), nil
		}
	}

	return &types.MsgStartInferenceResponse{
		InferenceIndex: msg.InferenceId,
	}, nil
}

func failedStart(ctx sdk.Context, error error, message *types.MsgStartInference) *types.MsgStartInferenceResponse {
	ctx.EventManager().EmitEvent(
		sdk.NewEvent("start_inference",
			sdk.NewAttribute("result", "failed")))
	return &types.MsgStartInferenceResponse{
		InferenceIndex: message.InferenceId,
		ErrorMessage:   error.Error(),
	}
}

func (k msgServer) verifyKeys(ctx sdk.Context, msg *types.MsgStartInference, agent types.Participant, dev types.Participant) error {
	devComponents := getDevSignatureComponents(msg)

	if err := k.validateTimestamp(ctx, devComponents, msg.InferenceId, 60); err != nil {
		return err
	}

	// Verify dev signature (original_prompt_hash)
	if err := calculations.VerifyKeys(ctx, devComponents, calculations.SignatureData{
		DevSignature: msg.InferenceId, Dev: &dev,
	}, k); err != nil {
		k.LogError("StartInference: dev signature failed", types.Inferences, "error", err)
		return err
	}

	// Verify TA signature (prompt_hash)
	if err := calculations.VerifyKeys(ctx, getTASignatureComponents(msg), calculations.SignatureData{
		TransferSignature: msg.TransferSignature, TransferAgent: &agent,
	}, k); err != nil {
		k.LogError("StartInference: TA signature failed", types.Inferences, "error", err)
		return err
	}

	return nil
}

func (k msgServer) validateTimestamp(
	ctx sdk.Context,
	components calculations.SignatureComponents,
	inferenceId string,
	extraSeconds int64,
) error {
	params, err := k.GetParamsSafe(ctx)
	if err != nil {
		return err
	}
	k.LogInfo("Validating timestamp for StartInference:", types.Inferences,
		"timestamp", components.Timestamp,
		"inferenceId", inferenceId,
		"currentBlockTime", ctx.BlockTime().UnixNano(),
		"timestampExpiration", params.ValidationParams.TimestampExpiration,
		"timestampAdvance", params.ValidationParams.TimestampAdvance,
	)
	err = calculations.ValidateTimestamp(
		components.Timestamp,
		ctx.BlockTime().UnixNano(),
		params.ValidationParams.TimestampExpiration,
		params.ValidationParams.TimestampAdvance,
		// signature dedupe (via inferenceID) will prevent most replay, this is for
		// replay attacks of pruned inferences only
		extraSeconds*int64(time.Second),
	)
	if err != nil {
		k.LogError("StartInference: validateTimestamp failed", types.Inferences, "error", err)
		return err
	}
	return err
}

func (k msgServer) addTimeout(ctx sdk.Context, inference *types.Inference) {
	expirationBlocks := k.GetParams(ctx).ValidationParams.ExpirationBlocks
	expirationHeight := uint64(inference.StartBlockHeight + expirationBlocks)
	err := k.SetInferenceTimeout(ctx, types.InferenceTimeout{
		ExpirationHeight: expirationHeight,
		InferenceId:      inference.InferenceId,
	})

	if err != nil {
		// Not fatal, we try to continue
		k.LogError("Unable to set inference timeout", types.Inferences, err)
	}

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
		err := k.IssueRefund(ctx, -payments.EscrowAmount, inference.RequestedBy, "inference_refund:"+inference.InferenceId)
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
		k.SafeLogSubAccountTransaction(ctx, executor.Address, types.ModuleName, types.OwedSubAccount, executor.CoinBalance, "inference_started:"+inference.InferenceId)
		err := k.SetParticipant(ctx, executor)
		if err != nil {
			return nil, err
		}
	}
	return inference, nil

}

// getDevSignatureComponents returns components for dev signature verification
// Dev signs: original_prompt_hash + timestamp + ta_address (no executor)
func getDevSignatureComponents(msg *types.MsgStartInference) calculations.SignatureComponents {
	return calculations.SignatureComponents{
		Payload:         msg.OriginalPromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.Creator,
		ExecutorAddress: "", // Dev doesn't include executor address
	}
}

// getTASignatureComponents returns components for TA signature verification
// TA signs: prompt_hash + timestamp + ta_address + executor_address
func getTASignatureComponents(msg *types.MsgStartInference) calculations.SignatureComponents {
	return calculations.SignatureComponents{
		Payload:         msg.PromptHash,
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
	// Not all accounts are guaranteed to have a pubkey
	if acc.GetPubKey() == nil {
		k.LogError("getAccountPubKey: Account has no pubkey", types.Participants, "address", address)
		return "", types.ErrPubKeyUnavailable
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
