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
	if err := k.CheckPermission(goCtx, msg, ActiveParticipantPermission); err != nil {
		// return the failure and back out even batch transactions, since permissions will not change in a batch
		return nil, err
	}

	ctx, err := k.Keeper.InjectParamsIntoContext(sdk.UnwrapSDKContext(goCtx))
	if err != nil {
		k.LogWarn("StartInference: failed to inject params", types.Inferences, "error", err)
	}

	k.LogInfo("StartInference", types.Inferences, "inferenceId", msg.InferenceId, "creator", msg.Creator, "requestedBy", msg.RequestedBy, "model", msg.Model)

	// Developer access gating: before the cutoff height, only allowlisted developers may request inferences.
	if k.IsDeveloperAccessRestricted(ctx, ctx.BlockHeight()) && !k.IsAllowedDeveloper(ctx, msg.RequestedBy) {
		return failedStart(ctx, sdkerrors.Wrap(types.ErrDeveloperNotAllowlisted, msg.RequestedBy), msg), nil
	}

	// Transfer Agent access gating: only allowlisted TAs may submit StartInference.
	if k.IsTransferAgentRestricted(ctx) && !k.IsAllowedTransferAgent(ctx, msg.Creator) {
		k.LogError("StartInference: transfer agent is not allowlisted", types.Inferences,
			"transferAgent", msg.Creator, "blockHeight", ctx.BlockHeight())
		return failedStart(ctx, sdkerrors.Wrap(types.ErrTransferAgentNotAllowlisted, msg.Creator), msg), nil
	}

	if msg.MaxTokens > types.MaxAllowedTokens {
		return failedStart(ctx, sdkerrors.Wrapf(types.ErrTokenCountOutOfRange, "max_tokens exceeds limit (%d > %d)", msg.MaxTokens, types.MaxAllowedTokens), msg), nil
	}
	if msg.PromptTokenCount > types.MaxAllowedTokens {
		return failedStart(ctx, sdkerrors.Wrapf(types.ErrTokenCountOutOfRange, "prompt_token_count exceeds limit (%d > %d)", msg.PromptTokenCount, types.MaxAllowedTokens), msg), nil
	}

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

	existingInference, found := k.GetInference(ctx, msg.InferenceId)

	if found && existingInference.StartProcessed() {
		k.LogError("StartInference: inference already started", types.Inferences, "inferenceId", msg.InferenceId)
		return failedStart(ctx, sdkerrors.Wrap(types.ErrInferenceStartProcessed, "inference has already start processed"), msg), nil
	}

	// Signature verification policy:
	// - Start first: verify dev signature once; skip TA signature.
	// - Finish first: start performs equality checks only (no TA/dev re-verification).
	// - Executor signature verification is disabled by policy in both paths.
	if existingInference.FinishedProcessed() {
		if err := k.compareDevComponents(msg, &existingInference); err != nil {
			k.LogError("StartInference: dev component mismatch", types.Inferences, "error", err, "inferenceId", msg.InferenceId)
			return failedStart(ctx, err, msg), nil
		}
		if err := k.compareStartTAComponents(msg, &existingInference); err != nil {
			k.LogError("StartInference: TA component mismatch", types.Inferences, "error", err, "inferenceId", msg.InferenceId)
			return failedStart(ctx, err, msg), nil
		}
		if err := k.compareStartModelField(msg, &existingInference); err != nil {
			k.LogError("StartInference: model field mismatch", types.Inferences, "error", err, "inferenceId", msg.InferenceId)
			return failedStart(ctx, err, msg), nil
		}
		k.LogDebug("StartInference: cryptographic signature verification skipped; dev and TA components compared for consistency", types.Inferences, "inferenceId", msg.InferenceId)
	} else {
		err := k.verifyStartFirstMessageKeys(ctx, msg, &dev)
		if err != nil {
			k.LogError("StartInference: verifyStartFirstMessageKeys failed", types.Inferences, "error", err)
			return failedStart(ctx, sdkerrors.Wrap(types.ErrInvalidSignature, err.Error()), msg), nil
		}
		k.LogDebug("StartInference: dev signature cryptographically verified; TA signature deferred to FinishInference", types.Inferences, "inferenceId", msg.InferenceId)
	}
	k.LogDebug("StartInference: executor signature verification disabled by policy", types.Inferences, "inferenceId", msg.InferenceId)

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

	var executor *types.Participant
	if inference.ExecutedBy != "" {
		executorValue, found := k.GetParticipant(ctx, inference.ExecutedBy)
		if !found {
			k.LogError("StartInference: executor not found", types.Inferences, "executed_by", inference.ExecutedBy, "inference_id", inference.InferenceId)
			return failedStart(ctx, sdkerrors.Wrap(types.ErrParticipantNotFound, inference.ExecutedBy), msg), nil
		}
		executor = &executorValue
	}

	finalInference, err := k.processInferencePayments(ctx, inference, payments, false, executor)
	if err != nil {
		return failedStart(ctx, err, msg), nil
	}

	if finalInference.IsCompleted() {
		k.handleInferenceCompleted(ctx, finalInference, executor)
	}
	if shouldPersistParticipant(finalInference, payments, executor) {
		if err := k.SetParticipant(ctx, *executor); err != nil {
			return failedStart(ctx, err, msg), nil
		}
	}
	err = k.SetInference(ctx, *finalInference)
	if err != nil {
		return failedStart(ctx, err, msg), nil
	}
	k.addTimeout(ctx, finalInference)

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

