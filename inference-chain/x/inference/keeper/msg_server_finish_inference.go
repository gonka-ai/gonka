package keeper

import (
	"context"
	"strconv"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) FinishInference(goCtx context.Context, msg *types.MsgFinishInference) (*types.MsgFinishInferenceResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ActiveParticipantPermission, PreviousActiveParticipantPermission); err != nil {
		// do not return failedFinish here. The entire transaction should fail here since permissions will all
		// have the same result
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	// Inject cache once at the entry point
	ctx, err := k.Keeper.InjectParamsIntoContext(sdk.UnwrapSDKContext(goCtx))
	if err != nil {
		k.LogWarn("FinishInference: failed to inject params", types.Inferences, "error", err)
	}

	k.LogInfo("FinishInference", types.Inferences, "inference_id", msg.InferenceId, "executed_by", msg.ExecutedBy, "created_by", msg.Creator)
	if msg.Creator != msg.ExecutedBy {
		err := sdkerrors.Wrapf(types.ErrInferenceRoleMismatch, "creator (%s) must equal executed_by (%s)", msg.Creator, msg.ExecutedBy)
		k.LogError("FinishInference: creator-role invariant failed", types.Inferences, "error", err)
		return failedFinish(ctx, err, msg), nil
	}

	if msg.PromptTokenCount > types.MaxAllowedTokens {
		return failedFinish(ctx, sdkerrors.Wrapf(types.ErrTokenCountOutOfRange, "prompt_token_count exceeds limit (%d > %d)", msg.PromptTokenCount, types.MaxAllowedTokens), msg), nil
	}
	if msg.CompletionTokenCount > types.MaxAllowedTokens {
		return failedFinish(ctx, sdkerrors.Wrapf(types.ErrTokenCountOutOfRange, "completion_token_count exceeds limit (%d > %d)", msg.CompletionTokenCount, types.MaxAllowedTokens), msg), nil
	}

	// Developer access gating: until cutoff height only allowlisted developers may run inference flows.
	// We gate by the original requester (developer), not the executor/TA.
	if k.IsDeveloperAccessRestricted(ctx, ctx.BlockHeight()) && !k.IsAllowedDeveloper(ctx, msg.RequestedBy) {
		k.LogError("FinishInference: developer is not allowlisted at this height", types.Inferences, "developer", msg.RequestedBy, "blockHeight", ctx.BlockHeight())
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrDeveloperNotAllowlisted, msg.RequestedBy), msg), nil
	}

	// Transfer Agent access gating: only allowlisted TAs may be involved in inferences.
	if k.IsTransferAgentRestricted(ctx) && !k.IsAllowedTransferAgent(ctx, msg.TransferredBy) {
		k.LogError("FinishInference: transfer agent is not allowlisted", types.Inferences,
			"transferAgent", msg.TransferredBy, "blockHeight", ctx.BlockHeight())
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrTransferAgentNotAllowlisted, msg.TransferredBy), msg), nil
	}

	executor, found := k.GetParticipant(ctx, msg.ExecutedBy)
	if !found {
		k.LogError("FinishInference: executor not found", types.Inferences, "executed_by", msg.ExecutedBy)
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.ExecutedBy), msg), nil
	}

	requestor, found := k.GetParticipant(ctx, msg.RequestedBy)
	if !found {
		k.LogError("FinishInference: requestor not found", types.Inferences, "requested_by", msg.RequestedBy)
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.RequestedBy), msg), nil
	}

	transferAgent, found := k.GetParticipant(ctx, msg.TransferredBy)
	if !found {
		k.LogError("FinishInference: transfer agent not found", types.Inferences, "transferred_by", msg.TransferredBy)
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.TransferredBy), msg), nil
	}

	existingInference, found := k.GetInference(ctx, msg.InferenceId)

	if found && existingInference.FinishedProcessed() {
		k.LogError("FinishInference: inference already finished", types.Inferences, "inferenceId", msg.InferenceId)
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrInferenceFinishProcessed, "inference has already finished processed"), msg), nil
	}

	if found && existingInference.Status == types.InferenceStatus_EXPIRED {
		k.LogWarn("FinishInference: cannot finish expired inference", types.Inferences,
			"inferenceId", msg.InferenceId,
			"currentStatus", existingInference.Status,
			"executedBy", msg.ExecutedBy)
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrInferenceExpired, "inference has already expired"), msg), nil
	}

	// Signature verification policy:
	// - Start first: finish performs equality checks only (no TA/dev re-verification).
	// - Finish first: verify dev + TA signatures.
	// - Executor signature verification is disabled by policy in both paths.
	if existingInference.StartProcessed() {
		if err := k.compareDevComponents(msg, &existingInference); err != nil {
			k.LogError("FinishInference: dev component mismatch", types.Inferences, "error", err, "inferenceId", msg.InferenceId)
			return failedFinish(ctx, err, msg), nil
		}
		if err := k.compareFinishTAComponents(msg, &existingInference); err != nil {
			k.LogError("FinishInference: TA component mismatch", types.Inferences, "error", err, "inferenceId", msg.InferenceId)
			return failedFinish(ctx, err, msg), nil
		}
		if err := k.compareFinishModelField(msg, &existingInference); err != nil {
			k.LogError("FinishInference: model field mismatch", types.Inferences, "error", err, "inferenceId", msg.InferenceId)
			return failedFinish(ctx, err, msg), nil
		}
		k.LogDebug("FinishInference: cryptographic signature verification skipped; dev and TA components compared for consistency", types.Inferences, "inferenceId", msg.InferenceId)
	} else {
		err := k.verifyFinishKeys(ctx, msg, &transferAgent, &requestor)
		if err != nil {
			k.LogError("FinishInference: verifyFinishKeys failed", types.Inferences, "error", err)
			return failedFinish(ctx, sdkerrors.Wrap(types.ErrInvalidSignature, err.Error()), msg), nil
		}
		k.LogDebug("FinishInference: dev and TA signatures cryptographically verified", types.Inferences, "inferenceId", msg.InferenceId)
	}
	k.LogDebug("FinishInference: executor signature verification disabled by policy", types.Inferences, "inferenceId", msg.InferenceId)

	// Record the current price only if this is the first message (StartInference not processed yet)
	// This ensures consistent pricing regardless of message arrival order
	if !existingInference.StartProcessed() {
		existingInference.Model = msg.Model
		k.RecordInferencePrice(goCtx, &existingInference, msg.InferenceId)
	} else if existingInference.Model == "" {
		k.LogError("FinishInference: model not set by the processed start message", types.Inferences,
			"inferenceId", msg.InferenceId,
			"executedBy", msg.ExecutedBy)
	} else if existingInference.Model != msg.Model {
		k.LogError("FinishInference: model mismatch", types.Inferences,
			"inferenceId", msg.InferenceId,
			"existingInference.Model", existingInference.Model,
			"msg.Model", msg.Model)
	}

	blockContext := calculations.BlockContext{
		BlockHeight:    ctx.BlockHeight(),
		BlockTimestamp: ctx.BlockTime().UnixMilli(),
	}

	inference, payments, err := calculations.ProcessFinishInference(&existingInference, msg, blockContext, k)
	if err != nil {
		return failedFinish(ctx, err, msg), nil
	}

	finalInference, err := k.processInferencePayments(ctx, inference, payments, true, &executor)
	if err != nil {
		return failedFinish(ctx, err, msg), nil
	}
	if finalInference.IsCompleted() {
		k.handleInferenceCompleted(ctx, finalInference, &executor)
	}
	if shouldPersistParticipant(finalInference, payments, &executor) {
		if err := k.SetParticipant(ctx, executor); err != nil {
			return failedFinish(ctx, err, msg), nil
		}
	}
	err = k.SetInference(ctx, *finalInference)
	if err != nil {
		return failedFinish(ctx, err, msg), nil
	}

	return &types.MsgFinishInferenceResponse{InferenceIndex: msg.InferenceId}, nil
}

