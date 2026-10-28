package escrow

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
	"devshard/signing"
)

func (m *Manager) settlePending(ctx context.Context, snapshot chain.PhaseSnapshot, devshards []store.DevshardRecord) error {
	return m.settleParked(ctx, m.parkedToSettle(snapshot, devshards))
}

// callLog records call names in the order they happen, shared across fakes.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (c *callLog) record(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, name)
}

func (c *callLog) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// fakeSettlementSource models the registry that routing and nonce commits go through.
type fakeSettlementSource struct {
	mu        sync.Mutex
	retired   bool
	committed int

	busy        bool
	retireErr   error
	finalizeErr error
	buildErr    error
	buildInput  chain.SettlementInput
	calls       *callLog
}

func (f *fakeSettlementSource) Retire(escrowID string) error {
	if f.calls != nil {
		f.calls.record("Retire")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retired = true
	return f.retireErr
}

func (f *fakeSettlementSource) commit() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.retired {
		return false
	}
	f.committed++
	return true
}

func (f *fakeSettlementSource) IsBusy(escrowID string) bool {
	if f.calls != nil {
		f.calls.record("IsBusy")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busy || f.committed > 0
}

func (f *fakeSettlementSource) Finalize(ctx context.Context, escrowID string) error {
	if f.calls != nil {
		f.calls.record("Finalize")
	}
	return f.finalizeErr
}

func (f *fakeSettlementSource) BuildSettlement(ctx context.Context, escrowID string) (chain.SettlementInput, error) {
	if f.calls != nil {
		f.calls.record("BuildSettlement")
	}
	if f.buildErr != nil {
		return chain.SettlementInput{}, f.buildErr
	}
	return f.buildInput, nil
}

func holderWithSettlementEnabled(enabled bool) *config.Holder {
	cfg := config.Defaults()
	cfg.Rotation.SettlementEnabled = enabled
	return config.NewHolder(&cfg)
}

func stringsEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Test flow:
//  1. Seed the store with one active devshard record and a shared call log across the fake store, settlement source and chain client.
//  2. Configure the fake settlement source to report busy.
//  3. Call `settle` and assert it returns `ErrDevshardBusy`.
//  4. Assert the call log shows park and retire happening before the busy check short-circuits any chain call.
//  5. Assert the devshard ends up SettlementPending=true and Active=false.
func TestSettleBusyMarksPendingAndReturnsErrDevshardBusy(t *testing.T) {
	testStore := newFakeStore()
	record := store.DevshardRecord{EscrowID: "1", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	testStore.devshards[record.EscrowID] = record
	log := &callLog{}
	testStore.calls = log
	settlementSource := &fakeSettlementSource{busy: true, calls: log}
	txClient := &fakeTxClient{calls: log}

	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: settlementSource,
	}

	_, err := m.settle(context.Background(), record, false)
	if !errors.Is(err, ErrDevshardBusy) {
		t.Fatalf("settle() = %v, want ErrDevshardBusy", err)
	}

	want := []string{"ParkForSettlement", "Retire", "IsBusy"}
	if got := log.snapshot(); !stringsEqual(got, want) {
		t.Fatalf("call log = %v, want %v (park + stop routing, then busy short-circuits before any chain call)", got, want)
	}
	if got := testStore.devshards[record.EscrowID]; !got.SettlementPending || got.Active {
		t.Fatalf("devshard = %+v, want SettlementPending=true and Active=false (deactivated so it can drain)", got)
	}
}

// Test flow:
//  1. Seed the store with one active devshard record and a shared call log.
//  2. Configure the fake settlement source and chain client to succeed, recording the settle tx hash inside SettleEscrow before the broadcast completes.
//  3. Call `settle` and assert it returns nil.
//  4. Assert the call log shows the full ordered sequence: park, retire, busy check, chain lookup, finalize, build, broadcast, then hash and pending recorded.
//  5. Assert the devshard ends up Active=false and SettlementPending=false.
func TestSettleHappyPathOrderAndClearsPendingOnSuccess(t *testing.T) {
	testStore := newFakeStore()
	record := store.DevshardRecord{EscrowID: "2", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	testStore.devshards[record.EscrowID] = record
	log := &callLog{}
	testStore.calls = log
	settlementSource := &fakeSettlementSource{busy: false, calls: log, buildInput: chain.SettlementInput{EscrowID: 2}}
	txClient := &fakeTxClient{
		calls:        log,
		settleTxHash: "SETTLE-TX",
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			return chain.SettleEscrowResult{EscrowID: input.EscrowID}, nil
		},
	}

	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: settlementSource,
	}

	if _, err := m.settle(context.Background(), record, false); err != nil {
		t.Fatalf("settle() = %v, want nil", err)
	}

	want := []string{
		"ParkForSettlement", "Retire", "IsBusy", "GetEscrow",
		"Finalize", "BuildSettlement", "SettleEscrow", `SetDevshardSettleTxHash("SETTLE-TX")`,
		"SetDevshardSettlementPending(false)",
	}
	if got := log.snapshot(); !stringsEqual(got, want) {
		t.Fatalf("call log = %v, want %v", got, want)
	}
	got := testStore.devshards[record.EscrowID]
	if got.Active {
		t.Fatalf("devshard.Active = true, want false (deactivated)")
	}
	if got.SettlementPending {
		t.Fatalf("devshard.SettlementPending = true, want false (cleared on success)")
	}
}

