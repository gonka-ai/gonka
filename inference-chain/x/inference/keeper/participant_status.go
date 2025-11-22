package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

// UpdateParticipantStatus is the single entry point for changing a participant's status.
// It detects transitions and applies side-effects exactly once. Currently, when transitioning
// to INVALID it will: slash collateral, record an exclusion entry for the current epoch,
// and invoke removal from EpochGroup memberships for the current epoch.
func (k Keeper) UpdateParticipantStatus(ctx context.Context, participant *types.Participant) error {
	if participant == nil {
		return nil
	}
	if participant.CurrentEpochStats == nil {
		participant.CurrentEpochStats = &types.CurrentEpochStats{}
	}

	oldParticipant, found := k.GetParticipant(ctx, participant.Address)
	if !found {
		oldParticipant = *participant
	}

	params := k.GetParams(ctx)
	originalStatus := participant.Status
	newStatus, reason, newStats := calculations.ComputeStatus(
		params.ValidationParams,
		params.ConfirmationPocParams,
		*participant,
		*oldParticipant.CurrentEpochStats,
	)
	participant.CurrentEpochStats = &newStats

	k.LogInfo("Participant status updated", types.Validation, "address", participant.Address, "original", originalStatus, "new", newStatus, "reason", reason, "stats", participant.CurrentEpochStats)

	if originalStatus == newStatus {
		return nil
	}

	// This should be the ONLY place status is set
	participant.Status = newStatus

	// Handle transition to INVALID once.
	if originalStatus != types.ParticipantStatus_INVALID && newStatus == types.ParticipantStatus_INVALID {
		return k.invalidateParticipant(ctx, participant, reason, params)
	}

	if originalStatus != types.ParticipantStatus_INACTIVE && newStatus == types.ParticipantStatus_INACTIVE {
		return k.deactiveParticipant(ctx, participant, reason, params)
	}

	return nil
}

func (k Keeper) deactiveParticipant(ctx context.Context, participant *types.Participant, reason calculations.ParticipantStatusReason, params types.Params) error {
	k.LogWarn("Participant deactivated for downtime", types.Validation, "address", participant.Address, "reason", reason, "stats", participant.CurrentEpochStats)
	// 1) Slash collateral
	k.SlashForDowntime(ctx, participant, params)

	// 2) Record exclusion
	k.recordExclusion(ctx, participant, reason)

	// 3) Reduce reputation
	participant.EpochsCompleted = multiply(participant.EpochsCompleted, params.ValidationParams.DowntimeReputationPreserve)

	// 4) Remove from all epoch groups
	return k.removeFromEpochGroups(ctx, participant, reason)
}

// invalidateParticipant performs all side-effects associated with a participant becoming INVALID.
// This includes:
// - Slashing collateral according to params.CollateralParams.SlashFractionInvalid
// - Recording an ExcludedParticipants entry for the current effective epoch
// - Removing the participant from the EpochGroup parent and all model sub-groups for the current epoch
// Idempotency: Recording to ExcludedParticipants uses Set with (epoch_index, address) composite key.
func (k Keeper) invalidateParticipant(ctx context.Context, participant *types.Participant, reason calculations.ParticipantStatusReason, params types.Params) error {
	k.LogWarn("Participant invalidated", types.Validation, "address", participant.Address, "reason", reason, "stats", participant.CurrentEpochStats)
	// 1) Slash collateral
	k.SlashForInvalidStatus(ctx, participant, params)

	// 2) Record exclusion entry for current effective epoch (if available)
	k.recordExclusion(ctx, participant, reason)

	// 3) Reduce reputation
	participant.EpochsCompleted = multiply(participant.EpochsCompleted, params.ValidationParams.InvalidReputationPreserve)

	// 4) Remove from current-epoch EpochGroup memberships
	return k.removeFromEpochGroups(ctx, participant, reason)
}

// This is way messier than you'd expect...
func multiply(completed uint32, preserve *types.Decimal) uint32 {
	if preserve == nil {
		return completed
	}
	pd := preserve.ToDecimal()
	if pd.LessThan(decimal.Zero) || pd.GreaterThan(decimal.NewFromInt(1)) {
		return completed
	}
	toDecimal := preserve.ToDecimal()
	result := decimal.NewFromInt32(int32(completed)).Mul(toDecimal)
	return uint32(result.Round(0).IntPart())
}

func (k Keeper) removeFromEpochGroups(ctx context.Context, participant *types.Participant, reason calculations.ParticipantStatusReason) error {
	parentGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("Failed to get current epoch group", types.Validation, "error", err)
		return err
	}
	return parentGroup.RemoveMember(ctx, participant)
}

func (k Keeper) recordExclusion(ctx context.Context, participant *types.Participant, reason calculations.ParticipantStatusReason) {
	if epochIndex, ok := k.GetEffectiveEpochIndex(ctx); ok {
		addr, err := sdk.AccAddressFromBech32(participant.Address)
		if err == nil {
			_ = k.ExcludedParticipantsMap.Set(ctx, collections.Join(epochIndex, addr), types.ExcludedParticipant{
				Address:              participant.Address,
				EpochIndex:           epochIndex,
				Reason:               string(reason),
				ExclusionBlockHeight: uint64(sdk.UnwrapSDKContext(ctx).BlockHeight()),
			})
		} else {
			k.LogError("Failed to parse participant address for exclusion entry", types.Validation, "address", participant.Address, "error", err)
		}
	}
}
