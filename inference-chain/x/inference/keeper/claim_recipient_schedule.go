package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// MaxClaimRecipientLookahead caps how far into the future a participant may
// schedule a recipient override. Bounds per-participant state to ~40 entries,
// preventing cheap state bloat via dumping thousands of future entries.
const MaxClaimRecipientLookahead uint64 = 40

// GetClaimRecipientForEpoch returns the scheduled recipient address for the
// given (participant, epoch). Returns ("", false) if no override exists, in
// which case the caller should pay the participant directly.
func (k Keeper) GetClaimRecipientForEpoch(ctx context.Context, participant sdk.AccAddress, epoch uint64) (string, bool) {
	v, err := k.ClaimRecipients.Get(ctx, collections.Join(participant, epoch))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return "", false
		}
		return "", false
	}
	return v, true
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