// Test flow:
//  1. Seed the store with one active devshard record.
//  2. Configure the fake chain client's SettleEscrow to fail with a broadcast error.
//  3. Call `settle` and assert the error wraps the broadcast error.
//  4. Assert the devshard stays Active=false (already deactivated) with SettlementPending still true.
func TestSettleBroadcastFailureLeavesSettlementPendingSet(t *testing.T) {
	testStore := newFakeStore()
	record := store.DevshardRecord{EscrowID: "3", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	testStore.devshards[record.EscrowID] = record
	settlementSource := &fakeSettlementSource{busy: false}
	broadcastErr := errors.New("broadcast rejected")
	txClient := &fakeTxClient{
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			return chain.SettleEscrowResult{}, broadcastErr
		},
	}

	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: settlementSource,
	}

	_, err := m.settle(context.Background(), record, false)
	if err == nil || !errors.Is(err, broadcastErr) {
		t.Fatalf("settle() = %v, want wrapped %v", err, broadcastErr)
	}
	got := testStore.devshards[record.EscrowID]
	if !got.SettlementPending {
		t.Fatal("devshard.SettlementPending = false, want true (never cleared on failure)")
	}
	if got.Active {
		t.Fatal("devshard.Active = true, want false (deactivation already happened before the broadcast)")
	}
}

// Test flow:
//  1. Seed the store with one active devshard record.
//  2. Build a fake chain client that fails the test if SettleEscrow is ever called.
//  3. Configure the Manager with the settlement toggle disabled.
//  4. Call `retire` and assert it returns nil.
//  5. Assert the record ends up parked rather than deleted.
func TestRetireSettlementDisabledSkipsChainAndParksEscrow(t *testing.T) {
	testStore := newFakeStore()
	record := store.DevshardRecord{EscrowID: "4", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	testStore.devshards[record.EscrowID] = record
	txClient := &fakeTxClient{
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			t.Fatal("SettleEscrow must not be called when the settlement toggle is off")
			return chain.SettleEscrowResult{}, nil
		},
	}

	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config:           holderWithSettlementEnabled(false),
	}

	if err := m.retire(context.Background(), record); err != nil {
		t.Fatalf("retire() = %v, want nil", err)
	}
	assertParked(t, testStore, record.EscrowID)
}

// Test flow:
//  1. Seed the store with one active devshard record.
//  2. Configure the fake chain client to settle successfully with the settlement toggle enabled.
//  3. Call `retire` and assert it returns nil.
//  4. Assert the devshard row is deleted once the settlement completes.
func TestRetireSettlementEnabledHappyPathSettlesThenDeletes(t *testing.T) {
	testStore := newFakeStore()
	record := store.DevshardRecord{EscrowID: "5", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	testStore.devshards[record.EscrowID] = record
	settlementSource := &fakeSettlementSource{busy: false}
	txClient := &fakeTxClient{
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			return chain.SettleEscrowResult{EscrowID: input.EscrowID}, nil
		},
	}

	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: settlementSource,
		config:           holderWithSettlementEnabled(true),
	}

	if err := m.retire(context.Background(), record); err != nil {
		t.Fatalf("retire() = %v, want nil", err)
	}
	if _, ok := testStore.devshards[record.EscrowID]; ok {
		t.Fatal("devshard still present after retire(), want deleted once settled")
	}
}

// Test flow:
//  1. Seed the store with one active devshard record.
//  2. Configure the fake settlement source as busy and build a chain client that fails the test if SettleEscrow is called.
//  3. Call `retire` with the settlement toggle enabled and assert it returns `ErrDevshardBusy`.
//  4. Assert the devshard row stays registered with SettlementPending set for a later deferred-settle attempt.
func TestRetireSettlementEnabledBusyLeavesDevshardRegistered(t *testing.T) {
	testStore := newFakeStore()
	record := store.DevshardRecord{EscrowID: "6", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	testStore.devshards[record.EscrowID] = record
	settlementSource := &fakeSettlementSource{busy: true}
	txClient := &fakeTxClient{
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			t.Fatal("SettleEscrow must not be called while busy")
			return chain.SettleEscrowResult{}, nil
		},
	}

	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: settlementSource,
		config:           holderWithSettlementEnabled(true),
	}

	err := m.retire(context.Background(), record)
	if !errors.Is(err, ErrDevshardBusy) {
		t.Fatalf("retire() = %v, want ErrDevshardBusy", err)
	}
	got, ok := testStore.devshards[record.EscrowID]
	if !ok {
		t.Fatal("devshard deleted after a busy retire(), want it kept registered")
	}
	if !got.SettlementPending {
		t.Fatal("devshard.SettlementPending = false, want true (deferred-settle marker left set)")
	}
}

