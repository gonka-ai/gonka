package simulation

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// simBlockTimeFloor matches cosmos-sdk x/simulation/params.go
// minTimePerBlock. The runner advances block time uniformly in
// [5000s, 10000s]. Future ops fire when header time strictly exceeds
// the scheduled BlockTime, so scheduling at the floor guarantees
// arrival at or before the target sim block — a late estimate would
// miss a PoC window and cost a whole sim epoch.
const simBlockTimeFloor = 5000 * time.Second

// pocFactoryState dedups self-reschedule by sim-epoch index across the
// lifetime of one factory instance. simsx fires queued ops on a single
// goroutine today; the atomic guard pins the contract so a concurrent
// runner upgrade does not silently double-book.
//
// Concurrency assumption: cosmos-sdk simsx fires queued ops serially
// today (x/simulation/simulate.go runQueuedTimeOperations is a single
// goroutine). A single *LazyStateSimMsgFactory instance is reused
// across multiple queued fires, and SetFutureOpsRegistry mutates its
// fsOpsReg field on each fire (testutil/simsx/registry.go); if a
// future runner upgrade dispatches factories concurrently, the
// closure's read of r.fsOpsReg would race that write. The atomic
// guard here is forward-compatible; the closure-side assumption
// upstream is not — re-audit if upstream introduces parallel sim ops.
type pocFactoryState struct {
	lastScheduledEpoch atomic.Uint64
}

// newPocFactoryState returns an initialised factory-state guard.
func newPocFactoryState() *pocFactoryState {
	return &pocFactoryState{}
}

// scheduleForEpoch enqueues factory at the estimated arrival time of
// targetHeight, but only when epochIndex strictly exceeds every prior
// schedule for this state. Returns true when a schedule was added.
func (s *pocFactoryState) scheduleForEpoch(
	ctx context.Context,
	fOpsReg simsx.FutureOpsRegistry,
	epochIndex uint64,
	currentHeight, targetHeight int64,
	factory simsx.SimMsgFactoryX,
) bool {
	for {
		prev := s.lastScheduledEpoch.Load()
		if epochIndex <= prev {
			return false
		}
		if s.lastScheduledEpoch.CompareAndSwap(prev, epochIndex) {
			break
		}
	}
	delta := targetHeight - currentHeight
	if delta < 1 {
		delta = 1
	}
	now := sdk.UnwrapSDKContext(ctx).BlockTime()
	fOpsReg.Add(now.Add(time.Duration(delta)*simBlockTimeFloor), factory)
	return true
}
