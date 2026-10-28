package escrow

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/store"
)

const (
	finalizeWaitLimit = 5 * time.Second
	ownEpochOutside   = 8
	ownEpochInside    = 7

	firstForcedEscrowID = 40
)

var snapshotBeforeTheSwitch = chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2600, EpochSwitchBlockHeight: 3100}

// stalledSettlementSource holds the finalize of the named escrows until released, as a host that answers nothing holds a real one.
type stalledSettlementSource struct {
	*fakeSettlementSource
	stalled map[string]bool
	release chan struct{}
	reached chan string

	mu        sync.Mutex
	finalized map[string]int
}

func newStalledSettlementSource(capacity int, stalledEscrowIDs ...string) *stalledSettlementSource {
	stalled := map[string]bool{}
	for _, escrowID := range stalledEscrowIDs {
		stalled[escrowID] = true
	}
	return &stalledSettlementSource{
		fakeSettlementSource: &fakeSettlementSource{},
		stalled:              stalled,
		release:              make(chan struct{}),
		reached:              make(chan string, capacity),
		finalized:            map[string]int{},
	}
}

func (source *stalledSettlementSource) Finalize(ctx context.Context, escrowID string) error {
	source.mu.Lock()
	source.finalized[escrowID]++
	source.mu.Unlock()
	source.reached <- escrowID
	if source.stalled[escrowID] {
		<-source.release
	}
	return source.fakeSettlementSource.Finalize(ctx, escrowID)
}

func (source *stalledSettlementSource) finalizeCount(escrowID string) int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.finalized[escrowID]
}

func awaitFinalizes(t *testing.T, source *stalledSettlementSource, escrowIDs ...string) {
	t.Helper()
	waiting := map[string]bool{}
	for _, escrowID := range escrowIDs {
		waiting[escrowID] = true
	}
	deadline := time.After(finalizeWaitLimit)
	for len(waiting) > 0 {
		select {
		case reached := <-source.reached:
			delete(waiting, reached)
		case <-deadline:
			t.Fatalf("escrows %v never reached Finalize", slices.Sorted(func(yield func(string) bool) {
				for escrowID := range waiting {
					if !yield(escrowID) {
						return
					}
				}
			}))
		}
	}
}

func parkedRowOfEpoch(escrowID string, ownEpoch uint64) store.DevshardRecord {
	row := parkedRecord(escrowID)
	row.ChainEpoch = ownEpoch
	return row
}

func concurrentSettlementManager(t *testing.T, testStore *fakeStore, source *stalledSettlementSource, operatorSettles *sync.WaitGroup) *Manager {
	t.Helper()
	manager := &Manager{
		tx:               settlingTxClient(),
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: source,
		config:           holderWithSettlementEnabled(true),
		chainFacts:       &fakeTxClient{},
		now:              time.Now,
	}
	t.Cleanup(func() {
		close(source.release)
		manager.offTickWork.Wait()
		operatorSettles.Wait()
	})
	return manager
}

func storeParkedRows(testStore *fakeStore, rows ...store.DevshardRecord) {
	testStore.mu.Lock()
	defer testStore.mu.Unlock()
	for _, row := range rows {
		testStore.devshards[row.EscrowID] = row
	}
}

func rowsOfEpoch(ownEpoch uint64, escrowIDs ...string) []store.DevshardRecord {
	rows := make([]store.DevshardRecord, 0, len(escrowIDs))
	for _, escrowID := range escrowIDs {
		rows = append(rows, parkedRowOfEpoch(escrowID, ownEpoch))
	}
	return rows
}

