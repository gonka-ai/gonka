package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SettleTeeAttestations(goCtx context.Context, stage int64) error {
	ctx := sdk.UnwrapSDKContext(goCtx)
	height := ctx.BlockHeight()

	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("SettleTeeAttestations: failed to load params", types.PoC, "stage", stage, "err", err)
		return err
	}
	teeParams := params.TeeAttestationParams
	if teeParams == nil {
		return nil
	}

	snapshot, found, err := k.GetPoCValidationSnapshot(ctx, stage)
	if err != nil {
		k.LogError("SettleTeeAttestations: snapshot lookup failed", types.PoC, "stage", stage, "err", err)
		return err
	}
	if !found {
		rejected, advErr := k.advancePendingTeeAttestationsWithoutSnapshot(ctx, stage, height)
		if advErr != nil {
			k.LogError("SettleTeeAttestations: pending-reject failed", types.PoC, "stage", stage, "err", advErr)
			return advErr
		}
		pruned, pruneErr := k.PruneTeeValidationsForStage(ctx, stage)
		if pruneErr != nil {
			k.LogWarn("SettleTeeAttestations: prune failed", types.PoC, "stage", stage, "err", pruneErr)
		}
		k.LogWarn("SettleTeeAttestations: no PoCValidationSnapshot, rejected pending attestations",
			types.PoC, "stage", stage, "rejected", rejected, "pruned_votes", pruned)
		return nil
	}

	votingPower := buildTeeVotingPowerMap(snapshot)
	totalWeight := snapshot.TotalNetworkWeight
	if totalWeight <= 0 {
		rejected, advErr := k.advancePendingTeeAttestationsWithoutVotes(ctx, stage, height, nil)
		if advErr != nil {
			k.LogError("SettleTeeAttestations: zero-weight pending-reject failed", types.PoC, "stage", stage, "err", advErr)
			return advErr
		}
		pruned, pruneErr := k.PruneTeeValidationsForStage(ctx, stage)
		if pruneErr != nil {
			k.LogWarn("SettleTeeAttestations: prune failed", types.PoC, "stage", stage, "err", pruneErr)
		}
		k.LogWarn("SettleTeeAttestations: zero TotalNetworkWeight, rejected pending attestations",
			types.PoC, "stage", stage, "rejected_without_votes", rejected, "pruned_votes", pruned)
		return nil
	}

	thresholdBps := teeParams.ValidationThresholdBps
	if thresholdBps == 0 {
		thresholdBps = types.DefaultTeeValidationThresholdBps
	}

	subjects, err := k.TallyTeeValidationsForStage(ctx, stage)
	if err != nil {
		k.LogError("SettleTeeAttestations: vote scan failed", types.PoC, "stage", stage, "err", err)
		return err
	}

	votedSubjects := make(map[teeSubjectNode]struct{}, len(subjects))
	for _, s := range subjects {
		votedSubjects[teeSubjectNode{subject: s.Subject.String(), nodeID: s.NodeID}] = struct{}{}
	}

	settled := 0
	for _, s := range subjects {
		att, attFound, err := k.GetTeeAttestation(ctx, s.Subject, s.NodeID)
		if err != nil {
			k.LogWarn("SettleTeeAttestations: attestation lookup failed", types.PoC,
				"stage", stage, "subject", s.Subject.String(), "node_id", s.NodeID, "err", err)
			continue
		}
		if !attFound || att.AttestedAtHeight != stage {
			continue
		}

		if att.VerificationStatus != types.TeeVerificationStatus_TEE_VERIFICATION_PENDING {
			continue
		}

		okWeight, rejectWeight := tallyVotes(s.Votes, votingPower)
		status := decideVerdict(okWeight, rejectWeight, totalWeight, thresholdBps)

		att.VerificationStatus = status
		att.VerifiedAtHeight = height
		att.OkVotingPowerBps = uint32(weightToBps(okWeight, totalWeight))
		att.RejectVotingPowerBps = uint32(weightToBps(rejectWeight, totalWeight))
		att.PendingEpochs = 0
		if status != types.TeeVerificationStatus_TEE_VERIFICATION_PENDING {
			att.Quote = nil
		}

		if err := k.SetTeeAttestation(ctx, att); err != nil {
			k.LogWarn("SettleTeeAttestations: failed to write settled attestation", types.PoC,
				"stage", stage, "subject", s.Subject.String(), "node_id", s.NodeID, "err", err)
			continue
		}
		settled++
	}

	rejectedWithoutVotes, err := k.advancePendingTeeAttestationsWithoutVotes(
		ctx, stage, height, votedSubjects,
	)
	if err != nil {
		k.LogWarn("SettleTeeAttestations: reject-without-votes failed", types.PoC, "stage", stage, "err", err)
	}

	pruned, err := k.PruneTeeValidationsForStage(ctx, stage)
	if err != nil {
		k.LogWarn("SettleTeeAttestations: prune failed", types.PoC, "stage", stage, "err", err)
	}

	k.LogInfo("SettleTeeAttestations: complete", types.PoC,
		"stage", stage,
		"subjects", len(subjects),
		"settled", settled,
		"rejected_without_votes", rejectedWithoutVotes,
		"pruned_votes", pruned)
	return nil
}