// Test flow:
//  1. Seed the store with one active devshard record and a chain client whose SettleEscrow blocks until released.
//  2. Start a first `settle` call in a goroutine and wait until it has entered the broadcast.
//  3. Call `settle` again concurrently for the same escrow and assert it returns `ErrSettlementInFlight`.
//  4. Release the blocked call, wait for it to finish, and assert the call log recorded exactly one SettleEscrow broadcast.
func TestSettleDedupesConcurrentCallsForSameEscrow(t *testing.T) {
	testStore := newFakeStore()
	record := store.DevshardRecord{EscrowID: "7", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	testStore.devshards[record.EscrowID] = record
	settlementSource := &fakeSettlementSource{busy: false}

	entered := make(chan struct{})
	release := make(chan struct{})
	log := &callLog{}
	txClient := &fakeTxClient{
		calls: log,
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			close(entered)
			<-release
			return chain.SettleEscrowResult{EscrowID: input.EscrowID}, nil
		},
	}

	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: settlementSource,
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := m.settle(context.Background(), record, false); err != nil {
			t.Errorf("first settle() = %v, want nil", err)
		}
	})

	<-entered
	if _, err := m.settle(context.Background(), record, false); !errors.Is(err, ErrSettlementInFlight) {
		t.Fatalf("second concurrent settle() = %v, want ErrSettlementInFlight", err)
	}
	close(release)
	wg.Wait()

	broadcastCount := 0
	for _, name := range log.snapshot() {
		if name == "SettleEscrow" {
			broadcastCount++
		}
	}
	if broadcastCount != 1 {
		t.Fatalf("SettleEscrow called %d times, want exactly 1", broadcastCount)
	}
}

// Test flow:
//  1. Seed the store with one active devshard record and a fake settlement source used as the routing registry.
//  2. Configure the chain client's SettleEscrow to attempt a nonce `commit` against that same routing registry mid-broadcast.
//  3. Call `settle` and assert it returns nil.
//  4. Assert the nonce commit attempted during the broadcast was refused, since the escrow was already retired from routing before the broadcast.
func TestSettleRefusesNonceCommitsOnceTheSettlementIsBroadcast(t *testing.T) {
	testStore := newFakeStore()
	record := store.DevshardRecord{EscrowID: "20", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	testStore.devshards[record.EscrowID] = record
	routing := &fakeSettlementSource{}
	committedDuringBroadcast := true
	txClient := &fakeTxClient{
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			committedDuringBroadcast = routing.commit()
			return chain.SettleEscrowResult{EscrowID: input.EscrowID}, nil
		},
	}

	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: routing,
	}

	if _, err := m.settle(context.Background(), record, false); err != nil {
		t.Fatalf("settle() = %v, want nil", err)
	}
	if committedDuringBroadcast {
		t.Fatal("a nonce was committed on an escrow whose settlement was already being broadcast")
	}
}

// Test flow:
//  1. Seed the store with one active devshard record and a chain client whose SettleEscrow blocks until released.
//  2. Start a first `retire` call in a goroutine and wait until it has entered the broadcast.
//  3. Call `retire` again for the same escrow while the first is still in flight, and assert it returns `ErrSettlementInFlight` while the row stays present with its private-key env preserved.
//  4. Release the blocked call, wait for it to finish, and assert the row is deleted once the real settlement completes.
func TestDedupedSettleLeavesTheRowForTheCallerThatIsReallySettling(t *testing.T) {
	testStore := newFakeStore()
	record := store.DevshardRecord{EscrowID: "21", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	testStore.devshards[record.EscrowID] = record

	entered := make(chan struct{})
	release := make(chan struct{})
	txClient := &fakeTxClient{
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			close(entered)
			<-release
			return chain.SettleEscrowResult{EscrowID: input.EscrowID}, nil
		},
	}
	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config:           holderWithSettlementEnabled(true),
	}

	var settling sync.WaitGroup
	settling.Go(func() {
		if err := m.retire(context.Background(), record); err != nil {
			t.Errorf("settling retire() = %v, want nil", err)
		}
	})

	<-entered
	dedupedErr := m.retire(context.Background(), record)
	kept, present := testStore.snapshotDevshard(record.EscrowID)
	if !present {
		t.Fatal("the deduped caller deleted the row while the real settlement was still in flight")
	}
	if kept.PrivateKeyEnv != record.PrivateKeyEnv {
		t.Fatalf("kept row = %+v, want the private-key env preserved", kept)
	}
	if !errors.Is(dedupedErr, ErrSettlementInFlight) {
		t.Fatalf("deduped retire() = %v, want ErrSettlementInFlight", dedupedErr)
	}

	close(release)
	settling.Wait()
	if _, stillPresent := testStore.snapshotDevshard(record.EscrowID); stillPresent {
		t.Fatal("row survived the settlement that actually completed, want it dropped")
	}
}

func parkedRecord(escrowID string) store.DevshardRecord {
	return store.DevshardRecord{EscrowID: escrowID, PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: false, SettlementPending: true}
}

func settlingTxClient() *fakeTxClient {
	return &fakeTxClient{
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			return chain.SettleEscrowResult{EscrowID: input.EscrowID}, nil
		},
	}
}

// Test flow:
//  1. Seed the store with one parked devshard record.
//  2. Call `settlePending` for that record with the settlement toggle enabled.
//  3. Assert it returns nil and the row is deleted after the confirmed settle.
func TestSettlePendingSettlesParkedEscrowAndDropsRow(t *testing.T) {
	testStore := newFakeStore()
	record := parkedRecord("9")
	testStore.devshards[record.EscrowID] = record

	m := &Manager{
		tx:               settlingTxClient(),
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config:           holderWithSettlementEnabled(true),
	}

	if err := m.settlePending(context.Background(), chain.PhaseSnapshot{}, []store.DevshardRecord{record}); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}
	if _, ok := testStore.devshards[record.EscrowID]; ok {
		t.Fatal("row still present after a confirmed settle, want it dropped")
	}
}