func failedFinish(ctx sdk.Context, err error, msg *types.MsgFinishInference) *types.MsgFinishInferenceResponse {
	ctx.EventManager().EmitEvent(
		sdk.NewEvent("finish_inference",
			sdk.NewAttribute("result", "failed")))
	return &types.MsgFinishInferenceResponse{
		InferenceIndex: msg.InferenceId,
		ErrorMessage:   err.Error(),
	}
}

func (k msgServer) verifyFinishKeys(ctx sdk.Context, msg *types.MsgFinishInference, transferAgent *types.Participant, requestor *types.Participant) error {
	// Hash-based signature verification (post-upgrade flow)
	// Dev signs: original_prompt_hash + timestamp + ta_address
	// TA signs: prompt_hash + timestamp + ta_address + executor_address
	devComponents := getFinishDevSignatureComponents(msg)
	taComponents := getFinishTASignatureComponents(msg)

	// Extra seconds for long-running inferences; deduping via inferenceId is primary replay defense
	if err := k.validateTimestamp(ctx, devComponents, msg.InferenceId, 60*60); err != nil {
		return err
	}

	// Verify dev signature (original_prompt_hash)
	if err := calculations.VerifyKeys(ctx, devComponents, calculations.SignatureData{
		DevSignature: msg.InferenceId, Dev: requestor,
	}, k); err != nil {
		k.LogError("FinishInference: dev signature failed", types.Inferences, "error", err)
		return err
	}

	// Verify TA signature (prompt_hash)
	if err := k.verifyTASignature(ctx, msg, taComponents, transferAgent); err != nil {
		return err
	}

	return nil
}

