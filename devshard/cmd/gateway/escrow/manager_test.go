package escrow

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/internal/leakcheck"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
	"devshard/signing"
)

type fakeSnapshotSource struct {
	snapshot chain.PhaseSnapshot
}

func (f *fakeSnapshotSource) Snapshot() chain.PhaseSnapshot { return f.snapshot }

// blockingSnapshotSource blocks Snapshot() until release is closed, signaling started first.
type blockingSnapshotSource struct {
	started  chan struct{}
	release  chan struct{}
	snapshot chain.PhaseSnapshot
}

func (f *blockingSnapshotSource) Snapshot() chain.PhaseSnapshot {
	close(f.started)
	<-f.release
	return f.snapshot
}

func hasCall(calls []string, name string) bool {
	return slices.Contains(calls, name)
}

func testManagerDeps(t *testing.T, testStore *fakeStore, txClient *fakeTxClient, snapshots snapshotSource, cfg *config.Config) Deps {
	t.Helper()
	return Deps{
		Tx:         txClient,
		Store:      testStore,
		Snapshots:  snapshots,
		Settlement: &fakeSettlementSource{},
		Signer:     &fakeSignerSource{signer: testSigner(t)},
		Config:     config.NewHolder(cfg),
		Now:        time.Now,
	}
}

// Test flow:
//  1. Build a manager with rotation disabled.
//  2. Call tick and assert it returns no error.
//  3. Assert the call log contains "LoadCommitments" (reconcile still ran).
//  4. Assert the call log contains no "SaveRotationStatus" (no rotation work happened).
func TestTickReconcileRunsEvenWhenRotationDisabled(t *testing.T) {
	testStore := newFakeStore()
	log := &callLog{}
	testStore.calls = log
	cfg := config.Defaults()
	cfg.Rotation.Enabled = false
	m := mustManager(t, testManagerDeps(t, testStore, &fakeTxClient{}, &fakeSnapshotSource{}, &cfg))

	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("tick(): %v", err)
	}

	calls := log.snapshot()
	if !hasCall(calls, "LoadCommitments") {
		t.Fatalf("calls = %v, want LoadCommitments (reconcile must run even when rotation is disabled)", calls)
	}
	if hasCall(calls, "SaveRotationStatus") {
		t.Fatalf("calls = %v, want no rotation work while rotation is disabled", calls)
	}
}

// Test flow:
//  1. Build a manager with rotation enabled and a snapshot that is a cold start, varied across cases: zero epoch index, zero block height.
//  2. Call tick and assert it returns no error.
//  3. Assert the call log contains no "SaveRotationStatus", since rotation must skip until the chain snapshot is populated.
func TestTickColdStartReturnsAfterReconcile(t *testing.T) {
	tests := []struct {
		name     string
		snapshot chain.PhaseSnapshot
	}{
		{name: "zero epoch index", snapshot: chain.PhaseSnapshot{EpochIndex: 0, BlockHeight: 500}},
		{name: "zero block height", snapshot: chain.PhaseSnapshot{EpochIndex: 5, BlockHeight: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			testStore := newFakeStore()
			log := &callLog{}
			testStore.calls = log
			cfg := config.Defaults()
			cfg.Rotation.Enabled = true
			m := mustManager(t, testManagerDeps(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, &fakeSnapshotSource{snapshot: tt.snapshot}, &cfg))

			if err := m.tick(context.Background()); err != nil {
				t.Fatalf("tick(): %v", err)
			}
			if hasCall(log.snapshot(), "SaveRotationStatus") {
				t.Fatal("rotation ran on cold start, want it skipped until the chain snapshot is populated")
			}
		})
	}
}

// Test flow:
//  1. Build a manager with rotation enabled and an invalid ModelsJSON config string.
//  2. Call tick.
//  3. Assert tick returns an error surfacing the bad rotation config.
func TestTickInvalidModelsJSONSurfacesErrorAlongsideReconcile(t *testing.T) {
	testStore := newFakeStore()
	cfg := config.Defaults()
	cfg.Rotation.Enabled = true
	cfg.Rotation.ModelsJSON = "not valid json"
	m := mustManager(t, testManagerDeps(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, &fakeSnapshotSource{}, &cfg))

	if err := m.tick(context.Background()); err == nil {
		t.Fatal("tick() = nil, want the bad rotation config surfaced")
	}
}

