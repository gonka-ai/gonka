package escrow

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
	"devshard/signing"
)

type recordingSweeper struct {
	mu      sync.Mutex
	calls   int
	grace   time.Duration
	budget  int
	blockOn chan struct{}

	settleLog    *callLog
	settleEscrow string
	settleBudget int
	settleSwept  [3]int
	settleErr    error
	onSettle     func()
}

func (s *recordingSweeper) SweepBeforeFinalize(_ context.Context, escrowID string, budget int) (int, int, int, error) {
	if s.settleLog != nil {
		s.settleLog.record("SweepBeforeFinalize")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settleEscrow, s.settleBudget = escrowID, budget
	if s.onSettle != nil {
		s.onSettle()
	}
	return s.settleSwept[0], s.settleSwept[1], s.settleSwept[2], s.settleErr
}

func (s *recordingSweeper) settleSweptWith() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settleEscrow, s.settleBudget
}

func (s *recordingSweeper) SweepExecutionTimeouts(_ context.Context, grace time.Duration, budgetPerEscrow int) (int, int, int) {
	s.mu.Lock()
	s.calls++
	s.grace, s.budget = grace, budgetPerEscrow
	block := s.blockOn
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	return 1, 1, 0
}

func (s *recordingSweeper) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func managerWithSweeper(t *testing.T, sweeper TimeoutSweeper, cfg *config.Config) *Manager {
	t.Helper()
	deps := testManagerDeps(t, newFakeStore(), &fakeTxClient{}, &fakeSnapshotSource{}, cfg)
	deps.Timeouts = sweeper
	return mustManager(t, deps)
}

// Test flow:
//  1. Configure a budget of 3 and a grace of 90 seconds, and build a manager with a `recordingSweeper`.
//  2. Call sweepTimeouts and wait for the sweep work to finish.
//  3. Assert the sweeper was called once with the configured grace and budget.
func TestSweepPassesTheConfiguredBudgetAndGrace(t *testing.T) {
	cfg := config.Defaults()
	cfg.TimeoutSweep.BudgetPerTick = 3
	cfg.TimeoutSweep.GraceSeconds = 90
	sweeper := &recordingSweeper{}
	manager := managerWithSweeper(t, sweeper, &cfg)

	manager.sweepTimeouts(context.Background())
	manager.offTickWork.Wait()

	require.Equal(t, 1, sweeper.callCount())
	require.Equal(t, 90*time.Second, sweeper.grace)
	require.Equal(t, 3, sweeper.budget)
}

// Test flow:
//  1. Configure a budget of 0 and build a manager with a `recordingSweeper`.
//  2. Call sweepTimeouts and wait for the sweep work to finish.
//  3. Assert the sweeper was never called.
func TestSweepIsOffWithoutABudget(t *testing.T) {
	cfg := config.Defaults()
	cfg.TimeoutSweep.BudgetPerTick = 0
	sweeper := &recordingSweeper{}
	manager := managerWithSweeper(t, sweeper, &cfg)

	manager.sweepTimeouts(context.Background())
	manager.offTickWork.Wait()

	require.Zero(t, sweeper.callCount())
}

// Test flow:
//  1. Build a manager with a `recordingSweeper` that blocks on a channel until released.
//  2. Call sweepTimeouts once and wait for the sweeper's first call to register.
//  3. Call sweepTimeouts a second time while the first sweep is still blocked, then release the block.
//  4. Wait for the sweep work to finish and assert the sweeper was called only once.
func TestASecondTickDoesNotStartASecondSweep(t *testing.T) {
	cfg := config.Defaults()
	cfg.TimeoutSweep.BudgetPerTick = 2
	release := make(chan struct{})
	sweeper := &recordingSweeper{blockOn: release}
	manager := managerWithSweeper(t, sweeper, &cfg)

	manager.sweepTimeouts(context.Background())
	require.Eventually(t, func() bool { return sweeper.callCount() == 1 }, time.Second, time.Millisecond)
	manager.sweepTimeouts(context.Background())
	close(release)
	manager.offTickWork.Wait()

	require.Equal(t, 1, sweeper.callCount())
}