func (k msgServer) verifyStartFirstMessageKeys(ctx sdk.Context, msg *types.MsgStartInference, dev *types.Participant) error {
	devComponents := getDevSignatureComponents(msg)

	if err := k.validateTimestamp(ctx, devComponents, msg.InferenceId, 60); err != nil {
		return err
	}

	// Verify dev signature (original_prompt_hash)
	if err := calculations.VerifyKeys(ctx, devComponents, calculations.SignatureData{
		DevSignature: msg.InferenceId, Dev: dev,
	}, k); err != nil {
		k.LogError("StartInference: dev signature failed", types.Inferences, "error", err)
		return err
	}

	return nil
}

type HasDevComponents interface {
	GetOriginalPromptHash() string
	GetRequestTimestamp() int64
	GetRequestedBy() string
	GetTransferredBy() string
}

func (k msgServer) compareDevComponents(msg HasDevComponents, inference *types.Inference) error {
	if inference.OriginalPromptHash != msg.GetOriginalPromptHash() {
		return sdkerrors.Wrapf(
			types.ErrDevComponentMismatch,
			"original_prompt_hash mismatch: message=%s inference=%s",
			msg.GetOriginalPromptHash(),
			inference.OriginalPromptHash,
		)
	}
	if inference.RequestTimestamp != msg.GetRequestTimestamp() {
		return sdkerrors.Wrapf(
			types.ErrDevComponentMismatch,
			"request_timestamp mismatch: message=%d inference=%d",
			msg.GetRequestTimestamp(),
			inference.RequestTimestamp,
		)
	}
	if inference.TransferredBy != msg.GetTransferredBy() {
		return sdkerrors.Wrapf(
			types.ErrDevComponentMismatch,
			"transfer agent mismatch: message=%s inference=%s",
			msg.GetTransferredBy(),
			inference.TransferredBy,
		)
	}
	if inference.RequestedBy != msg.GetRequestedBy() {
		return sdkerrors.Wrapf(
			types.ErrDevComponentMismatch,
			"requested_by mismatch: message=%s inference=%s",
			msg.GetRequestedBy(),
			inference.RequestedBy,
		)
	}
	return nil
}