// Test flow:
//  1. Seed the store with one parked devshard record.
//  2. Upsert an unrelated copy of the record with SettlementPending cleared, mirroring how the real store still preserves the existing pending marker on upsert.
//  3. Load the updated records and call `settlePending` with them.
//  4. Assert it returns nil and the escrow is still settled and dropped, not stranded by the upsert.
func TestSettlePendingStillSettlesAfterAnUnrelatedUpsert(t *testing.T) {
	testStore := newFakeStore()
	record := parkedRecord("13")
	testStore.devshards[record.EscrowID] = record

	m := &Manager{
		tx:               settlingTxClient(),
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config:           holderWithSettlementEnabled(true),
	}

	reimported := record
	reimported.SettlementPending = false
	if err := testStore.UpsertDevshard(context.Background(), reimported); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	devshards, err := testStore.ListDevshards(context.Background())
	if err != nil {
		t.Fatalf("ListDevshards(): %v", err)
	}

	if err := m.settlePending(context.Background(), chain.PhaseSnapshot{}, devshards); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}
	if _, ok := testStore.snapshotDevshard(record.EscrowID); ok {
		t.Fatal("the escrow was not settled after an unrelated upsert; nothing else will ever pick it up")
	}
}

// Test flow:
//  1. Seed the store with one parked devshard record.
//  2. Configure the fake settlement source to report busy.
//  3. Call `settlePending` and assert it returns nil: busy defers the settle, it is no tick failure.
//  4. Assert the record stays parked for the next sweep.
func TestSettlePendingBusyEscrowStaysParkedForTheNextTick(t *testing.T) {
	testStore := newFakeStore()
	record := parkedRecord("10")
	testStore.devshards[record.EscrowID] = record

	m := &Manager{
		tx:               settlingTxClient(),
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{busy: true},
		config:           holderWithSettlementEnabled(true),
	}

	if err := m.settlePending(context.Background(), chain.PhaseSnapshot{}, []store.DevshardRecord{record}); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}
	assertParked(t, testStore, record.EscrowID)
}

type settleTxReadFailingStore struct {
	*fakeStore
	err error
}

func (s settleTxReadFailingStore) DevshardSettleTxHash(context.Context, string) (string, time.Time, error) {
	return "", time.Time{}, s.err
}

// Test flow:
//  1. Seed the store with one parked devshard record.
//  2. Configure the fake settlement source to fail Finalize.
//  3. Call `settlePending` and assert it returns the Finalize error.
//  4. Assert the record stays parked for the next sweep.
func TestSettlePendingRealFailureOfParkedEscrowIsReported(t *testing.T) {
	testStore := newFakeStore()
	record := parkedRecord("10")
	testStore.devshards[record.EscrowID] = record
	finalizeErr := errors.New("finalize rejected")

	m := &Manager{
		tx:               settlingTxClient(),
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{finalizeErr: finalizeErr},
		config:           holderWithSettlementEnabled(true),
	}

	err := m.settlePending(context.Background(), chain.PhaseSnapshot{}, []store.DevshardRecord{record})
	if !errors.Is(err, finalizeErr) {
		t.Fatalf("settlePending() = %v, want the Finalize error", err)
	}
	assertParked(t, testStore, record.EscrowID)
}

// Test flow:
//  1. Seed the store with one parked devshard record and make reading its settle tx fail.
//  2. Call `settlePending` and assert it returns the store error, naming escrow 10.
func TestSettlePendingFailureReadingTheSettleTxNamesTheEscrow(t *testing.T) {
	testStore := newFakeStore()
	record := parkedRecord("10")
	testStore.devshards[record.EscrowID] = record
	readErr := errors.New("settle tx read failed")

	m := &Manager{
		tx:               settlingTxClient(),
		store:            settleTxReadFailingStore{fakeStore: testStore, err: readErr},
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config:           holderWithSettlementEnabled(true),
	}

	err := m.settlePending(context.Background(), chain.PhaseSnapshot{}, []store.DevshardRecord{record})
	if !errors.Is(err, readErr) {
		t.Fatalf("settlePending() = %v, want the store error", err)
	}
	if !strings.Contains(err.Error(), "escrow 10") {
		t.Fatalf("settlePending() = %q, want it to name escrow 10", err)
	}
}

// Test flow:
//  1. Seed the store with one parked devshard record whose settle tx was broadcast just now and is not yet found on chain.
//  2. Call `settlePending` and assert it returns nil: a settle still in flight is deferred, not a tick failure.
//  3. Assert the record stays parked and its in-flight hash is kept.
func TestSettlePendingSettleInFlightIsDeferred(t *testing.T) {
	testStore := newFakeStore()
	record := parkedRecord("10")
	testStore.devshards[record.EscrowID] = record
	if err := testStore.SetDevshardSettleTxHash(context.Background(), record.EscrowID, "IN-FLIGHT"); err != nil {
		t.Fatalf("SetDevshardSettleTxHash(): %v", err)
	}
	txClient := settlingTxClient()
	txClient.txCommittedFn = func(string) (bool, error) { return false, chain.ErrTxNotFound }

	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config:           holderWithSettlementEnabled(true),
		now:              time.Now,
	}

	if err := m.settlePending(context.Background(), chain.PhaseSnapshot{}, []store.DevshardRecord{record}); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}
	assertParked(t, testStore, record.EscrowID)
	if got := testStore.devshards[record.EscrowID].SettleTxHash; got != "IN-FLIGHT" {
		t.Fatalf("recorded hash = %q, want the transaction still in flight kept", got)
	}
}