// Test flow:
//  1. Build a manager with rotation enabled and a store whose ListDevshards always fails.
//  2. Call tick.
//  3. Assert tick returns an error surfacing the ListDevshards failure.
func TestTickListDevshardsFailureSurfacesErrorAlongsideReconcile(t *testing.T) {
	testStore := newFakeStore()
	testStore.listDevshardsErr = errors.New("store unavailable")
	cfg := config.Defaults()
	cfg.Rotation.Enabled = true
	snapshot := chain.PhaseSnapshot{EpochIndex: 5, BlockHeight: 100}
	m := mustManager(t, testManagerDeps(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, &fakeSnapshotSource{snapshot: snapshot}, &cfg))

	if err := m.tick(context.Background()); err == nil {
		t.Fatal("tick() = nil, want the ListDevshards failure surfaced")
	}
}

// Test flow:
//  1. Build a manager with rotation enabled and a snapshot inside the pre-PoC window while RequestsBlocked is false (PoC not active).
//  2. Call tick and assert it returns no error.
//  3. Assert a temp escrow was created for the model (prepareBridge ran, the pre-PoC window wins even though PoC is inactive).
//  4. Assert no regular escrow was created.
func TestTickPrePoCWindowRunsPrepareBridgeEvenWhenPoCInactive(t *testing.T) {
	testStore := newFakeStore()
	txClient := &fakeTxClient{createEscrowFn: succeedingCreateEscrowFn(700)}
	cfg := config.Defaults()
	cfg.Rotation.Enabled = true
	cfg.Rotation.ModelsJSON = `[{"model_id":"model-a","temp_count":1,"target_count":1,"amount":1000,"private_key_env":"MODEL_A_KEY"}]`
	snapshot := chain.PhaseSnapshot{
		EpochIndex: 9, BlockHeight: 500, EpochSwitchBlockHeight: 500 + cfg.Rotation.PrePoCBlocks/2,
		RequestsBlocked:    false,
		FullWeightsByModel: map[string]map[string]float64{"model-a": {"p": 1}},
	}
	m := mustManager(t, testManagerDeps(t, testStore, txClient, &fakeSnapshotSource{snapshot: snapshot}, &cfg))

	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("tick(): %v", err)
	}

	tempCreated, regularCreated := false, false
	for _, record := range testStore.devshards {
		if record.Model != "model-a" {
			continue
		}
		switch record.RotationRole {
		case roleTemp:
			tempCreated = true
		case roleRegular:
			regularCreated = true
		}
	}
	if !tempCreated {
		t.Fatal("no temp escrow created, want prepareBridge to run inside the pre-PoC window")
	}
	if regularCreated {
		t.Fatal("a regular escrow was created, want only prepareBridge (temp) to run in the pre-PoC window")
	}
}

// Test flow:
//  1. Build a manager and call Start twice in a row.
//  2. Call Stop twice in a row.
//  3. Assert no goroutine leak, which would show if the second Start overwrote and orphaned the first goroutine's stop/done channels, and that neither call blocks or panics.
func TestStartStopIdempotent(t *testing.T) {
	defer leakcheck.VerifyNone(t)

	cfg := config.Defaults()
	m := mustManager(t, testManagerDeps(t, newFakeStore(), &fakeTxClient{}, &fakeSnapshotSource{}, &cfg))

	ctx := context.Background()
	m.Start(ctx)
	m.Start(ctx)
	m.Stop()
	m.Stop()
}

// Test flow:
//  1. Build a manager with rotation enabled (so tick reaches the snapshot fetch) and a `blockingSnapshotSource`.
//  2. Call Start and wait for the immediate first tick to block inside Snapshot().
//  3. Call Stop in a goroutine and assert it has not returned after 100ms, while the tick is still blocked.
//  4. Release the block and assert Stop then returns within 2 seconds.
func TestStopBlocksUntilInFlightTickExits(t *testing.T) {
	defer leakcheck.VerifyNone(t)

	started := make(chan struct{})
	release := make(chan struct{})
	cfg := config.Defaults()
	cfg.Rotation.Enabled = true
	m := mustManager(t, testManagerDeps(t, newFakeStore(), &fakeTxClient{}, &blockingSnapshotSource{started: started, release: release}, &cfg))

	m.Start(context.Background())
	<-started

	stopReturned := make(chan struct{})
	go func() {
		m.Stop()
		close(stopReturned)
	}()

	select {
	case <-stopReturned:
		t.Fatal("Stop() returned before the in-flight tick finished, want it to block")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case <-stopReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return after the in-flight tick finished")
	}
}

