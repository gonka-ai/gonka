package keeper

import (
	"context"
	"encoding/binary"
	"fmt"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// ── SubmitContinuousPoCCommit ─────────────────────────────────────────────────

// SubmitContinuousPoCCommit handles submission of continuous PoC commits.
// Participants call this periodically to prove that spare GPU capacity is generating
// PoC nonces in parallel with inference work.
func (k msgServer) SubmitContinuousPoCCommit(goCtx context.Context, msg *types.MsgSubmitContinuousPoCCommit) (*types.MsgSubmitContinuousPoCCommitResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ActiveParticipantPermission); err != nil {
		return nil, err
	}

	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	cpocParams := params.ContinuousPocParams
	if cpocParams == nil || !cpocParams.EnableContinuousPoC {
		return nil, sdkerrors.Wrap(types.ErrContinuousPoCDisabled, "continuous PoC is not enabled in params")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()

	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid creator address: %v", err))
	}

	_, err = k.Keeper.Participants.Get(ctx, addr)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, fmt.Sprintf("creator %s is not a registered participant", msg.Creator))
	}

	if msg.NonceCount < cpocParams.MinNoncesPerCommit {
		return nil, sdkerrors.Wrap(types.ErrContinuousPoCMinNoncesNotMet,
			fmt.Sprintf("nonce_count %d below minimum %d", msg.NonceCount, cpocParams.MinNoncesPerCommit))
	}

	if len(msg.RootHash) != 32 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState,
			fmt.Sprintf("root_hash must be 32 bytes, got %d", len(msg.RootHash)))
	}

	if msg.GpuUtilizationBps > 10000 {
		return nil, sdkerrors.Wrap(types.ErrContinuousPoCInvalidUtilization,
			fmt.Sprintf("gpu_utilization_bps %d exceeds maximum 10000", msg.GpuUtilizationBps))
	}

	summaryKey := collections.Join(msg.EpochIndex, addr)
	summary, err := k.ContinuousPoCEpochSummaries.Get(ctx, summaryKey)
	if err != nil {
		summary = types.ContinuousPoCEpochSummary{
			ParticipantAddress: msg.Creator,
			EpochIndex:         msg.EpochIndex,
		}
	}

	if summary.CommitCount >= cpocParams.MaxCommitsPerEpoch {
		return nil, sdkerrors.Wrap(types.ErrContinuousPoCMaxCommitsExceeded,
			fmt.Sprintf("participant has already submitted %d commits this epoch (max: %d)",
				summary.CommitCount, cpocParams.MaxCommitsPerEpoch))
	}

	if summary.LastCommitHeight == currentBlockHeight {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "only one continuous PoC commit per block allowed")
	}

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
		return nil, sdkerrors.Wrap(types.ErrIllegalState,
			fmt.Sprintf("failed to store continuous PoC commit: %v", err))
	}

	summary.TotalNonces += uint64(msg.NonceCount)
	summary.TotalInferences += uint64(msg.InferenceCount)
	summary.CommitCount++
	summary.LastCommitHeight = currentBlockHeight
	if cpocParams.NonceWeight > 0 {
		summary.EffectivePocWeight = int64(summary.TotalNonces / uint64(cpocParams.NonceWeight))
	}

	if err := k.ContinuousPoCEpochSummaries.Set(ctx, summaryKey, summary); err != nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState,
			fmt.Sprintf("failed to update epoch summary: %v", err))
	}

	k.LogInfo("[ContinuousPoC] Commit recorded", types.PoC,
		"participant", msg.Creator,
		"epoch", msg.EpochIndex,
		"nonceCount", msg.NonceCount,
		"totalNonces", summary.TotalNonces,
		"effectiveWeight", summary.EffectivePocWeight)

	return &types.MsgSubmitContinuousPoCCommitResponse{}, nil
}

// ── RespondContinuousPoCChallenge ────────────────────────────────────────────