// Test flow:
//  1. Build a manager with a `recordingSweeper` that blocks on a channel until released.
//  2. Call sweepTimeouts and wait for the sweeper's call to register.
//  3. Call Stop in a goroutine and assert it has not returned after 20ms, while the sweep is still blocked.
//  4. Release the block and assert Stop then returns.
func TestStopWaitsForASweepInFlight(t *testing.T) {
	cfg := config.Defaults()
	cfg.TimeoutSweep.BudgetPerTick = 2
	release := make(chan struct{})
	sweeper := &recordingSweeper{blockOn: release}
	manager := managerWithSweeper(t, sweeper, &cfg)
	manager.sweepTimeouts(context.Background())
	require.Eventually(t, func() bool { return sweeper.callCount() == 1 }, time.Second, time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		manager.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop() returned while a sweep was still voting")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-stopped
}

func settlingManager(t *testing.T, sweeper *recordingSweeper, budget int64) (*Manager, *fakeSettlementSource, *callLog, store.DevshardRecord) {
	t.Helper()
	cfg := config.Defaults()
	cfg.TimeoutSweep.BudgetPerTick = budget
	testStore := newFakeStore()
	record := store.DevshardRecord{EscrowID: "2", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	testStore.devshards[record.EscrowID] = record
	log := &callLog{}
	sweeper.settleLog = log
	settlementSource := &fakeSettlementSource{calls: log, buildInput: chain.SettlementInput{EscrowID: 2}}
	txClient := &fakeTxClient{
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			return chain.SettleEscrowResult{EscrowID: input.EscrowID}, nil
		},
	}
	deps := testManagerDeps(t, testStore, txClient, &fakeSnapshotSource{}, &cfg)
	deps.Settlement = settlementSource
	deps.Timeouts = sweeper
	return mustManager(t, deps), settlementSource, log, record
}

// Test flow:
//  1. Configure a sweep budget of 3 and a sweeper that applies one vote on the escrow being settled.
//  2. Settle the escrow without force.
//  3. Assert the settle succeeded, swept escrow 2 with the configured budget, and swept before it finalized.
func TestSettleVotesDueTimeoutsBeforeFinalizing(t *testing.T) {
	sweeper := &recordingSweeper{settleSwept: [3]int{1, 1, 0}}
	manager, _, log, record := settlingManager(t, sweeper, 3)

	_, err := manager.settle(context.Background(), record, false)

	require.NoError(t, err)
	escrowID, budget := sweeper.settleSweptWith()
	require.Equal(t, "2", escrowID)
	require.Equal(t, 3, budget)
	calls := log.snapshot()
	require.Less(t, slices.Index(calls, "SweepBeforeFinalize"), slices.Index(calls, "Finalize"))
	require.NotEqual(t, -1, slices.Index(calls, "SweepBeforeFinalize"))
}

// Test flow:
//  1. Table-driven: with a sweep budget of 3, a settle-time sweep that found the whole budget due with every vote applied, with one vote failed, with every vote failed, one that found less than the budget, and one that found less with a vote failed.
//  2. Settle the escrow without force on one manager and with force on another.
//  3. Assert the unforced settle defers as busy without finalizing while a full budget was due and a vote landed or while any vote failed, and the forced one always finalizes.
func TestAnUnforcedSettleWaitsWhileTheSweepSpentItsWholeBudget(t *testing.T) {
	testCases := []struct {
		name       string
		swept      [3]int
		wantDefers bool
	}{
		{name: "full budget applied", swept: [3]int{3, 3, 0}, wantDefers: true},
		{name: "full budget with a failed vote", swept: [3]int{3, 2, 1}, wantDefers: true},
		{name: "full budget all failed", swept: [3]int{3, 0, 3}, wantDefers: true},
		{name: "less than the budget due", swept: [3]int{2, 2, 0}, wantDefers: false},
		{name: "less than the budget with a failed vote", swept: [3]int{2, 1, 1}, wantDefers: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			unforced, _, unforcedLog, unforcedRecord := settlingManager(t, &recordingSweeper{settleSwept: testCase.swept}, 3)
			forced, _, forcedLog, forcedRecord := settlingManager(t, &recordingSweeper{settleSwept: testCase.swept}, 3)

			_, unforcedErr := unforced.settle(context.Background(), unforcedRecord, false)
			_, forcedErr := forced.settle(context.Background(), forcedRecord, true)

			if testCase.wantDefers {
				require.ErrorIs(t, unforcedErr, ErrDevshardBusy)
				require.NotContains(t, unforcedLog.snapshot(), "Finalize")
			} else {
				require.NoError(t, unforcedErr)
				require.Contains(t, unforcedLog.snapshot(), "Finalize")
			}
			require.NoError(t, forcedErr)
			require.Contains(t, forcedLog.snapshot(), "Finalize")
		})
	}
}

