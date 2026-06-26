package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/types"
)

// SetClaimRecipients batch-configures per-epoch payout recipient overrides for
// MsgClaimRewards. Must be signed by the participant's own (cold) key — this
// message is intentionally NOT included in InferenceOperationKeyPerms so that
// no authz grant is created for warm/operational keys.
//
// Each entry targets a future epoch. An empty recipient deletes the entry for
// that epoch (reverting to the creator default). The whole batch is atomic:
// any invalid entry rolls back the entire message.
func (ms msgServer) SetClaimRecipients(goCtx context.Context, msg *types.MsgSetClaimRecipients) (*types.MsgSetClaimRecipientsResponse, error) {
	if err := ms.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	creatorAddr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}

	currentEpoch, ok := ms.GetEffectiveEpochIndex(ctx)
	if !ok {
		return nil, errorsmod.Wrap(types.ErrCurrentEpochGroupNotFound, "effective epoch not set")
	}

	if len(msg.Entries) == 0 {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "entries must not be empty")
	}

	// Apply into a cache context so the whole batch is atomic: any failed
	// entry rolls back every preceding write.
	cacheCtx, writeFn := ctx.CacheContext()
	for _, entry := range msg.Entries {
		if err := ms.applyClaimRecipientEntry(cacheCtx, creatorAddr, entry, currentEpoch); err != nil {
			return nil, err
		}
	}
	writeFn()

	return &types.MsgSetClaimRecipientsResponse{}, nil
}

// applyClaimRecipientEntry validates one batch entry against the lookahead
// window and writes (or removes) the corresponding ClaimRecipients row.
// Caller is expected to invoke this on a cache context so partial writes
// roll back on the first error.
func (ms msgServer) applyClaimRecipientEntry(
	ctx sdk.Context,
	creatorAddr sdk.AccAddress,
	entry types.ClaimRecipientEntry,
	currentEpoch uint64,
) error {
	if entry.Epoch <= currentEpoch {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "epoch %d is not in the future (current=%d)", entry.Epoch, currentEpoch)
	}
	if entry.Epoch > currentEpoch+MaxClaimRecipientLookahead {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "epoch %d exceeds max lookahead of %d from current epoch %d", entry.Epoch, MaxClaimRecipientLookahead, currentEpoch)
	}
	if entry.Recipient != "" {
		if _, err := sdk.AccAddressFromBech32(entry.Recipient); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid recipient for epoch %d: %s", entry.Epoch, err)
		}
	}

	if entry.Recipient == "" {
		return ms.RemoveClaimRecipientForEpoch(ctx, creatorAddr, entry.Epoch)
	}
	return ms.SetClaimRecipientForEpoch(ctx, creatorAddr, entry.Epoch, entry.Recipient)
}