// RespondContinuousPoCChallenge verifies a Merkle proof for a pending challenge.
// The participant reveals the nonce preimage at the challenged index together
// with the Merkle path; the chain verifies against the stored root_hash.
// A valid response resolves the challenge. An expired challenge cannot be answered.
func (k msgServer) RespondContinuousPoCChallenge(goCtx context.Context, msg *types.MsgRespondContinuousPoCChallenge) (*types.MsgRespondContinuousPoCChallengeResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ActiveParticipantPermission, PreviousActiveParticipantPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	blockHeight := ctx.BlockHeight()

	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid creator address: %v", err))
	}

	challengeKey := collections.Join3(msg.EpochIndex, addr, msg.ChallengeBlockHeight)
	challenge, err := k.ContinuousPoCChallenges.Get(ctx, challengeKey)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrContinuousPoCChallengeNotFound,
			fmt.Sprintf("challenge for epoch %d at block %d not found", msg.EpochIndex, msg.ChallengeBlockHeight))
	}

	if challenge.Resolved {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "challenge is already resolved")
	}

	if blockHeight > challenge.DeadlineBlockHeight {
		return nil, sdkerrors.Wrap(types.ErrContinuousPoCChallengeExpired,
			fmt.Sprintf("challenge expired at block %d, current block %d",
				challenge.DeadlineBlockHeight, blockHeight))
	}

	// Retrieve the specific commit to get the stored root hash and nonce count.
	commitKey := collections.Join3(msg.EpochIndex, addr, challenge.CommitBlockHeight)
	commit, err := k.ContinuousPoCCommits.Get(ctx, commitKey)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState,
			fmt.Sprintf("commit at block %d for epoch %d not found", challenge.CommitBlockHeight, msg.EpochIndex))
	}

	// Validate proof array lengths are consistent.
	if len(msg.ProofSiblings) != len(msg.ProofDirs) {
		return nil, sdkerrors.Wrap(types.ErrContinuousPoCInvalidMerkleProof,
			"proof_siblings and proof_dirs must have equal length")
	}

	// Verify the Merkle proof: sha256(leaf_value) must be the leaf at challenge.NonceIndex.
	if !types.VerifyMerkleProof(msg.LeafValue, challenge.NonceIndex, msg.ProofSiblings, msg.ProofDirs, commit.RootHash) {
		// Mark summary as penalized: forfeit effective weight for this epoch.
		summaryKey := collections.Join(msg.EpochIndex, addr)
		if summary, err := k.ContinuousPoCEpochSummaries.Get(ctx, summaryKey); err == nil {
			summary.PenaltyApplied = true
			summary.EffectivePocWeight = 0
			_ = k.ContinuousPoCEpochSummaries.Set(ctx, summaryKey, summary)
		}
		k.LogWarn("[ContinuousPoC] Invalid Merkle proof — epoch weight zeroed", types.PoC,
			"participant", msg.Creator,
			"epoch", msg.EpochIndex,
			"nonceIndex", challenge.NonceIndex)
		return nil, sdkerrors.Wrap(types.ErrContinuousPoCInvalidMerkleProof, "Merkle proof verification failed")
	}

	// Mark challenge resolved.
	challenge.Resolved = true
	if err := k.ContinuousPoCChallenges.Set(ctx, challengeKey, challenge); err != nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "failed to update challenge state")
	}

	k.LogInfo("[ContinuousPoC] Challenge resolved", types.PoC,
		"participant", msg.Creator,
		"epoch", msg.EpochIndex,
		"nonceIndex", challenge.NonceIndex)

	return &types.MsgRespondContinuousPoCChallengeResponse{}, nil
}

// ── IssueContinuousPoCChallenges ──────────────────────────────────────────────

