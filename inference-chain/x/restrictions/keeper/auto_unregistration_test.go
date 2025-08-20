package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/restrictions/types"
)

func TestCheckAndUnregisterRestriction_StillActive(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Set restrictions to be still active
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Future block
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	ctx = ctx.WithBlockHeight(1000000) // Current block before restriction end

	// When restrictions are still active, no unregistration should occur
	err = keeper.CheckAndUnregisterRestriction(ctx)
	require.NoError(t, err)

	// Should still be considered active
	require.True(t, keeper.IsRestrictionActive(ctx))
}

func TestCheckAndUnregisterRestriction_Expired(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Set restrictions to be expired
	params := types.DefaultParams()
	params.RestrictionEndBlock = 1000000 // Past block
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	ctx = ctx.WithBlockHeight(2000000) // Current block after restriction end

	// When restrictions have expired, unregistration should occur
	err = keeper.CheckAndUnregisterRestriction(ctx)
	require.NoError(t, err)

	// Should no longer be considered active
	require.False(t, keeper.IsRestrictionActive(ctx))

	// Check that events were emitted
	events := ctx.EventManager().Events()
	require.Greater(t, len(events), 0)

	// Look for the restriction lifted event
	var found bool
	for _, event := range events {
		if event.Type == types.EventTypeRestrictionLifted {
			found = true

			// Check event attributes
			require.Greater(t, len(event.Attributes), 0)

			// Verify attributes contain expected keys
			hasCurrentBlock := false
			hasEndBlock := false
			for _, attr := range event.Attributes {
				if attr.Key == types.AttributeKeyCurrentBlock {
					hasCurrentBlock = true
				}
				if attr.Key == types.AttributeKeyRestrictionEndBlock {
					hasEndBlock = true
				}
			}
			require.True(t, hasCurrentBlock, "Event should contain current block attribute")
			require.True(t, hasEndBlock, "Event should contain restriction end block attribute")
			break
		}
	}
	require.True(t, found, "Should emit restriction lifted event")
}

func TestCheckAndUnregisterRestriction_AlreadyUnregistered(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Set restrictions to be expired
	params := types.DefaultParams()
	params.RestrictionEndBlock = 1000000 // Past block
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	ctx = ctx.WithBlockHeight(2000000) // Current block after restriction end

	// First call should unregister
	err = keeper.CheckAndUnregisterRestriction(ctx)
	require.NoError(t, err)

	// Clear events from first call
	ctx = ctx.WithEventManager(sdk.NewEventManager())

	// Second call should not do anything (already unregistered)
	err = keeper.CheckAndUnregisterRestriction(ctx)
	require.NoError(t, err)

	// No new events should be emitted
	events := ctx.EventManager().Events()
	hasRestrictionEvent := false
	for _, event := range events {
		if event.Type == types.EventTypeRestrictionLifted {
			hasRestrictionEvent = true
			break
		}
	}
	require.False(t, hasRestrictionEvent, "Should not emit restriction lifted event on second call")
}