// Test flow:
//  1. Build a manager, call Start with a cancelable context, and capture m.done.
//  2. Cancel the context without ever calling Stop.
//  3. Assert m.done closes within 2 seconds, proving the tick goroutine terminates on context cancel alone.
func TestContextCancelAloneStopsTickGoroutine(t *testing.T) {
	defer leakcheck.VerifyNone(t)

	cfg := config.Defaults()
	m := mustManager(t, testManagerDeps(t, newFakeStore(), &fakeTxClient{}, &fakeSnapshotSource{}, &cfg))

	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	done := m.done
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tick goroutine did not exit within 2s of context cancel")
	}
}

// Test flow:
//  1. Build a manager and launch 10 pairs of goroutines each calling Start and Stop concurrently.
//  2. Wait for all goroutines to finish.
//  3. Call Stop once more and assert it completes cleanly, whichever Start last won the race.
func TestStartStopConcurrentCallsAreRaceFree(t *testing.T) {
	defer leakcheck.VerifyNone(t)

	cfg := config.Defaults()
	m := mustManager(t, testManagerDeps(t, newFakeStore(), &fakeTxClient{}, &fakeSnapshotSource{}, &cfg))

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(2)
		go func() { defer wg.Done(); m.Start(context.Background()) }()
		go func() { defer wg.Done(); m.Stop() }()
	}
	wg.Wait()
	m.Stop()
}

// Test flow:
//  1. Store a parked record whose settlement blocks until released, with settlement enabled.
//  2. Run one lifecycle tick and assert it returns while the settlement is still in flight.
//  3. Release the settlement, wait for the work the tick started, and assert the record was settled and dropped.
func TestASlowSettlementDoesNotHoldTheTick(t *testing.T) {
	testStore := newFakeStore()
	record := parkedRecord("42")
	testStore.devshards[record.EscrowID] = record
	cfg := config.Defaults()
	cfg.Rotation.SettlementEnabled = true
	release := make(chan struct{})
	txClient := &fakeTxClient{
		settleEscrowFn: func(ctx context.Context, _ *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			<-release
			return chain.SettleEscrowResult{EscrowID: input.EscrowID}, nil
		},
	}
	m := mustManager(t, testManagerDeps(t, testStore, txClient, &fakeSnapshotSource{}, &cfg))

	ticked := make(chan error, 1)
	go func() { ticked <- m.tickWith(context.Background(), true) }()
	select {
	case <-ticked:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the tick waited on a settlement: a slow finalize holds retirement and funding for every other escrow")
	}
	close(release)
	m.offTickWork.Wait()

	testStore.mu.Lock()
	_, survived := testStore.devshards[record.EscrowID]
	testStore.mu.Unlock()
	if survived {
		t.Fatal("parked escrow survived its settlement; nothing else will ever settle it")
	}
}

// Test flow:
//  1. Store a parked record and build a manager with rotation disabled but settlement enabled, and a `settlingTxClient`.
//  2. Call tick and assert it returns no error.
//  3. Assert the parked record is gone from the store, since settlement must not depend on the rotation toggle.
func TestTickSettlesParkedEscrowWhileRotationDisabled(t *testing.T) {
	testStore := newFakeStore()
	record := parkedRecord("42")
	testStore.devshards[record.EscrowID] = record
	cfg := config.Defaults()
	cfg.Rotation.Enabled = false
	cfg.Rotation.SettlementEnabled = true

	m := mustManager(t, testManagerDeps(t, testStore, settlingTxClient(), &fakeSnapshotSource{}, &cfg))

	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("tick(): %v", err)
	}
	if _, ok := testStore.devshards[record.EscrowID]; ok {
		t.Fatal("parked escrow survived the tick; nothing else will ever settle it")
	}
}