// Test flow:
//  1. Seed the store with one parked devshard record.
//  2. Build a fake chain client that fails the test if SettleEscrow is ever called.
//  3. Call `settlePending` with the settlement toggle disabled and assert it returns nil.
//  4. Assert the record stays parked untouched.
func TestSettlePendingIsNoOpWhileSettlementDisabled(t *testing.T) {
	testStore := newFakeStore()
	record := parkedRecord("11")
	testStore.devshards[record.EscrowID] = record
	txClient := &fakeTxClient{
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			t.Fatal("SettleEscrow must not be called while the settlement toggle is off")
			return chain.SettleEscrowResult{}, nil
		},
	}

	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config:           holderWithSettlementEnabled(false),
	}

	if err := m.settlePending(context.Background(), chain.PhaseSnapshot{}, []store.DevshardRecord{record}); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}
	assertParked(t, testStore, record.EscrowID)
}

// Test flow:
//  1. Seed the store with a devshard record that is inactive but has no settlement-pending marker.
//  2. Build a fake chain client that fails the test if SettleEscrow is ever called.
//  3. Call `settlePending` and assert it returns nil.
//  4. Assert the unmarked record is left in place, neither settled nor dropped.
func TestSettlePendingIgnoresInactiveEscrowWithoutPendingMarker(t *testing.T) {
	testStore := newFakeStore()
	record := store.DevshardRecord{EscrowID: "12", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: false, SettlementPending: false}
	testStore.devshards[record.EscrowID] = record
	txClient := &fakeTxClient{
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			t.Fatal("SettleEscrow called for an escrow that was never marked settlement-pending")
			return chain.SettleEscrowResult{}, nil
		},
	}

	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config:           holderWithSettlementEnabled(true),
	}

	if err := m.settlePending(context.Background(), chain.PhaseSnapshot{}, []store.DevshardRecord{record}); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}
	if _, ok := testStore.devshards[record.EscrowID]; !ok {
		t.Fatal("unmarked escrow was settled and dropped")
	}
}

// Test flow:
//  1. Seed the store with a devshard record that already carries a recorded settle tx hash.
//  2. Configure the fake chain client's TxCommitted to report that transaction as already on chain.
//  3. Call `settle` and assert it returns nil with the result's TxHash equal to the recorded hash.
//  4. Assert the call log shows no second SettleEscrow broadcast.
func TestASettleAlreadyOnChainIsReconciledInsteadOfRebroadcast(t *testing.T) {
	log := &callLog{}
	record := store.DevshardRecord{EscrowID: "7", Model: "model-a", SettlementPending: true, SettleTxHash: "SETTLE-TX"}
	testStore := newFakeStore()
	testStore.devshards[record.EscrowID] = record
	testStore.calls = log
	txClient := &fakeTxClient{
		calls:         log,
		txCommittedFn: func(string) (bool, error) { return true, nil },
	}
	m := &Manager{
		tx: txClient, store: testStore, signer: &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
	}

	result, err := m.settle(context.Background(), record, false)
	if err != nil {
		t.Fatalf("settle() = %v, want the recorded settlement recognised", err)
	}
	if result.TxHash != "SETTLE-TX" {
		t.Fatalf("TxHash = %q, want the transaction already on chain", result.TxHash)
	}
	for _, call := range log.snapshot() {
		if call == "SettleEscrow" {
			t.Fatal("a second settlement was broadcast for an escrow the chain had already settled")
		}
	}
}

// Test flow:
//  1. Seed the store with a devshard record whose recorded settle tx hash never landed on chain.
//  2. Configure the fake chain client's TxCommitted to report the hash as not found, and SettleEscrow to broadcast a fresh transaction.
//  3. Call `settle` and assert it returns nil.
//  4. Assert the store now records the fresh transaction hash in place of the stale one.
func TestASettleThatNeverLandedIsRetried(t *testing.T) {
	record := store.DevshardRecord{EscrowID: "7", Model: "model-a", SettlementPending: true, SettleTxHash: "GONE"}
	testStore := newFakeStore()
	testStore.devshards[record.EscrowID] = record
	txClient := &fakeTxClient{
		settleTxHash:  "FRESH-TX",
		txCommittedFn: func(string) (bool, error) { return false, chain.ErrTxNotFound },
		settleEscrowFn: func(_ context.Context, _ *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			return chain.SettleEscrowResult{EscrowID: input.EscrowID}, nil
		},
	}
	m := &Manager{
		tx: txClient, store: testStore, signer: &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
	}

	if _, err := m.settle(context.Background(), record, false); err != nil {
		t.Fatalf("settle() = %v, want a fresh settlement", err)
	}

	if got := testStore.devshards["7"].SettleTxHash; got != "FRESH-TX" {
		t.Fatalf("recorded hash = %q, want the new transaction's", got)
	}
}