// Test flow:
//  1. Park two escrows outside their margin, the first one's finalize stalling on a silent host.
//  2. Run one settle tick.
//  3. Assert the second escrow reaches Finalize while the first is still stalled: one slow finalize must not hold the others.
func TestAStalledFinalizeDoesNotHoldTheNextParkedEscrow(t *testing.T) {
	testStore := newFakeStore()
	rows := rowsOfEpoch(ownEpochOutside, "1", "2")
	storeParkedRows(testStore, rows...)
	source := newStalledSettlementSource(len(rows), "1")
	manager := concurrentSettlementManager(t, testStore, source, &sync.WaitGroup{})

	manager.settleOffTick(context.Background(), snapshotBeforeTheSwitch, rows)

	awaitFinalizes(t, source, "1", "2")
}

// Test flow:
//  1. Stall as many escrows outside their margin as the budget holds, in flight from one tick.
//  2. Settle once more, waiting for what that pass starts, over them, one more escrow outside the margin and one inside it.
//  3. Assert the one inside the margin was finalized, the extra one outside never was, and no stalled escrow was started again: settles still running from an earlier tick spend the budget.
func TestTheBudgetCountsSettlesStillRunningFromEarlierTicks(t *testing.T) {
	testStore := newFakeStore()
	stalledIDs := []string{"10", "11", "12", "13"}
	stalled := rowsOfEpoch(ownEpochOutside, stalledIDs...)
	extra, urgent := parkedRowOfEpoch("14", ownEpochOutside), parkedRowOfEpoch("30", ownEpochInside)
	storeParkedRows(testStore, append(stalled, extra, urgent)...)
	source := newStalledSettlementSource(len(stalled)+2, stalledIDs...)
	manager := concurrentSettlementManager(t, testStore, source, &sync.WaitGroup{})

	manager.settleOffTick(context.Background(), snapshotBeforeTheSwitch, stalled)
	awaitFinalizes(t, source, stalledIDs...)
	if err := manager.settlePending(context.Background(), snapshotBeforeTheSwitch, append(slices.Clone(stalled), extra, urgent)); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}

	if count := source.finalizeCount(urgent.EscrowID); count != 1 {
		t.Fatalf("escrow %s finalized %d times, want 1: a row inside the margin settles past the spent budget", urgent.EscrowID, count)
	}
	if count := source.finalizeCount(extra.EscrowID); count != 0 {
		t.Fatalf("escrow %s finalized %d times, want 0: the budget is spent by the settles still running", extra.EscrowID, count)
	}
	for _, escrowID := range stalledIDs {
		if count := source.finalizeCount(escrowID); count != 1 {
			t.Fatalf("escrow %s finalized %d times, want 1: a settling escrow must not be started again", escrowID, count)
		}
	}
}

// Test flow:
//  1. Stall the settle of an escrow inside its margin, in flight from one tick.
//  2. Run the next tick over it and as many escrows outside the margin as the budget holds.
//  3. Assert every one of those reaches Finalize: a settle inside the margin does not spend the budget.
func TestASettleInsideTheMarginLeavesTheBudgetToTheOthers(t *testing.T) {
	testStore := newFakeStore()
	urgent := parkedRowOfEpoch("30", ownEpochInside)
	budgetedIDs := []string{"10", "11", "12", "13"}
	budgeted := rowsOfEpoch(ownEpochOutside, budgetedIDs...)
	storeParkedRows(testStore, append(budgeted, urgent)...)
	source := newStalledSettlementSource(len(budgeted)+1, urgent.EscrowID)
	manager := concurrentSettlementManager(t, testStore, source, &sync.WaitGroup{})

	manager.settleOffTick(context.Background(), snapshotBeforeTheSwitch, []store.DevshardRecord{urgent})
	awaitFinalizes(t, source, urgent.EscrowID)
	manager.settleOffTick(context.Background(), snapshotBeforeTheSwitch, append(slices.Clone(budgeted), urgent))

	awaitFinalizes(t, source, budgetedIDs...)
}

