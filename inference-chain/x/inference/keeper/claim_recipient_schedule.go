package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// MaxClaimRecipientLookahead caps how far into the future a participant may
// schedule a recipient override. Bounds per-participant state to ~40 entries,
// preventing cheap state bloat via dumping thousands of future entries.
const MaxClaimRecipientLookahead uint64 = 40

// GetClaimRecipientForEpoch returns the scheduled recipient address for the
// given (participant, epoch). Returns ("", false, nil) if no override exists,
// in which case the caller should pay the participant directly.
func (k Keeper) GetClaimRecipientForEpoch(ctx context.Context, participant sdk.AccAddress, epoch uint64) (string, bool, error) {
	v, err := k.ClaimRecipients.Get(ctx, collections.Join(participant, epoch))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return v, true, nil
}

// ResolveClaimRecipientAddress returns the payout address configured for
// (participant, epoch), or the participant address when no override exists.
func (k Keeper) ResolveClaimRecipientAddress(ctx context.Context, participant string, epoch uint64) (sdk.AccAddress, error) {
	participantAddr, err := sdk.AccAddressFromBech32(participant)
	if err != nil {
		return nil, err
	}
	recipient, found, err := k.GetClaimRecipientForEpoch(ctx, participantAddr, epoch)
	if err != nil {
		return nil, err
	}
	if !found {
		return participantAddr, nil
	}
	return sdk.AccAddressFromBech32(recipient)
}

// SetClaimRecipientForEpoch writes the primary schedule entry and pruning index
// atomically. All claim-recipient writes must use this helper to keep the two
// collections in sync.
func (k Keeper) SetClaimRecipientForEpoch(ctx sdk.Context, participant sdk.AccAddress, epoch uint64, recipient string) error {
	cacheCtx, writeFn := ctx.CacheContext()
	if err := k.ClaimRecipients.Set(cacheCtx, collections.Join(participant, epoch), recipient); err != nil {
		return errorsmod.Wrapf(err, "failed to set claim recipient for epoch %d", epoch)
	}
	if err := k.ClaimRecipientsByEpoch.Set(cacheCtx, collections.Join(epoch, participant)); err != nil {
		return errorsmod.Wrapf(err, "failed to set claim recipient epoch index for epoch %d", epoch)
	}
	writeFn()
	return nil
}

// RemoveClaimRecipientForEpoch removes the primary schedule entry and pruning
// index atomically. Missing rows are treated as already removed so cleanup paths
// can be idempotent.
func (k Keeper) RemoveClaimRecipientForEpoch(ctx sdk.Context, participant sdk.AccAddress, epoch uint64) error {
	cacheCtx, writeFn := ctx.CacheContext()
	if err := k.ClaimRecipients.Remove(cacheCtx, collections.Join(participant, epoch)); err != nil && !errors.Is(err, collections.ErrNotFound) {
		return errorsmod.Wrapf(err, "failed to remove claim recipient for epoch %d", epoch)
	}
	if err := k.ClaimRecipientsByEpoch.Remove(cacheCtx, collections.Join(epoch, participant)); err != nil && !errors.Is(err, collections.ErrNotFound) {
		return errorsmod.Wrapf(err, "failed to remove claim recipient epoch index for epoch %d", epoch)
	}
	writeFn()
	return nil
}

// GetClaimRecipientsByParticipant returns every (epoch, recipient) override
// currently scheduled for the given participant, ordered by epoch.
func (k Keeper) GetClaimRecipientsByParticipant(ctx context.Context, participant sdk.AccAddress) ([]types.ClaimRecipientEntry, error) {
	it, err := k.ClaimRecipients.Iterate(ctx, collections.NewPrefixedPairRange[sdk.AccAddress, uint64](participant))
	if err != nil {
		return nil, err
	}
	defer it.Close()

	var out []types.ClaimRecipientEntry
	for ; it.Valid(); it.Next() {
		key, err := it.Key()
		if err != nil {
			return nil, err
		}
		recipient, err := it.Value()
		if err != nil {
			return nil, err
		}
		out = append(out, types.ClaimRecipientEntry{
			Epoch:     key.K2(),
			Recipient: recipient,
		})
	}
	return out, nil
}