// Test flow:
//  1. Seed the store with a devshard record that carries a recorded settle tx hash.
//  2. Build a stale copy of that record with the hash cleared, simulating a caller holding an older snapshot from earlier in the same tick.
//  3. Configure the fake chain client's TxCommitted to report the row's actual hash as already on chain.
//  4. Call `settle` with the stale copy and assert it reads the hash from the store's row, returning nil with the recorded hash and no second broadcast.
func TestASettleReadsTheHashFromTheRowNotTheCopyItWasHanded(t *testing.T) {
	log := &callLog{}
	stored := store.DevshardRecord{EscrowID: "7", Model: "model-a", SettlementPending: true, SettleTxHash: "SETTLE-TX"}
	testStore := newFakeStore()
	testStore.devshards[stored.EscrowID] = stored
	testStore.calls = log
	txClient := &fakeTxClient{calls: log, txCommittedFn: func(string) (bool, error) { return true, nil }}
	m := &Manager{
		tx: txClient, store: testStore, signer: &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
	}
	stale := stored
	stale.SettleTxHash = ""

	result, err := m.settle(context.Background(), stale, false)

	if err != nil {
		t.Fatalf("settle() = %v, want the settle already on chain to be reconciled", err)
	}
	if result.TxHash != "SETTLE-TX" {
		t.Errorf("TxHash = %q, want the hash the row holds", result.TxHash)
	}
	if slices.Contains(log.snapshot(), "SettleEscrow") {
		t.Error("a second settlement was broadcast for an escrow already settled")
	}
}

// Test flow:
//  1. Seed the store with a devshard record and record an in-flight settle tx hash still within its TTL.
//  2. Configure the fake chain client's TxCommitted to report that hash as not yet found on chain.
//  3. Call `settle` and assert it returns `ErrSettlementInFlight`.
//  4. Assert the recorded hash stays the in-flight one, not rebroadcast.
func TestASettleStillWithinItsTTLIsNotRebroadcast(t *testing.T) {
	record := store.DevshardRecord{EscrowID: "7", Model: "model-a", SettlementPending: true}
	testStore := newFakeStore()
	testStore.devshards[record.EscrowID] = record
	if err := testStore.SetDevshardSettleTxHash(context.Background(), "7", "IN-FLIGHT"); err != nil {
		t.Fatalf("SetDevshardSettleTxHash(): %v", err)
	}
	txClient := &fakeTxClient{
		settleTxHash:  "SECOND-TX",
		txCommittedFn: func(string) (bool, error) { return false, chain.ErrTxNotFound },
	}
	m := &Manager{
		tx: txClient, store: testStore, signer: &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{}, now: time.Now,
	}

	_, err := m.settle(context.Background(), record, false)

	if !errors.Is(err, ErrSettlementInFlight) {
		t.Fatalf("settle() = %v, want the settle still on its way to be left alone", err)
	}
	if got := testStore.devshards["7"].SettleTxHash; got != "IN-FLIGHT" {
		t.Errorf("recorded hash = %q, want the transaction still in flight kept", got)
	}
}

// Test flow:
//  1. Seed the store with a devshard record whose settlement is already confirmed on chain.
//  2. Configure the fake chain client's TxCommitted to report it as already settled.
//  3. Call `settle` and assert it returns nil.
//  4. Assert the call log recorded a Retire call, since the caller deletes the row next and a row that is gone can no longer un-publish the escrow from routing.
func TestAnAlreadySettledEscrowIsStillTakenOutOfRouting(t *testing.T) {
	log := &callLog{}
	record := store.DevshardRecord{EscrowID: "7", Model: "model-a", SettlementPending: true, SettleTxHash: "SETTLE-TX"}
	testStore := newFakeStore()
	testStore.devshards[record.EscrowID] = record
	testStore.calls = log
	source := &fakeSettlementSource{calls: log}
	m := &Manager{
		tx:     &fakeTxClient{calls: log, txCommittedFn: func(string) (bool, error) { return true, nil }},
		store:  testStore,
		signer: &fakeSignerSource{signer: testSigner(t)}, settlementSource: source, now: time.Now,
	}

	if _, err := m.settle(context.Background(), record, false); err != nil {
		t.Fatalf("settle() = %v, want the settle already on chain reconciled", err)
	}

	if !slices.Contains(log.snapshot(), "Retire") {
		t.Error("the escrow was reconciled while still routable")
	}
}

func (c *callLog) contains(call string) bool {
	return slices.Contains(c.snapshot(), call)
}

func prunedEpochSnapshot() chain.PhaseSnapshot {
	return chain.PhaseSnapshot{EpochIndex: 53, EffectiveEpochIndex: 52, BlockHeight: 1_000}
}

func prunedTxClient(t *testing.T) *fakeTxClient {
	t.Helper()
	return &fakeTxClient{
		getEscrowFn: func(context.Context, string) (chain.EscrowInfo, bool, error) { return chain.EscrowInfo{}, false, nil },
		settleEscrowFn: func(context.Context, *signing.Secp256k1Signer, chain.SettlementInput) (chain.SettleEscrowResult, error) {
			t.Fatal("SettleEscrow must not be broadcast for an escrow the chain no longer holds")
			return chain.SettleEscrowResult{}, nil
		},
	}
}