// Test flow:
//  1. Store one active regular record for a model, mark it at its nonce cap, and build a manager with rotation disabled.
//  2. Call tick and assert it returns no error.
//  3. Assert the record is now parked, since an exhausted escrow left active would otherwise attract and fail traffic.
func TestTickParksDepletedEscrowWhileRotationDisabled(t *testing.T) {
	testStore := newFakeStore()
	depleted := store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true, RotationRole: roleRegular, PrivateKeyEnv: "MODEL_A_KEY"}
	testStore.devshards[depleted.EscrowID] = depleted
	cfg := config.Defaults()
	cfg.Rotation.Enabled = false
	cfg.Rotation.ModelsJSON = `[{"model_id":"model-a","target_count":1,"amount":1000,"private_key_env":"MODEL_A_KEY"}]`
	manager := mustManager(t, testManagerDeps(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, &fakeSnapshotSource{}, &cfg))
	manager.OnBalanceExhausted("1", scheduler.ExhaustionNonceCap)

	if err := manager.tick(context.Background()); err != nil {
		t.Fatalf("tick(): %v", err)
	}

	assertParked(t, testStore, "1")
}

// Test flow:
//  1. Build a manager with rotation enabled and a `blockingSnapshotSource`, then Start it and wait for the tick to block inside Snapshot.
//  2. Launch two concurrent Stop calls.
//  3. Assert neither has returned after 100ms, while the tick is still running.
//  4. Release the block, wait for both Stop calls, and assert both returned.
func TestManagerStopIsABarrierForConcurrentCallers(t *testing.T) {
	defer leakcheck.VerifyNone(t)
	testStore := newFakeStore()
	blocking := &blockingSnapshotSource{started: make(chan struct{}), release: make(chan struct{})}
	cfg := config.Defaults()
	cfg.Rotation.Enabled = true
	m := mustManager(t, testManagerDeps(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, blocking, &cfg))

	m.Start(context.Background())
	<-blocking.started

	returned := make(chan struct{}, 2)
	var stoppers sync.WaitGroup
	for range 2 {
		stoppers.Go(func() {
			m.Stop()
			returned <- struct{}{}
		})
	}

	select {
	case <-returned:
		t.Fatal("Stop returned while a tick was still running")
	case <-time.After(100 * time.Millisecond):
	}

	close(blocking.release)
	stoppers.Wait()
	if len(returned) != 2 {
		t.Fatalf("%d of 2 Stop calls returned", len(returned))
	}
}

// mustManager builds a manager the way the composition root does, failing the test rather than the tick.
func mustManager(t *testing.T, deps Deps) *Manager {
	t.Helper()
	manager, err := NewManager(deps)
	if err != nil {
		t.Fatalf("NewManager() = %v, want a wired manager", err)
	}
	return manager
}

// Test flow:
//  1. Store a serving regular escrow that no request has reported, with rotation off and a probe reading it at its nonce cap.
//  2. Call tick.
//  3. Assert the escrow is parked in that same tick: markSpent runs before the planner drains the marks.
func TestTickParksASpentEscrowNoRequestReported(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true, RotationRole: roleRegular, PrivateKeyEnv: "MODEL_A_KEY"}
	cfg := config.Defaults()
	cfg.Rotation.Enabled = false
	deps := testManagerDeps(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, &fakeSnapshotSource{}, &cfg)
	deps.Exhaustion = fakeExhaustionProbe{"1": scheduler.ExhaustionNonceCap}
	manager := mustManager(t, deps)

	if err := manager.tick(context.Background()); err != nil {
		t.Fatalf("tick(): %v", err)
	}

	assertParked(t, testStore, "1")
}

// Test flow:
//  1. Store one serving regular escrow, mark it for its balance floor, and build a manager with rotation disabled.
//  2. Call tick.
//  3. Assert the escrow still serves and nothing was created: a balance mark parks nothing.
func TestABalanceMarkWithRotationOffParksNothing(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true, RotationRole: roleRegular, PrivateKeyEnv: "MODEL_A_KEY"}
	cfg := config.Defaults()
	cfg.Rotation.Enabled = false
	cfg.Rotation.ModelsJSON = `[{"model_id":"model-a","target_count":1,"amount":1000,"private_key_env":"MODEL_A_KEY"}]`
	manager := mustManager(t, testManagerDeps(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, &fakeSnapshotSource{}, &cfg))
	manager.OnBalanceExhausted("1", scheduler.ExhaustionBalanceFloor)

	if err := manager.tick(context.Background()); err != nil {
		t.Fatalf("tick() = %v, want nil", err)
	}

	if record := testStore.devshards["1"]; !record.Active || record.SettlementPending {
		t.Fatalf("record = %+v, want still serving", record)
	}
}
