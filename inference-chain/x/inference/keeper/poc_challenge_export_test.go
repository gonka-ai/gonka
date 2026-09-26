package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/x/group"
)

// FilterOutChallengeParticipants exposes the unexported method for tests.
func (k Keeper) FilterOutChallengeParticipants(ctx context.Context, members []*group.GroupMember) []*group.GroupMember {
	return k.filterOutChallengeParticipants(ctx, members)
}