// Test flow:
//  1. Seed the store with one parked record created in chain epoch 50, whose escrow the chain no longer holds at effective epoch 52.
//  2. Settle it through the admin entry.
//  3. Assert it reports ErrEscrowPruned without finalizing or broadcasting, and the row is dropped.
func TestSettleDropsAnEscrowTheChainNoLongerHolds(t *testing.T) {
	testStore := newFakeStore()
	record := parkedRecord("21")
	record.ChainEpoch = 50
	testStore.devshards[record.EscrowID] = record
	calls := &callLog{}
	m := &Manager{
		tx:               prunedTxClient(t),
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{calls: calls},
		snapshots:        &fakeSnapshotSource{snapshot: prunedEpochSnapshot()},
		config:           holderWithSettlementEnabled(true),
	}

	_, err := m.Settle(context.Background(), record.EscrowID, false)

	if !errors.Is(err, ErrEscrowPruned) {
		t.Fatalf("Settle() = %v, want ErrEscrowPruned", err)
	}
	if calls.contains("Finalize") {
		t.Error("finalized an escrow the chain no longer holds")
	}
	if _, ok := testStore.snapshotDevshard(record.EscrowID); ok {
		t.Fatal("row still present for an escrow the chain pruned, want it dropped")
	}
}

// Test flow:
//  1. Seed the store with one parked record created in chain epoch 50, whose escrow the chain no longer holds at effective epoch 52.
//  2. Run `settlePending` with the settlement toggle enabled.
//  3. Assert it returns nil and the row is dropped, so the tick stops retrying it.
func TestSettlePendingDropsAnEscrowTheChainNoLongerHolds(t *testing.T) {
	testStore := newFakeStore()
	record := parkedRecord("22")
	record.ChainEpoch = 50
	testStore.devshards[record.EscrowID] = record
	m := &Manager{
		tx:               prunedTxClient(t),
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		snapshots:        &fakeSnapshotSource{snapshot: prunedEpochSnapshot()},
		config:           holderWithSettlementEnabled(true),
	}

	if err := m.settlePending(context.Background(), chain.PhaseSnapshot{}, []store.DevshardRecord{record}); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}
	if _, ok := testStore.snapshotDevshard(record.EscrowID); ok {
		t.Fatal("row still present for an escrow the chain pruned, want it dropped")
	}
}

// Test flow:
//  1. Seed the store with a parked record created in chain epoch 50, while the chain's effective epoch is still 50 or 51, is unknown during PoC with the latest at 52, or the record's epoch is unresolved.
//  2. Answer the escrow lookup as absent, the way a node behind the create height does, and fail the settle broadcast.
//  3. Settle it through the admin entry.
//  4. Assert the answer is not taken as a prune: the settle was attempted, its error is not ErrEscrowPruned, and the row is kept for a retry.
func TestSettleKeepsAnEscrowTheChainCannotHavePrunedYet(t *testing.T) {
	cases := []struct {
		name       string
		chainEpoch uint64
		latest     uint64
		effective  uint64
	}{
		{name: "in its own epoch", chainEpoch: 50, latest: 51, effective: 50},
		{name: "in the epoch after", chainEpoch: 50, latest: 52, effective: 51},
		{name: "in the epoch after with the effective epoch unknown", chainEpoch: 50, latest: 52},
		{name: "with its epoch unresolved", chainEpoch: 0, latest: 91, effective: 90},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			testStore := newFakeStore()
			record := parkedRecord("23")
			record.ChainEpoch = testCase.chainEpoch
			testStore.devshards[record.EscrowID] = record
			broadcasts := 0
			tx := &fakeTxClient{
				getEscrowFn: func(context.Context, string) (chain.EscrowInfo, bool, error) { return chain.EscrowInfo{}, false, nil },
				settleEscrowFn: func(context.Context, *signing.Secp256k1Signer, chain.SettlementInput) (chain.SettleEscrowResult, error) {
					broadcasts++
					return chain.SettleEscrowResult{}, errors.New("escrow not found")
				},
			}
			m := &Manager{
				tx:               tx,
				store:            testStore,
				signer:           &fakeSignerSource{signer: testSigner(t)},
				settlementSource: &fakeSettlementSource{calls: &callLog{}},
				snapshots:        &fakeSnapshotSource{snapshot: chain.PhaseSnapshot{EpochIndex: testCase.latest, EffectiveEpochIndex: testCase.effective, BlockHeight: 1_000}},
				config:           holderWithSettlementEnabled(true),
			}

			_, err := m.Settle(context.Background(), record.EscrowID, false)

			if err == nil || errors.Is(err, ErrEscrowPruned) {
				t.Fatalf("Settle() = %v, want the broadcast's own error, not ErrEscrowPruned", err)
			}
			if broadcasts != 1 {
				t.Fatalf("settle broadcasts = %d, want 1", broadcasts)
			}
			if _, ok := testStore.snapshotDevshard(record.EscrowID); !ok {
				t.Fatal("row dropped for an escrow the chain cannot have pruned yet")
			}
		})
	}
}

