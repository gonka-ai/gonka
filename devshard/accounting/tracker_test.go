package accounting

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Register two escrows in different epochs.
//  2. Purge the first escrow's epoch.
//  3. Require the tracker to report it retains only the escrow still in its ledger, and none it never saw.
func TestRetainsEscrowFollowsTheLedger(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "expired", 8, "m")
	registerEscrow(t, tracker, "current", 9, "m")

	_, err := tracker.PurgeEpoch(context.Background(), 8)
	require.NoError(t, err)

	require.False(t, tracker.RetainsEscrow("expired"))
	require.True(t, tracker.RetainsEscrow("current"))
	require.False(t, tracker.RetainsEscrow("never-seen"))
}