// IssueContinuousPoCChallenges is called from EndBlock to randomly sample
// and challenge continuous PoC commits based on the ValidationSampleRateBps param.
// Uses the block's app_hash as a deterministic entropy source.
func (k Keeper) IssueContinuousPoCChallenges(ctx context.Context, params types.Params, blockHeight int64, appHashHex string) {
	cpocParams := params.ContinuousPocParams
	if cpocParams == nil || !cpocParams.EnableContinuousPoC {
		return
	}
	if cpocParams.ValidationSampleRateBps == 0 {
		return
	}

	// Iterate recent commits from the current epoch and sample them.
	currentEpoch, found := k.GetEffectiveEpoch(ctx)
	if !found || currentEpoch == nil {
		return
	}

	entropy := deriveEntropy(appHashHex, blockHeight)

	iter, err := k.ContinuousPoCCommits.Iterate(ctx,
		collections.NewPrefixedTripleRange[uint64, sdk.AccAddress, int64](currentEpoch.Index))
	if err != nil {
		k.LogError("[ContinuousPoC] Failed to iterate commits for challenge issuance", types.PoC, "error", err)
		return
	}
	defer iter.Close()

	sampleCounter := uint64(0)
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			continue
		}
		epochIdx := key.K1()
		participantAddr := key.K2()
		commitHeight := key.K3()

		commit, err := iter.Value()
		if err != nil || commit.NonceCount == 0 {
			continue
		}

		// Determine whether to challenge this commit using sample rate.
		sampleSeed := entropy ^ sampleCounter
		sampleCounter++
		if sampleSeed%10000 >= uint64(cpocParams.ValidationSampleRateBps) {
			continue
		}

		// Pick a random nonce index within the commit.
		nonceIndex := uint32(sampleSeed % uint64(commit.NonceCount))

		challengeKey := collections.Join3(epochIdx, participantAddr, blockHeight)
		// Avoid issuing a duplicate challenge for the same commit in the same block.
		if exists, _ := k.ContinuousPoCChallenges.Has(ctx, challengeKey); exists {
			continue
		}

		challenge := types.ContinuousPoCChallenge{
			ChallengedAddress:    participantAddr.String(),
			EpochIndex:           epochIdx,
			CommitBlockHeight:    commitHeight,
			NonceIndex:           nonceIndex,
			ChallengeBlockHeight: blockHeight,
			DeadlineBlockHeight:  blockHeight + cpocParams.ValidationChallengeDeadlineBlocks,
		}
		if err := k.ContinuousPoCChallenges.Set(ctx, challengeKey, challenge); err != nil {
			k.LogError("[ContinuousPoC] Failed to store challenge", types.PoC, "error", err)
			continue
		}

		k.LogInfo("[ContinuousPoC] Challenge issued", types.PoC,
			"participant", participantAddr.String(),
			"epoch", epochIdx,
			"commitHeight", commitHeight,
			"nonceIndex", nonceIndex,
			"deadline", challenge.DeadlineBlockHeight)
	}
}

// ExpireContinuousPoCChallenges checks all pending challenges and penalises
// participants who failed to respond within the deadline.
func (k Keeper) ExpireContinuousPoCChallenges(ctx context.Context, blockHeight int64) {
	currentEpoch, found := k.GetEffectiveEpoch(ctx)
	if !found || currentEpoch == nil {
		return
	}

	iter, err := k.ContinuousPoCChallenges.Iterate(ctx,
		collections.NewPrefixedTripleRange[uint64, sdk.AccAddress, int64](currentEpoch.Index))
	if err != nil {
		k.LogError("[ContinuousPoC] Failed to iterate challenges for expiry", types.PoC, "error", err)
		return
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		challenge, err := iter.Value()
		if err != nil || challenge.Resolved {
			continue
		}
		if blockHeight <= challenge.DeadlineBlockHeight {
			continue
		}

		// Challenge expired without a valid response — penalise the participant.
		addr, err := sdk.AccAddressFromBech32(challenge.ChallengedAddress)
		if err != nil {
			continue
		}
		summaryKey := collections.Join(challenge.EpochIndex, addr)
		if summary, err := k.ContinuousPoCEpochSummaries.Get(ctx, summaryKey); err == nil {
			if !summary.PenaltyApplied {
				summary.PenaltyApplied = true
				summary.EffectivePocWeight = 0
				_ = k.ContinuousPoCEpochSummaries.Set(ctx, summaryKey, summary)
				k.LogWarn("[ContinuousPoC] Challenge expired — epoch weight zeroed", types.PoC,
					"participant", challenge.ChallengedAddress,
					"epoch", challenge.EpochIndex,
					"nonceIndex", challenge.NonceIndex)
			}
		}
	}
}

// deriveEntropy produces a uint64 deterministic entropy value from an app_hash hex string and block height.
func deriveEntropy(appHashHex string, blockHeight int64) uint64 {
	if len(appHashHex) < 16 {
		return uint64(blockHeight)
	}
	// Use the first 8 bytes of the hex-decoded app hash XORed with block height.
	var b [8]byte
	for i := 0; i < 8 && 2*i+1 < len(appHashHex); i++ {
		hi := hexNibble(appHashHex[2*i])
		lo := hexNibble(appHashHex[2*i+1])
		b[i] = hi<<4 | lo
	}
	return binary.BigEndian.Uint64(b[:]) ^ uint64(blockHeight)
}

func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}