// TODO: We need to include inferenceId in the TA signature to make sure executor can't substitute the modified prompt
// TODO: any error here should lead to punishing the TA
func (k msgServer) compareStartTAComponents(msg *types.MsgStartInference, inference *types.Inference) error {
	if inference.PromptHash != msg.PromptHash {
		return sdkerrors.Wrapf(
			types.ErrTAComponentMismatch,
			"prompt_hash mismatch: start=%s finish=%s",
			msg.PromptHash,
			inference.PromptHash,
		)
	}
	if inference.RequestTimestamp != msg.RequestTimestamp {
		return sdkerrors.Wrapf(
			types.ErrTAComponentMismatch,
			"request_timestamp mismatch: start=%d finish=%d",
			msg.RequestTimestamp,
			inference.RequestTimestamp,
		)
	}
	if inference.TransferredBy != msg.Creator {
		return sdkerrors.Wrapf(
			types.ErrTAComponentMismatch,
			"transfer agent mismatch: start=%s finish=%s",
			msg.Creator,
			inference.TransferredBy,
		)
	}
	if inference.ExecutedBy != msg.AssignedTo {
		return sdkerrors.Wrapf(
			types.ErrTAComponentMismatch,
			"executor mismatch: start.assigned_to=%s finish.executed_by=%s",
			msg.AssignedTo,
			inference.ExecutedBy,
		)
	}
	return nil
}

func (k msgServer) compareStartModelField(msg *types.MsgStartInference, inference *types.Inference) error {
	if inference.Model != "" && inference.Model != msg.Model {
		return sdkerrors.Wrapf(
			types.ErrInferenceRoleMismatch,
			"model mismatch: start=%s finish=%s",
			msg.Model,
			inference.Model,
		)
	}
	return nil
}

func (k msgServer) validateTimestamp(
	ctx sdk.Context,
	components calculations.SignatureComponents,
	inferenceId string,
	extraSeconds int64,
) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("StartInference: validateTimestamp failed to get params", types.Inferences, "error", err)
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
	return nil
}

func (k msgServer) addTimeout(ctx sdk.Context, inference *types.Inference) {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Unable to get params for inference timeout", types.Inferences, "error", err)
		return
	}
	expirationBlocks := params.ValidationParams.ExpirationBlocks
	expirationHeight := uint64(inference.StartBlockHeight + expirationBlocks)
	err = k.SetInferenceTimeout(ctx, types.InferenceTimeout{
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
	allowRefund bool,
	executor *types.Participant,
) (*types.Inference, error) {
	if payments.EscrowAmount > 0 {
		escrowAmount, err := k.PutPaymentInEscrow(ctx, inference, payments.EscrowAmount)
		if err != nil {
			return nil, err
		}
		inference.EscrowAmount = escrowAmount
	}
	if payments.EscrowAmount < 0 {
		if !allowRefund {
			return nil, sdkerrors.Wrapf(types.ErrInvalidEscrowAmount, "escrow amount cannot be negative here: %d", payments.EscrowAmount)
		}
		err := k.IssueRefund(ctx, -payments.EscrowAmount, inference.RequestedBy, "inference_refund:"+inference.InferenceId)
		if err != nil {
			k.LogError("Unable to Issue Refund for started inference", types.Payments, err)
		}
	}
	if payments.ExecutorPayment > 0 {
		if executor == nil {
			return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, inference.ExecutedBy)
		}
		ensureParticipantEpochStats(executor)
		executor.CoinBalance += payments.ExecutorPayment
		executor.CurrentEpochStats.EarnedCoins += uint64(payments.ExecutorPayment)
		k.SafeLogSubAccountTransaction(ctx, executor.Address, types.ModuleName, types.OwedSubAccount, executor.CoinBalance, "inference_started:"+inference.InferenceId)
	}
	return inference, nil
}

func shouldPersistParticipant(inference *types.Inference, payments *calculations.Payments, executor *types.Participant) bool {
	if inference == nil || payments == nil || executor == nil {
		return false
	}
	return inference.IsCompleted() || payments.ExecutorPayment > 0
}

func ensureParticipantEpochStats(participant *types.Participant) {
	if participant == nil {
		return
	}
	if participant.CurrentEpochStats == nil {
		participant.CurrentEpochStats = &types.CurrentEpochStats{}
	}
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