// verifyTASignature verifies TA signature using prompt_hash.
// Includes upgrade-epoch fallback for inferences started before hash-based signing.
func (k msgServer) verifyTASignature(ctx sdk.Context, msg *types.MsgFinishInference, taComponents calculations.SignatureComponents, transferAgent *types.Participant) error {
	err := calculations.VerifyKeys(ctx, taComponents, calculations.SignatureData{
		TransferSignature: msg.TransferSignature, TransferAgent: transferAgent,
	}, k)
	if err == nil {
		return nil
	}

	// Upgrade-epoch fallback: inferences started before hash-based signing use original_prompt_hash
	// This path will be removed after upgrade epoch completes
	directComponents := calculations.SignatureComponents{
		Payload:         msg.OriginalPromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.TransferredBy,
		ExecutorAddress: msg.ExecutedBy,
	}
	if fallbackErr := calculations.VerifyKeys(ctx, directComponents, calculations.SignatureData{
		TransferSignature: msg.TransferSignature, TransferAgent: transferAgent,
	}, k); fallbackErr != nil {
		k.LogError("FinishInference: TA signature failed", types.Inferences, "promptHashErr", err, "fallbackErr", fallbackErr)
		return err
	}

	k.LogDebug("FinishInference: Using upgrade-epoch fallback for TA signature", types.Inferences, "inferenceId", msg.InferenceId)
	return nil
}

// getFinishDevSignatureComponents returns components for dev signature verification
// Dev signs: original_prompt_hash + timestamp + ta_address (no executor)
func getFinishDevSignatureComponents(msg *types.MsgFinishInference) calculations.SignatureComponents {
	return calculations.SignatureComponents{
		Payload:         msg.OriginalPromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.TransferredBy,
		ExecutorAddress: "", // Dev doesn't include executor address
	}
}

// getFinishTASignatureComponents returns components for TA/Executor signature verification
// TA/Executor sign: prompt_hash + timestamp + ta_address + executor_address
func getFinishTASignatureComponents(msg *types.MsgFinishInference) calculations.SignatureComponents {
	return calculations.SignatureComponents{
		Payload:         msg.PromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.TransferredBy,
		ExecutorAddress: msg.ExecutedBy,
	}
}

