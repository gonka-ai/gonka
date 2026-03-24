package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/x/group"
)

// CreateHealthFilterFnForTest exports the unexported createHealthFilterFn for unit testing.
func (k Keeper) CreateHealthFilterFnForTest(ctx context.Context, blockHeight int64) func([]*group.GroupMember) []*group.GroupMember {
	return k.createHealthFilterFn(ctx, blockHeight)
}
