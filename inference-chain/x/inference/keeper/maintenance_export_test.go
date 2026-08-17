package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/x/group"
)

// FilterOutMaintenanceParticipants exposes the unexported method for tests.
func (k Keeper) FilterOutMaintenanceParticipants(ctx context.Context, members []*group.GroupMember) []*group.GroupMember {
	return k.filterOutMaintenanceParticipants(ctx, members)
}