// Test flow:
//  1. Stall an operator's settle of an escrow outside its margin.
//  2. Run a tick over that escrow and as many others outside the margin as the budget holds.
//  3. Assert every other one reaches Finalize and the operator's escrow was finalized once: the tick leaves a settle already in flight alone, and does not spend a slot on it.
func TestTheTickLeavesAnOperatorSettleInFlightAlone(t *testing.T) {
	testStore := newFakeStore()
	operated := parkedRowOfEpoch("1", ownEpochOutside)
	othersIDs := []string{"10", "11", "12", "13"}
	others := rowsOfEpoch(ownEpochOutside, othersIDs...)
	storeParkedRows(testStore, append(others, operated)...)
	source := newStalledSettlementSource(len(others)+1, operated.EscrowID)
	var operatorSettles sync.WaitGroup
	manager := concurrentSettlementManager(t, testStore, source, &operatorSettles)

	operatorSettles.Go(func() { _, _ = manager.Settle(context.Background(), operated.EscrowID, false) })
	awaitFinalizes(t, source, operated.EscrowID)
	manager.settleOffTick(context.Background(), snapshotBeforeTheSwitch, append(slices.Clone(others), operated))

	awaitFinalizes(t, source, othersIDs...)
	if count := source.finalizeCount(operated.EscrowID); count != 1 {
		t.Fatalf("escrow %s finalized %d times, want 1", operated.EscrowID, count)
	}
}

// Test flow:
//  1. Stall as many escrows inside their margin as the forced budget holds, in flight from one tick.
//  2. Settle once more, waiting for what that pass starts, over them and one more escrow inside its margin.
//  3. Assert the extra one was never finalized: settles inside the margin are unbudgeted against the others, never unbounded.
func TestSettlesInsideTheMarginStayWithinTheirOwnBound(t *testing.T) {
	testStore := newFakeStore()
	stalledIDs := make([]string, 0, urgentSettleBudget)
	for index := range urgentSettleBudget {
		stalledIDs = append(stalledIDs, fmt.Sprintf("%d", firstForcedEscrowID+index))
	}
	stalled := rowsOfEpoch(ownEpochInside, stalledIDs...)
	extra := parkedRowOfEpoch(fmt.Sprintf("%d", firstForcedEscrowID+urgentSettleBudget), ownEpochInside)
	storeParkedRows(testStore, append(stalled, extra)...)
	source := newStalledSettlementSource(len(stalled)+1, stalledIDs...)
	manager := concurrentSettlementManager(t, testStore, source, &sync.WaitGroup{})

	manager.settleOffTick(context.Background(), snapshotBeforeTheSwitch, stalled)
	awaitFinalizes(t, source, stalledIDs...)
	if err := manager.settlePending(context.Background(), snapshotBeforeTheSwitch, append(slices.Clone(stalled), extra)); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}

	if count := source.finalizeCount(extra.EscrowID); count != 0 {
		t.Fatalf("escrow %s finalized %d times, want 0 past the forced budget of %d", extra.EscrowID, count, urgentSettleBudget)
	}
}

// Test flow:
//  1. Settle a parked escrow to completion, which drops its row.
//  2. Settle again from the same, now stale, list of rows.
//  3. Assert the second pass reports nothing: a row a finished settle dropped is settled, not failed.
func TestASettleFromAStaleListReportsNoFailure(t *testing.T) {
	testStore := newFakeStore()
	row := parkedRowOfEpoch("1", ownEpochOutside)
	storeParkedRows(testStore, row)
	source := newStalledSettlementSource(2)
	manager := concurrentSettlementManager(t, testStore, source, &sync.WaitGroup{})
	stale := []store.DevshardRecord{row}

	if err := manager.settlePending(context.Background(), snapshotBeforeTheSwitch, stale); err != nil {
		t.Fatalf("first settlePending() = %v, want nil", err)
	}
	if err := manager.settlePending(context.Background(), snapshotBeforeTheSwitch, stale); err != nil {
		t.Fatalf("settlePending() over the stale list = %v, want nil", err)
	}
}
