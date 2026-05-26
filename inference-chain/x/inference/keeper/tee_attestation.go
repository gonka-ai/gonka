package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

func teeAttestationKey(participant sdk.AccAddress, nodeID string) collections.Pair[sdk.AccAddress, string] {
	return collections.Join(participant, nodeID)
}

func teeAttestationExpiryKey(expiresAtHeight int64, participant sdk.AccAddress, nodeID string) collections.Triple[int64, sdk.AccAddress, string] {
	return collections.Join3(expiresAtHeight, participant, nodeID)
}

func teePendingAttestationStageKey(stage int64, participant sdk.AccAddress, nodeID string) collections.Triple[int64, sdk.AccAddress, string] {
	return collections.Join3(stage, participant, nodeID)
}

func (k Keeper) SetTeeAttestation(ctx context.Context, attestation types.TeeAttestation) error {
	if attestation.ParticipantAddress == "" {
		return sdkerrors.Wrap(types.ErrInvalidAddress, "participant_address is empty")
	}
	if attestation.NodeId == "" {
		return sdkerrors.Wrap(types.ErrTeeAttestationNodeIdEmpty, "node_id is empty")
	}
	participantAddr, err := sdk.AccAddressFromBech32(attestation.ParticipantAddress)
	if err != nil {
		return sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid participant_address: %v", err))
	}

	primaryKey := teeAttestationKey(participantAddr, attestation.NodeId)
	if previous, found, err := k.getTeeAttestationByKey(ctx, primaryKey); err != nil {
		return err
	} else if found {
		if err := k.TeeAttestationsByExpiry.Remove(
			ctx,
			teeAttestationExpiryKey(previous.ExpiresAtHeight, participantAddr, previous.NodeId),
		); err != nil {
			return sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to remove stale expiry index entry: %v", err))
		}
		if previous.VerificationStatus == types.TeeVerificationStatus_TEE_VERIFICATION_PENDING {
			if err := k.TeePendingAttestationsByStage.Remove(
				ctx,
				teePendingAttestationStageKey(previous.AttestedAtHeight, participantAddr, previous.NodeId),
			); err != nil && !errors.Is(err, collections.ErrNotFound) {
				return sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to remove stale pending-stage index entry: %v", err))
			}
		}
	}

	if err := k.TeeAttestations.Set(ctx, primaryKey, attestation); err != nil {
		return sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store tee attestation: %v", err))
	}
	if err := k.TeeAttestationsByExpiry.Set(
		ctx,
		teeAttestationExpiryKey(attestation.ExpiresAtHeight, participantAddr, attestation.NodeId),
	); err != nil {
		return sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to update expiry index: %v", err))
	}
	if attestation.VerificationStatus == types.TeeVerificationStatus_TEE_VERIFICATION_PENDING {
		if err := k.TeePendingAttestationsByStage.Set(
			ctx,
			teePendingAttestationStageKey(attestation.AttestedAtHeight, participantAddr, attestation.NodeId),
		); err != nil {
			return sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to update pending-stage index: %v", err))
		}
	}
	return nil
}

func (k Keeper) GetTeeAttestation(ctx context.Context, participant sdk.AccAddress, nodeID string) (types.TeeAttestation, bool, error) {
	return k.getTeeAttestationByKey(ctx, teeAttestationKey(participant, nodeID))
}

func (k Keeper) getTeeAttestationByKey(ctx context.Context, key collections.Pair[sdk.AccAddress, string]) (types.TeeAttestation, bool, error) {
	att, err := k.TeeAttestations.Get(ctx, key)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.TeeAttestation{}, false, nil
		}
		return types.TeeAttestation{}, false, err
	}
	return att, true, nil
}