// Test flow:
//  1. Configure a sweep budget of 3, a settle-time sweep whose only due vote fails every time, and a clock the test moves.
//  2. Settle the escrow without force once a tick for three ticks, all within failedSweepRetryWindow of the first failure; assert each defers as busy without finalizing.
//  3. Move the clock to the end of that window and settle again; assert it finalizes and settles.
func TestAnUnforcedSettleRetriesAFailedVoteForABoundedTime(t *testing.T) {
	manager, _, log, record := settlingManager(t, &recordingSweeper{settleSwept: [3]int{1, 0, 1}}, 3)
	firstFailure := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	clock := &testClock{now: firstFailure}
	manager.now = clock.read

	for range 3 {
		_, err := manager.settle(context.Background(), record, false)
		require.ErrorIs(t, err, ErrDevshardBusy)
		clock.now = clock.now.Add(TickInterval)
	}
	require.NotContains(t, log.snapshot(), "Finalize")
	clock.now = firstFailure.Add(failedSweepRetryWindow)
	_, err := manager.settle(context.Background(), record, false)

	require.NoError(t, err)
	require.Contains(t, log.snapshot(), "Finalize")
}

// Test flow:
//  1. Configure a sweep budget of 3 and a settle-time sweep that finds one vote due and is cancelled before it lands one, as shutdown does.
//  2. Settle the escrow with force on that context.
//  3. Assert the settle returns the context's error and never finalizes.
func TestASettleWhoseSweepWasCancelledDoesNotFinalize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sweeper := &recordingSweeper{settleSwept: [3]int{1, 0, 0}, onSettle: cancel}
	manager, _, log, record := settlingManager(t, sweeper, 3)

	_, err := manager.settle(ctx, record, true)

	require.ErrorIs(t, err, context.Canceled)
	require.NotContains(t, log.snapshot(), "Finalize")
}

// Test flow:
//  1. Configure a sweeper whose settle-time sweep fails to open the escrow's session.
//  2. Settle the escrow with force.
//  3. Assert the settle fails with the sweep's error and never finalizes.
func TestASettleWhoseSweepFailsDoesNotFinalize(t *testing.T) {
	openFailure := errors.New("session unavailable")
	sweeper := &recordingSweeper{settleErr: openFailure}
	manager, _, log, record := settlingManager(t, sweeper, 3)

	_, err := manager.settle(context.Background(), record, true)

	require.ErrorIs(t, err, openFailure)
	require.NotContains(t, log.snapshot(), "Finalize")
}

// Test flow:
//  1. Configure a sweep budget of 0, which turns the sweep off.
//  2. Settle the escrow without force.
//  3. Assert the settle succeeded without a settle-time sweep.
func TestSettleSkipsTheSweepWithoutABudget(t *testing.T) {
	sweeper := &recordingSweeper{settleSwept: [3]int{1, 1, 0}}
	manager, _, log, record := settlingManager(t, sweeper, 0)

	_, err := manager.settle(context.Background(), record, false)

	require.NoError(t, err)
	require.NotContains(t, log.snapshot(), "SweepBeforeFinalize")
}
