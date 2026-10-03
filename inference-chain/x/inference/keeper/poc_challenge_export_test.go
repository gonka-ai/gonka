package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/x/group"
)

// FilterOutChallengeParticipants exposes the unexported method for tests.
func (k Keeper) FilterOutChallengeParticipants(ctx context.Context, members []*group.GroupMember) []*group.GroupMember {
	return k.filterOutChallengeParticipants(ctx, members)
}

// IsChallengedForTesting reports addr through the one-pass challenge set used by CreateDevshardEscrow.
func (k Keeper) IsChallengedForTesting(ctx context.Context, addr string) bool {
	return isChallengedAddress(k.challengedAddresses(ctx), addr)
}