func (k Keeper) RemoveTeeAttestation(ctx context.Context, participant sdk.AccAddress, nodeID string) error {
	primaryKey := teeAttestationKey(participant, nodeID)
	existing, found, err := k.getTeeAttestationByKey(ctx, primaryKey)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if err := k.TeeAttestations.Remove(ctx, primaryKey); err != nil {
		return sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to remove tee attestation: %v", err))
	}
	if err := k.TeeAttestationsByExpiry.Remove(
		ctx,
		teeAttestationExpiryKey(existing.ExpiresAtHeight, participant, existing.NodeId),
	); err != nil {
		return sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to remove expiry index entry: %v", err))
	}
	if existing.VerificationStatus == types.TeeVerificationStatus_TEE_VERIFICATION_PENDING {
		if err := k.TeePendingAttestationsByStage.Remove(
			ctx,
			teePendingAttestationStageKey(existing.AttestedAtHeight, participant, existing.NodeId),
		); err != nil && !errors.Is(err, collections.ErrNotFound) {
			return sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to remove pending-stage index entry: %v", err))
		}
	}
	return nil
}

func (k Keeper) ListTeeAttestationsByParticipant(ctx context.Context, participant sdk.AccAddress) ([]types.TeeAttestation, error) {
	rng := collections.NewPrefixedPairRange[sdk.AccAddress, string](participant)
	iter, err := k.TeeAttestations.Iterate(ctx, rng)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []types.TeeAttestation
	for ; iter.Valid(); iter.Next() {
		v, err := iter.Value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (k Keeper) PruneExpiredTeeAttestations(ctx context.Context, currentBlockHeight int64, maxToPrune int) (int, error) {
	if maxToPrune <= 0 {
		return 0, nil
	}

	iter, err := k.TeeAttestationsByExpiry.Iterate(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	type victim struct {
		expiry      int64
		participant sdk.AccAddress
		nodeID      string
	}
	victims := make([]victim, 0, maxToPrune)
	for ; iter.Valid() && len(victims) < maxToPrune; iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return 0, err
		}
		if key.K1() > currentBlockHeight {
			break
		}
		victims = append(victims, victim{
			expiry:      key.K1(),
			participant: key.K2(),
			nodeID:      key.K3(),
		})
	}

	pruned := 0
	for _, v := range victims {
		attestation, found, err := k.getTeeAttestationByKey(ctx, teeAttestationKey(v.participant, v.nodeID))
		if err != nil {
			k.LogError("failed to get expired tee attestation",
				types.Pruning,
				"participant", v.participant.String(),
				"node_id", v.nodeID,
				"error", err,
			)
			continue
		}
		if found {
			if err := k.TeeAttestations.Remove(ctx, teeAttestationKey(v.participant, v.nodeID)); err != nil {
				k.LogError("failed to remove expired tee attestation",
					types.Pruning,
					"participant", v.participant.String(),
					"node_id", v.nodeID,
					"error", err,
				)
				continue
			}
			if attestation.VerificationStatus == types.TeeVerificationStatus_TEE_VERIFICATION_PENDING {
				if err := k.TeePendingAttestationsByStage.Remove(
					ctx,
					teePendingAttestationStageKey(attestation.AttestedAtHeight, v.participant, v.nodeID),
				); err != nil && !errors.Is(err, collections.ErrNotFound) {
					k.LogError("failed to remove pending-stage index entry for expired tee attestation",
						types.Pruning,
						"participant", v.participant.String(),
						"node_id", v.nodeID,
						"error", err,
					)
					continue
				}
			}
		}
		if err := k.TeeAttestationsByExpiry.Remove(ctx, teeAttestationExpiryKey(v.expiry, v.participant, v.nodeID)); err != nil {
			k.LogError("failed to remove expired tee attestation index entry",
				types.Pruning,
				"participant", v.participant.String(),
				"node_id", v.nodeID,
				"error", err,
			)
			continue
		}
		pruned++
	}
	if pruned > 0 {
		k.LogInfo("Pruned expired TEE attestations",
			types.Pruning,
			"count", pruned,
			"height", currentBlockHeight,
		)
	}
	return pruned, nil
}