func holderWithModelSettlement(globalEnabled bool, modelsJSON string) *config.Holder {
	cfg := config.Defaults()
	cfg.Rotation.SettlementEnabled = globalEnabled
	cfg.Rotation.ModelsJSON = modelsJSON
	return config.NewHolder(&cfg)
}

// Test flow:
//  1. Seed the store with a parked record for model-a, which turns settlement on for itself, and one for model-b, which says nothing.
//  2. Run `settlePending` with the global settlement toggle off.
//  3. Assert model-a's escrow is settled and dropped while model-b's stays parked under the global toggle.
func TestSettlePendingFollowsAModelsOwnSettlementFlag(t *testing.T) {
	testStore := newFakeStore()
	ownFlag := parkedRecord("31")
	inherited := parkedRecord("32")
	inherited.Model = "model-b"
	testStore.devshards[ownFlag.EscrowID] = ownFlag
	testStore.devshards[inherited.EscrowID] = inherited
	m := &Manager{
		tx:               settlingTxClient(),
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config: holderWithModelSettlement(false, `[
			{"model_id": "model-a", "amount": 1000, "settlement_enabled": true},
			{"model_id": "model-b", "amount": 1000}
		]`),
	}

	if err := m.settlePending(context.Background(), chain.PhaseSnapshot{}, []store.DevshardRecord{ownFlag, inherited}); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}
	if _, ok := testStore.snapshotDevshard(ownFlag.EscrowID); ok {
		t.Error("model-a's escrow was not settled though the model turns settlement on")
	}
	assertParked(t, testStore, inherited.EscrowID)
}

// Test flow:
//  1. Seed the store with one active record for model-a, which turns settlement off for itself.
//  2. Call `retire` with the global settlement toggle on and a chain client that fails the test if SettleEscrow is called.
//  3. Assert the escrow is parked, not settled.
func TestRetireParksAModelThatTurnsSettlementOff(t *testing.T) {
	testStore := newFakeStore()
	record := store.DevshardRecord{EscrowID: "33", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	testStore.devshards[record.EscrowID] = record
	txClient := &fakeTxClient{
		settleEscrowFn: func(context.Context, *signing.Secp256k1Signer, chain.SettlementInput) (chain.SettleEscrowResult, error) {
			t.Fatal("SettleEscrow must not be called for a model that turns settlement off")
			return chain.SettleEscrowResult{}, nil
		},
	}
	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config:           holderWithModelSettlement(true, `[{"model_id": "model-a", "amount": 1000, "settlement_enabled": false}]`),
	}

	if err := m.retire(context.Background(), record); err != nil {
		t.Fatalf("retire() = %v, want nil", err)
	}
	assertParked(t, testStore, record.EscrowID)
}

// Test flow:
//  1. Seed the store with a parked row marked gone from chain, and a chain client that fails the test if SettleEscrow is called.
//  2. Call `settlePending` with settlement enabled.
//  3. Assert it returns nil and the row stays as it was: a gone escrow has nothing to settle.
func TestSettlePendingSkipsARowGoneFromChain(t *testing.T) {
	testStore := newFakeStore()
	record := parkedRecord("12")
	record.GoneFromChain = true
	testStore.devshards[record.EscrowID] = record
	txClient := &fakeTxClient{
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			t.Fatal("SettleEscrow called for an escrow the chain no longer holds")
			return chain.SettleEscrowResult{}, nil
		},
		getEscrowFn: func(ctx context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
			t.Fatal("GetEscrow called for an escrow the chain no longer holds")
			return chain.EscrowInfo{}, false, nil
		},
	}
	m := &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config:           holderWithSettlementEnabled(true),
	}

	if err := m.settlePending(context.Background(), chain.PhaseSnapshot{}, []store.DevshardRecord{record}); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}
	if got, ok := testStore.snapshotDevshard(record.EscrowID); !ok || got != record {
		t.Fatalf("row = %+v (present %v), want %+v untouched", got, ok, record)
	}
}

// Test flow:
//  1. Seed the store with a serving row, mark it gone from chain, reactivate it, then park it.
//  2. Call `settlePending` with the rows the store now lists and settlement enabled.
//  3. Assert the row was settled and dropped: a reactivated row is no longer gone, so its later park settles.
func TestSettlePendingSettlesARowReactivatedAfterItWasGoneFromChain(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["14"] = store.DevshardRecord{EscrowID: "14", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	ctx := context.Background()
	if err := testStore.MarkDevshardGoneFromChain(ctx, "14"); err != nil {
		t.Fatalf("MarkDevshardGoneFromChain() = %v, want nil", err)
	}
	if err := testStore.SetDevshardActive(ctx, "14", true); err != nil {
		t.Fatalf("SetDevshardActive(true) = %v, want nil", err)
	}
	if err := testStore.ParkForSettlement(ctx, "14"); err != nil {
		t.Fatalf("ParkForSettlement() = %v, want nil", err)
	}
	m := &Manager{
		tx:               settlingTxClient(),
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config:           holderWithSettlementEnabled(true),
	}
	devshards, _ := testStore.ListDevshards(ctx)

	if err := m.settlePending(ctx, chain.PhaseSnapshot{}, devshards); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}
	if record, ok := testStore.snapshotDevshard("14"); ok {
		t.Fatalf("row = %+v still present, want it settled and dropped", record)
	}
}