func (k msgServer) compareFinishTAComponents(msg *types.MsgFinishInference, inference *types.Inference) error {
	if inference.PromptHash != msg.PromptHash {
		return sdkerrors.Wrapf(
			types.ErrTAComponentMismatch,
			"prompt_hash mismatch: finish=%s start=%s",
			msg.PromptHash,
			inference.PromptHash,
		)
	}
	if inference.RequestTimestamp != msg.RequestTimestamp {
		return sdkerrors.Wrapf(
			types.ErrTAComponentMismatch,
			"request_timestamp mismatch: finish=%d start=%d",
			msg.RequestTimestamp,
			inference.RequestTimestamp,
		)
	}
	if inference.TransferredBy != msg.TransferredBy {
		return sdkerrors.Wrapf(
			types.ErrTAComponentMismatch,
			"transfer agent mismatch: finish=%s start=%s",
			msg.TransferredBy,
			inference.TransferredBy,
		)
	}
	if inference.AssignedTo != msg.ExecutedBy {
		return sdkerrors.Wrapf(
			types.ErrTAComponentMismatch,
			"executor mismatch: finish.executed_by=%s start.assigned_to=%s",
			msg.ExecutedBy,
			inference.AssignedTo,
		)
	}
	return nil
}

func (k msgServer) compareFinishModelField(msg *types.MsgFinishInference, inference *types.Inference) error {
	// inference.Model CANNOT be "" here, Model is a required field for StartInference message
	if inference.Model != "" && inference.Model != msg.Model {
		return sdkerrors.Wrapf(
			types.ErrInferenceRoleMismatch,
			"model mismatch: finish=%s start=%s",
			msg.Model,
			inference.Model,
		)
	}
	return nil
}

func (k msgServer) handleInferenceCompleted(ctx sdk.Context, inference *types.Inference, executor *types.Participant) {
	if executor == nil {
		k.LogWarn("handleInferenceCompleted: executor not loaded, skipping participant updates", types.Inferences, "executed_by", inference.ExecutedBy)
	} else {
		ensureParticipantEpochStats(executor)
		executor.CurrentEpochStats.InferenceCount++
		executor.LastInferenceTime = inference.EndBlockTimestamp
	}

	effectiveEpoch, found := k.GetEffectiveEpoch(ctx)
	if !found {
		k.LogWarn("handleInferenceCompleted: effective epoch not found, defaulting epoch fields to zero", types.EpochGroup)
		inference.EpochPocStartBlockHeight = 0
		inference.EpochId = 0
	} else {
		inference.EpochPocStartBlockHeight = uint64(effectiveEpoch.PocStartBlockHeight)
		inference.EpochId = effectiveEpoch.Index
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"inference_finished",
		buildInferenceFinishedEventAttributes(inference)...,
	))

	if err := k.EnqueueFinishedInference(ctx, inference.InferenceId); err != nil {
		k.LogError("Unable to enqueue pending inference validation", types.Validation, "inference_id", inference.InferenceId, "block_height", ctx.BlockHeight(), "err", err)
		return
	}

	k.LogDebug("Queued inference for deferred validation details processing", types.Validation,
		"inference_id", inference.InferenceId,
		"epoch_id", inference.EpochId,
		"block_height", ctx.BlockHeight(),
	)
}

// buildInferenceFinishedEventAttributes emits only fields required for dev-stats off-chain migration.
func buildInferenceFinishedEventAttributes(inference *types.Inference) []sdk.Attribute {
	return []sdk.Attribute{
		sdk.NewAttribute("inference_id", inference.InferenceId),
		sdk.NewAttribute("requested_by", inference.RequestedBy),
		sdk.NewAttribute("model", inference.Model),
		sdk.NewAttribute("status", inference.Status.String()),
		sdk.NewAttribute("epoch_id", strconv.FormatUint(inference.EpochId, 10)),
		sdk.NewAttribute("prompt_token_count", strconv.FormatUint(inference.PromptTokenCount, 10)),
		sdk.NewAttribute("completion_token_count", strconv.FormatUint(inference.CompletionTokenCount, 10)),
		sdk.NewAttribute("actual_cost_in_coins", strconv.FormatInt(inference.ActualCost, 10)),
		sdk.NewAttribute("start_block_timestamp", strconv.FormatInt(inference.StartBlockTimestamp, 10)),
		sdk.NewAttribute("end_block_timestamp", strconv.FormatInt(inference.EndBlockTimestamp, 10)),
	}
}