func buildTeeVotingPowerMap(s types.PoCValidationSnapshot) map[string]int64 {
	out := make(map[string]int64)
	for _, mvp := range s.ModelVotingPowers {
		if mvp == nil {
			continue
		}
		for _, v := range mvp.VotingPowers {
			if v == nil || v.Address == "" || v.VotingPower <= 0 {
				continue
			}
			if existing, ok := out[v.Address]; !ok || v.VotingPower > existing {
				out[v.Address] = v.VotingPower
			}
		}
	}
	return out
}

func tallyVotes(votes []types.TeeValidation, vp map[string]int64) (okWeight, rejectWeight int64) {
	for _, v := range votes {
		w := vp[v.ValidatorAddress]
		if w <= 0 {
			continue
		}
		switch v.Verdict {
		case types.TeeVerdict_TEE_VERDICT_OK:
			okWeight += w
		case types.TeeVerdict_TEE_VERDICT_REJECT:
			rejectWeight += w
		}
	}
	return
}

func decideVerdict(okWeight, rejectWeight, totalWeight int64, thresholdBps uint32) types.TeeVerificationStatus {
	if int64(thresholdBps)*totalWeight == 0 {
		return types.TeeVerificationStatus_TEE_VERIFICATION_REJECTED
	}
	okHits := okWeight*10000 >= totalWeight*int64(thresholdBps)
	rejectHits := rejectWeight*10000 >= totalWeight*int64(thresholdBps)
	switch {
	case okHits:
		return types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED
	case rejectHits:
		return types.TeeVerificationStatus_TEE_VERIFICATION_REJECTED
	default:
		return types.TeeVerificationStatus_TEE_VERIFICATION_REJECTED
	}
}

func weightToBps(weight, total int64) int64 {
	if total <= 0 || weight <= 0 {
		return 0
	}
	bps := (weight * 10000) / total
	if bps > 10000 {
		bps = 10000
	}
	return bps
}

type teeSubjectNode struct {
	subject string
	nodeID  string
}

