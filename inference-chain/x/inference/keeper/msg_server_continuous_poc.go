package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// SubmitContinuousPoCCommit handles submission of continuous PoC commits.
// Participants call this periodically to prove that spare GPU capacity is generating
// PoC nonces in parallel with inference work.
func (k msgServer) SubmitContinuousPoCCommit(goCtx context.Context, msg *types.MsgSubmitContinuousPoCCommit) (*types.MsgSubmitContinuousPoCCommitResponse, error) {
	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	// Check that continuous PoC is enabled
	cpocParams := params.ContinuousPocParams
	if cpocParams == nil || !cpocParams.EnableContinuousPoC {
		return nil, sdkerrors.Wrap(types.ErrContinuousPoCDisabled, "continuous PoC is not enabled in params")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()

	// Validate creator address
	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid creator address: %v", err))
	}

	// Validate creator is a registered participant
	_, err = k.Keeper.Participants.Get(ctx, addr)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, fmt.Sprintf("creator %s is not a registered participant", msg.Creator))
	}

	// Validate nonce count meets minimum
	if msg.NonceCount < cpocParams.MinNoncesPerCommit {
		return nil, sdkerrors.Wrap(types.ErrContinuousPoCMinNoncesNotMet,
			fmt.Sprintf("nonce_count %d below minimum %d", msg.NonceCount, cpocParams.MinNoncesPerCommit))
	}

	// Validate root hash is 32 bytes
	if len(msg.RootHash) != 32 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("root_hash must be 32 bytes, got %d", len(msg.RootHash)))
	}

	// Validate GPU utilization is within basis points range
	if msg.GpuUtilizationBps > 10000 {
		return nil, sdkerrors.Wrap(types.ErrContinuousPoCInvalidUtilization,
			fmt.Sprintf("gpu_utilization_bps %d exceeds maximum 10000", msg.GpuUtilizationBps))
	}

	// Get or create epoch summary to check commit count limit
	summaryKey := collections.Join(msg.EpochIndex, addr)
	summary, err := k.ContinuousPoCEpochSummaries.Get(ctx, summaryKey)
	if err != nil {
		// First commit for this epoch - initialize summary
		summary = types.ContinuousPoCEpochSummary{
			ParticipantAddress: msg.Creator,
			EpochIndex:         msg.EpochIndex,
			TotalNonces:        0,
			TotalInferences:    0,
			CommitCount:        0,
			LastCommitHeight:   0,
			EffectivePocWeight: 0,
		}
	}

	// Check max commits per epoch
	if summary.CommitCount >= cpocParams.MaxCommitsPerEpoch {
		return nil, sdkerrors.Wrap(types.ErrContinuousPoCMaxCommitsExceeded,
			fmt.Sprintf("participant has already submitted %d commits this epoch (max: %d)",
				summary.CommitCount, cpocParams.MaxCommitsPerEpoch))
	}

	// Rate limit: one commit per block
	if summary.LastCommitHeight == currentBlockHeight {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "only one continuous PoC commit per block allowed")
	}

	// Store the individual commit record
	commit := types.ContinuousPoCCommit{
		ParticipantAddress: msg.Creator,
		EpochIndex:         msg.EpochIndex,
		NonceCount:         msg.NonceCount,
		RootHash:           msg.RootHash,
		InferenceCount:     msg.InferenceCount,
		CommitBlockHeight:  currentBlockHeight,
		GpuUtilizationBps:  msg.GpuUtilizationBps,
	}

	commitKey := collections.Join3(msg.EpochIndex, addr, currentBlockHeight)
	if err := k.ContinuousPoCCommits.Set(ctx, commitKey, commit); err != nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store continuous PoC commit: %v", err))
	}

	// Update the epoch summary
	summary.TotalNonces += uint64(msg.NonceCount)
	summary.TotalInferences += uint64(msg.InferenceCount)
	summary.CommitCount++
	summary.LastCommitHeight = currentBlockHeight

	// Calculate effective PoC weight: total nonces / nonce_weight
	if cpocParams.NonceWeight > 0 {
		summary.EffectivePocWeight = int64(summary.TotalNonces / uint64(cpocParams.NonceWeight))
	}

	if err := k.ContinuousPoCEpochSummaries.Set(ctx, summaryKey, summary); err != nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to update epoch summary: %v", err))
	}

	k.LogInfo("[ContinuousPoC] Commit recorded", types.PoC,
		"participant", msg.Creator,
		"epoch", msg.EpochIndex,
		"nonceCount", msg.NonceCount,
		"inferenceCount", msg.InferenceCount,
		"gpuUtilBps", msg.GpuUtilizationBps,
		"totalNonces", summary.TotalNonces,
		"commitCount", summary.CommitCount,
		"effectiveWeight", summary.EffectivePocWeight)

	return &types.MsgSubmitContinuousPoCCommitResponse{}, nil
}