func (k Keeper) advancePendingTeeAttestationsWithoutVotes(
	ctx sdk.Context,
	stage int64,
	height int64,
	votedSubjects map[teeSubjectNode]struct{},
) (int, error) {
	keys, err := k.listPendingTeeAttestationKeysForStage(ctx, stage)
	if err != nil {
		return 0, err
	}
	advanced := 0
	for _, key := range keys {
		subject := key.K2()
		nodeID := key.K3()
		att, found, err := k.GetTeeAttestation(ctx, subject, nodeID)
		if err != nil {
			k.LogWarn("SettleTeeAttestations: failed to load pending attestation", types.PoC,
				"stage", stage, "subject", subject.String(), "node_id", nodeID, "err", err)
			continue
		}
		if !found {

			k.removeStalePendingTeeIndexEntry(ctx, key, stage, subject.String(), nodeID)
			continue
		}
		if att.AttestedAtHeight != stage || att.VerificationStatus != types.TeeVerificationStatus_TEE_VERIFICATION_PENDING {

			k.removeStalePendingTeeIndexEntry(ctx, key, stage, att.ParticipantAddress, att.NodeId)
			continue
		}
		if _, voted := votedSubjects[teeSubjectNode{subject: att.ParticipantAddress, nodeID: att.NodeId}]; voted {
			continue
		}

		att.VerifiedAtHeight = height
		att.OkVotingPowerBps = 0
		att.RejectVotingPowerBps = 0
		att.PendingEpochs = 0
		att.VerificationStatus = types.TeeVerificationStatus_TEE_VERIFICATION_REJECTED
		att.Quote = nil

		if err := k.SetTeeAttestation(ctx, att); err != nil {
			k.LogWarn("SettleTeeAttestations: failed to write no-quorum attestation", types.PoC,
				"stage", stage, "subject", att.ParticipantAddress, "node_id", att.NodeId, "err", err)
			continue
		}
		advanced++
	}
	return advanced, nil
}

func (k Keeper) advancePendingTeeAttestationsWithoutSnapshot(
	ctx sdk.Context,
	stage int64,
	height int64,
) (int, error) {
	keys, err := k.listPendingTeeAttestationKeysForStage(ctx, stage)
	if err != nil {
		return 0, err
	}
	advanced := 0
	for _, key := range keys {
		subject := key.K2()
		nodeID := key.K3()
		att, found, err := k.GetTeeAttestation(ctx, subject, nodeID)
		if err != nil {
			k.LogWarn("SettleTeeAttestations: failed to load pending attestation", types.PoC,
				"stage", stage, "subject", subject.String(), "node_id", nodeID, "err", err)
			continue
		}
		if !found {
			k.removeStalePendingTeeIndexEntry(ctx, key, stage, subject.String(), nodeID)
			continue
		}
		if att.AttestedAtHeight != stage || att.VerificationStatus != types.TeeVerificationStatus_TEE_VERIFICATION_PENDING {
			k.removeStalePendingTeeIndexEntry(ctx, key, stage, att.ParticipantAddress, att.NodeId)
			continue
		}

		att.VerifiedAtHeight = height
		att.PendingEpochs = 0
		att.VerificationStatus = types.TeeVerificationStatus_TEE_VERIFICATION_REJECTED
		att.Quote = nil
		if err := k.SetTeeAttestation(ctx, att); err != nil {
			return 0, err
		}
		advanced++
	}
	return advanced, nil
}

func (k Keeper) listPendingTeeAttestationKeysForStage(
	ctx sdk.Context,
	stage int64,
) ([]collections.Triple[int64, sdk.AccAddress, string], error) {
	rng := collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](stage)
	it, err := k.TeePendingAttestationsByStage.Iterate(ctx, rng)
	if err != nil {
		return nil, err
	}
	defer it.Close()
	return it.Keys()
}

func (k Keeper) removeStalePendingTeeIndexEntry(
	ctx sdk.Context,
	key collections.Triple[int64, sdk.AccAddress, string],
	stage int64,
	subject string,
	nodeID string,
) {
	if err := k.TeePendingAttestationsByStage.Remove(ctx, key); err != nil && !errors.Is(err, collections.ErrNotFound) {
		k.LogWarn("SettleTeeAttestations: failed to remove stale pending index key", types.PoC,
			"stage", stage, "subject", subject, "node_id", nodeID, "err", err)
	}
}
