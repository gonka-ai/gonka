package registry

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/internal/leakcheck"
	"devshard/cmd/gateway/scheduler"
	"devshard/heightsync"
	"devshard/types"
	"devshard/user"
)

func TestMain(m *testing.M) {
	leakcheck.VerifyTestMain(m)
}

// settlementSource mirrors escrow.SettlementSource so a signature drift in this package fails here
// rather than in the composition root.
type settlementSource interface {
	IsBusy(escrowID string) bool
	Finalize(ctx context.Context, escrowID string) error
	BuildSettlement(ctx context.Context, escrowID string) (chain.SettlementInput, error)
}

var (
	_ settlementSource = (*Registry)(nil)
	// Assigns the registry into the scheduler's dependency struct to assert its unexported escrowSource interface.
	_ = func() scheduler.Deps { return scheduler.Deps{Escrows: (*Registry)(nil)} }
	_ = func() scheduler.Escrow { return scheduler.Escrow{Session: nonceStream{}} }
)

type fakeSession struct {
	sealed       int
	perSlotKeys  []string
	participants []string
	dials        []HostDial
	budgets      map[string]ChatBudget

	heightSyncView heightsync.OperatorView

	phase                atomic.Int32
	sweepReport          user.SweepReport
	onSweep              func()
	sweptGrace           time.Duration
	sweptBudget          int
	nonce                atomic.Uint64
	signatures           map[uint64]map[uint32][]byte
	settlementSignatures map[uint32][]byte
	settlementShortfall  error
	escrowState          types.EscrowState
	finalizeErr          error
	finalizeCalls        atomic.Int64
	onFinalize           func()
	flushErr             error
	flushCalls           atomic.Int64
	closeErr             error
	closeCalls           atomic.Int64
	chainMaxNonce        atomic.Uint32
	onFlush              func()
	prepare              func(user.ParamsForHost) (*user.PreparedInference, error)

	pendingTxs       []*types.DevshardTx
	pendingDiffErr   error
	pendingDiffCalls atomic.Int64
	onPendingDiff    func()
}

func newFakeSession(perSlotKeys ...string) *fakeSession {
	session := &fakeSession{perSlotKeys: perSlotKeys}
	seen := map[string]bool{}
	for _, participant := range perSlotKeys {
		if seen[participant] {
			continue
		}
		seen[participant] = true
		session.participants = append(session.participants, participant)
	}
	return session
}

func (f *fakeSession) PendingTxs() []*types.DevshardTx { return f.pendingTxs }

func (f *fakeSession) SendPendingDiff(context.Context) error {
	f.pendingDiffCalls.Add(1)
	if f.onPendingDiff != nil {
		f.onPendingDiff()
	}
	return f.pendingDiffErr
}

func (f *fakeSession) ParticipantKeys() []string        { return f.participants }
func (f *fakeSession) HostParticipantKeyList() []string { return f.perSlotKeys }
func (f *fakeSession) Nonce() uint64                    { return f.nonce.Load() }
func (f *fakeSession) PublishedNonce() uint64           { return f.nonce.Load() }
func (f *fakeSession) Balance() uint64                  { return 1 << 40 }
func (f *fakeSession) TokenPrice() uint64               { return 1 }
func (f *fakeSession) FeePerNonce() uint64              { return 0 }
func (f *fakeSession) Phase() types.SessionPhase        { return types.SessionPhase(f.phase.Load()) }

func (f *fakeSession) PrepareInferenceFn(chooser user.ParamsForHost) (*user.PreparedInference, error) {
	if f.prepare == nil {
		return nil, errors.New("fake session cannot prepare")
	}
	return f.prepare(chooser)
}

func (f *fakeSession) SettlementSignatures(nonce uint64) (map[uint32][]byte, error) {
	if f.settlementShortfall != nil {
		return nil, f.settlementShortfall
	}
	if f.settlementSignatures != nil {
		return f.settlementSignatures, nil
	}
	return f.signatures[nonce], nil
}

func (f *fakeSession) SignaturesAt(nonce uint64) map[uint32][]byte { return f.signatures[nonce] }

func (f *fakeSession) SignatureStatus() ([]user.SignatureStatusEntry, uint64, bool) {
	return nil, 0, false
}

func (f *fakeSession) SignedSlots() map[uint64]types.Bitmap128 { return nil }
func (f *fakeSession) SnapshotState() types.EscrowState        { return f.escrowState }
func (f *fakeSession) SealedInferences() int                   { return f.sealed }

func (f *fakeSession) ProtocolVersion() string { return f.escrowState.StateRootAndProtocolVersion }

func (f *fakeSession) Inference(nonce uint64) (types.InferenceRecord, bool) {
	record, committed := f.escrowState.Inferences[nonce]
	if !committed || record == nil {
		return types.InferenceRecord{}, false
	}
	return *record, true
}

func (f *fakeSession) SealedInference(uint64) (types.InferenceRecord, bool) {
	return types.InferenceRecord{}, false
}

func (f *fakeSession) LiveInferences() (types.SessionConfig, []types.InferenceRecord) {
	records := make([]types.InferenceRecord, 0, len(f.escrowState.Inferences))
	for _, record := range f.escrowState.Inferences {
		records = append(records, *record)
	}
	return f.escrowState.Config, records
}

func (f *fakeSession) Finalize(context.Context) error {
	f.finalizeCalls.Add(1)
	if f.onFinalize != nil {
		f.onFinalize()
	}
	return f.finalizeErr
}

func (f *fakeSession) FlushSnapshot() error {
	f.flushCalls.Add(1)
	if f.onFlush != nil {
		f.onFlush()
	}
	return f.flushErr
}

func (f *fakeSession) Close() error {
	f.closeCalls.Add(1)
	return f.closeErr
}

func (f *fakeSession) UserSession() *user.Session { return nil }

func (f *fakeSession) SweepExecutionTimeouts(_ context.Context, grace time.Duration, budget int) user.SweepReport {
	f.sweptGrace, f.sweptBudget = grace, budget
	if f.onSweep != nil {
		f.onSweep()
	}
	return f.sweepReport
}

func (f *fakeSession) SweepBeforeFinalize(ctx context.Context, budget int) (user.SweepReport, error) {
	return f.SweepExecutionTimeouts(ctx, noRaceGrace, budget), nil
}

func (f *fakeSession) HostDials() []HostDial { return f.dials }

func (f *fakeSession) ChatBudgets() map[string]ChatBudget { return f.budgets }

func (f *fakeSession) HeightSyncView() heightsync.OperatorView { return f.heightSyncView }

func (f *fakeSession) WaitRouterCatalog(context.Context) error   { return nil }
func (f *fakeSession) WaitHeightSeedReady(context.Context) error { return nil }

func (f *fakeSession) SetChainMaxNonce(maxNonce uint32)                { f.chainMaxNonce.Store(maxNonce) }
func (f *fakeSession) ArmQuietSettle()                                 {}
func (f *fakeSession) AllInferences() map[uint64]types.InferenceRecord { return nil }

func (f *fakeSession) setPhase(phase types.SessionPhase) { f.phase.Store(int32(phase)) }

type recordingMembership struct {
	mu       sync.Mutex
	shares   map[string]map[string]float64
	removals []string
}

func newRecordingMembership() *recordingMembership {
	return &recordingMembership{shares: map[string]map[string]float64{}}
}

func (m *recordingMembership) SetEscrowMembership(escrowID string, hostShares map[string]float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shares[escrowID] = hostShares
}

func (m *recordingMembership) RemoveEscrow(escrowID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.shares, escrowID)
	m.removals = append(m.removals, escrowID)
}

func (m *recordingMembership) sharesFor(escrowID string) map[string]float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shares[escrowID]
}

func (m *recordingMembership) removed() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.removals...)
}

type recordingExhaustion struct {
	mu        sync.Mutex
	exhausted []string
	reserves  []string
}

func (e *recordingExhaustion) OnReserveTaken(escrowID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reserves = append(e.reserves, escrowID)
}

func (e *recordingExhaustion) reservesTaken() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.reserves...)
}

func (e *recordingExhaustion) OnBalanceExhausted(escrowID string, reason scheduler.ExhaustionReason) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.exhausted = append(e.exhausted, escrowID)
}

func (e *recordingExhaustion) seen() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.exhausted...)
}

// sessions hands out a pre-registered session per escrow and counts how often each factory was asked.
type sessions struct {
	byEscrow             map[string]*fakeSession
	err                  error
	calls                atomic.Int64
	refuseConcurrentOpen bool
	openInFlight         atomic.Bool
}

func awaitDrainClose(t *testing.T, registry *Registry, closed func() bool) {
	t.Helper()
	registry.closing.Wait()
	if !closed() {
		t.Fatal("the drained escrow was never closed")
	}
}

func newSessions(byEscrow map[string]*fakeSession) *sessions {
	return &sessions{byEscrow: byEscrow}
}

// open stands in for a SQLite session, refusing a concurrent open when refuseConcurrentOpen is set.
func (s *sessions) open(_ context.Context, escrowID string) (EscrowSession, error) {
	s.calls.Add(1)
	if s.refuseConcurrentOpen {
		if !s.openInFlight.CompareAndSwap(false, true) {
			return nil, errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		defer s.openInFlight.Store(false)
		time.Sleep(5 * time.Millisecond)
	}
	if s.err != nil {
		return nil, s.err
	}
	session, known := s.byEscrow[escrowID]
	if !known {
		return nil, fmt.Errorf("no session for escrow %s", escrowID)
	}
	return session, nil
}

func fixedClock() func() time.Time {
	moment := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return moment }
}

// Test flow:
//  1. Build a registry with two escrows: escrow 1 where hostA holds two slots and hostB one, and escrow 2 where hostA holds a single slot.
//  2. Add both escrows.
//  3. Assert escrow 1's pushed membership gives hostA 2/3 (its slots in escrow 1 over its 3 total slots across both escrows) and hostB 1 (its only slot, in escrow 1 alone).
//  4. Assert escrow 2's pushed membership gives hostA 1/3, the same total-slots denominator, not the escrow-internal ratio of 1 that counting only escrow 2's own slot would give.
func TestAddPushesSharesSplitAcrossSharedParticipants(t *testing.T) {
	t.Parallel()
	shared := newSessions(map[string]*fakeSession{
		"1": newFakeSession("hostA", "hostA", "hostB"),
		"2": newFakeSession("hostA"),
	})
	capacity := newRecordingMembership()
	registry := New(Deps{ServingSessions: shared.open, Membership: capacity, Now: fixedClock()})

	if err := registry.Add(context.Background(), "1", "qwen"); err != nil {
		t.Fatalf("Add(1) = %v, want nil", err)
	}
	if err := registry.Add(context.Background(), "2", "qwen"); err != nil {
		t.Fatalf("Add(2) = %v, want nil", err)
	}

	wantFirst := map[string]float64{"hostA": 2.0 / 3.0, "hostB": 1}
	wantSecond := map[string]float64{"hostA": 1.0 / 3.0}
	if got := capacity.sharesFor("1"); !reflect.DeepEqual(got, wantFirst) {
		t.Errorf("membership for escrow 1 = %v, want %v", got, wantFirst)
	}
	if got := capacity.sharesFor("2"); !reflect.DeepEqual(got, wantSecond) {
		t.Errorf("membership for escrow 2 = %v, want %v", got, wantSecond)
	}
}

// Test flow:
//  1. Build a registry with two escrows sharing hostA (two slots in escrow 1, one in escrow 2) plus hostB (one slot in escrow 1), and add both.
//  2. Retire escrow 2.
//  3. Assert `RemoveEscrow` was called for escrow 2 only.
//  4. Assert escrow 1's membership is republished with hostA and hostB each now at share 1, since hostA no longer shares its slots with escrow 2.
//  5. Assert escrow 2's membership is gone.
func TestRetireRemovesMembershipAndRepublishesTheRest(t *testing.T) {
	t.Parallel()
	shared := newSessions(map[string]*fakeSession{
		"1": newFakeSession("hostA", "hostA", "hostB"),
		"2": newFakeSession("hostA"),
	})
	capacity := newRecordingMembership()
	registry := New(Deps{ServingSessions: shared.open, Membership: capacity, Now: fixedClock()})
	mustAdd(t, registry, "1", "qwen")
	mustAdd(t, registry, "2", "qwen")

	if err := registry.Retire("2"); err != nil {
		t.Fatalf("Retire(2) = %v, want nil", err)
	}

	if got, want := capacity.removed(), []string{"2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RemoveEscrow calls = %v, want %v", got, want)
	}
	wantFirst := map[string]float64{"hostA": 1, "hostB": 1}
	if got := capacity.sharesFor("1"); !reflect.DeepEqual(got, wantFirst) {
		t.Errorf("membership for escrow 1 after retire = %v, want %v", got, wantFirst)
	}
	if got := capacity.sharesFor("2"); got != nil {
		t.Errorf("membership for escrow 2 after retire = %v, want none", got)
	}
}

// Test flow:
//  1. For each case (the table varies the escrow's session phase: active, finalizing, or settlement), build a registry with one escrow in that phase.
//  2. Assert `Candidates` returns the case's expected count (1 for active, 0 otherwise).
//  3. Assert `Serves` agrees with whether any candidates were returned.
func TestCandidatesReturnOnlyAcceptingEscrows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		phase types.SessionPhase
		want  int
	}{
		{name: "active escrow is a candidate", phase: types.PhaseActive, want: 1},
		{name: "finalizing escrow is not", phase: types.PhaseFinalizing, want: 0},
		{name: "settling escrow is not", phase: types.PhaseSettlement, want: 0},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			session := newFakeSession("hostA")
			session.setPhase(testCase.phase)
			registry := New(Deps{
				ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
				Now:             fixedClock(),
			})
			mustAdd(t, registry, "1", "qwen")

			if got := len(registry.Candidates("qwen")); got != testCase.want {
				t.Errorf("len(Candidates(qwen)) = %d, want %d", got, testCase.want)
			}
			if got := registry.Serves("qwen"); got != (testCase.want > 0) {
				t.Errorf("Serves(qwen) = %v, want %v", got, testCase.want > 0)
			}
		})
	}
}

// Test flow:
//  1. Build a registry with two escrows: one accepting (with one request acquired) and one in settlement.
//  2. Assert the snapshot reports both, with the accepting escrow's InFlight and Accepting fields set and the settling one reported not accepting with no in-flight requests.
//  3. Assert `Candidates` returns only the one accepting escrow.
func TestSnapshotKeepsEscrowsCandidatesDrop(t *testing.T) {
	t.Parallel()
	accepting := newFakeSession("hostA", "hostA", "hostB")
	settling := newFakeSession("hostC")
	settling.setPhase(types.PhaseSettlement)
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": accepting, "2": settling}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	mustAdd(t, registry, "2", "qwen")
	_, release, held := registry.Acquire("1")
	if !held {
		t.Fatal("Acquire(1) was refused")
	}
	t.Cleanup(release)

	want := []EscrowState{
		{ID: "1", Model: "qwen", Accepting: true, InFlight: 1, Participants: []string{"hostA", "hostB"}},
		{ID: "2", Model: "qwen", Accepting: false, InFlight: 0, Participants: []string{"hostC"}},
	}
	if got := registry.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot() = %+v, want %+v", got, want)
	}
	if got := len(registry.Candidates("qwen")); got != 1 {
		t.Fatalf("len(Candidates(qwen)) = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a registry with no escrows added.
//  2. Assert `Snapshot` returns none.
func TestSnapshotOfAnEmptyRegistryIsEmpty(t *testing.T) {
	t.Parallel()
	if got := New(Deps{Now: fixedClock()}).Snapshot(); len(got) != 0 {
		t.Fatalf("Snapshot() = %+v, want none", got)
	}
}

// Test flow:
//  1. Build a registry and add three escrows, IDs 3, 1, and 2, across two different models, in that order.
//  2. Assert `Snapshot` reports them ordered by escrow ID (1, 2, 3) regardless of model or add order.
func TestSnapshotOrdersEveryEscrowByIDAcrossModels(t *testing.T) {
	t.Parallel()
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{
			"3": newFakeSession("hostC"),
			"1": newFakeSession("hostA"),
			"2": newFakeSession("hostB"),
		}).open,
		Now: fixedClock(),
	})
	mustAdd(t, registry, "3", "kimi")
	mustAdd(t, registry, "1", "qwen")
	mustAdd(t, registry, "2", "kimi")

	states := registry.Snapshot()
	ordered := make([]string, 0, len(states))
	for _, state := range states {
		ordered = append(ordered, state.ID)
	}
	if want := []string{"1", "2", "3"}; !reflect.DeepEqual(ordered, want) {
		t.Fatalf("Snapshot() escrow order = %v, want %v", ordered, want)
	}
}

// Test flow:
//  1. Build a registry with one escrow over two participants and take a first snapshot.
//  2. Mutate that first snapshot's Participants slice.
//  3. Take a second snapshot.
//  4. Assert the second snapshot's Participants are unaffected by the earlier mutation.
func TestSnapshotParticipantsAreIndependentCopies(t *testing.T) {
	t.Parallel()
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": newFakeSession("hostA", "hostB")}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")

	first := registry.Snapshot()
	first[0].Participants[0] = "corrupted"

	second := registry.Snapshot()
	if want := []string{"hostA", "hostB"}; !reflect.DeepEqual(second[0].Participants, want) {
		t.Fatalf("Snapshot() Participants after mutating an earlier snapshot = %v, want %v", second[0].Participants, want)
	}
}

// Test flow:
//  1. Build a registry with two escrows on different models, qwen and kimi.
//  2. Assert `Candidates("qwen")` returns only the qwen escrow.
//  3. Assert `Models` reports both model names.
func TestCandidatesSkipOtherModels(t *testing.T) {
	t.Parallel()
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{
			"1": newFakeSession("hostA"),
			"2": newFakeSession("hostB"),
		}).open,
		Now: fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	mustAdd(t, registry, "2", "kimi")

	candidates := registry.Candidates("qwen")
	if len(candidates) != 1 || candidates[0].ID != "1" || candidates[0].Model != "qwen" {
		t.Fatalf("Candidates(qwen) = %+v, want only escrow 1 on qwen", candidates)
	}
	if got, want := registry.Models(), []string{"kimi", "qwen"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Models() = %v, want %v", got, want)
	}
}

// Test flow:
//  1. Build a registry and add three escrows, IDs 3, 1, and 2, in that order, on the same model.
//  2. Assert `Candidates` reports them ordered by escrow ID (1, 2, 3) regardless of add order.
func TestCandidatesAreOrderedByEscrowID(t *testing.T) {
	t.Parallel()
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{
			"3": newFakeSession("hostC"),
			"1": newFakeSession("hostA"),
			"2": newFakeSession("hostB"),
		}).open,
		Now: fixedClock(),
	})
	mustAdd(t, registry, "3", "qwen")
	mustAdd(t, registry, "1", "qwen")
	mustAdd(t, registry, "2", "qwen")

	candidates := registry.Candidates("qwen")
	ordered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, candidate.ID)
	}
	if want := []string{"1", "2", "3"}; !reflect.DeepEqual(ordered, want) {
		t.Errorf("candidate order = %v, want %v", ordered, want)
	}
}

// Test flow:
//  1. Build a registry with one escrow and confirm it is a candidate and routable.
//  2. Retire the escrow.
//  3. Assert `RoutableSession` now reports it gone and `Candidates` returns none.
//  4. Assert the candidate slice returned before the retire still names the escrow: the caller must re-resolve the target through the registry instead of trusting a stale assignment.
func TestRetiredEscrowVanishesBetweenCandidatesAndTheRoutableLookup(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")

	candidates := registry.Candidates("qwen")
	if len(candidates) != 1 {
		t.Fatalf("len(Candidates(qwen)) = %d, want 1", len(candidates))
	}
	if _, found := registry.RoutableSession("1"); !found {
		t.Fatal("RoutableSession(1) before retire = not found, want found")
	}

	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}

	if _, found := registry.RoutableSession("1"); found {
		t.Error("RoutableSession(1) after retire = found, want gone")
	}
	if got := len(registry.Candidates("qwen")); got != 0 {
		t.Errorf("len(Candidates(qwen)) after retire = %d, want 0", got)
	}
	if candidates[0].ID != "1" {
		t.Errorf("previously returned candidate = %q, want 1", candidates[0].ID)
	}
}

// Test flow:
//  1. Build a registry with one escrow and a recording exhaustion sink.
//  2. Report the escrow exhausted.
//  3. Assert the sink recorded that escrow ID.
func TestNonceExhaustionReachesTheRotationSink(t *testing.T) {
	t.Parallel()
	rotation := &recordingExhaustion{}
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": newFakeSession("hostA")}).open,
		Exhaustion:      rotation,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")

	registry.Exhausted("1", "test")

	if got, want := rotation.seen(), []string{"1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("OnBalanceExhausted calls = %v, want %v", got, want)
	}
}

// Test flow:
//  1. Build a registry with one escrow and acquire it, holding a request in flight.
//  2. Retire the escrow.
//  3. Assert the session is not closed yet, the escrow reports busy, and it accepts neither a routable lookup nor a new acquire while draining.
//  4. Release the request and wait for the drain to close.
//  5. Assert the session closed once, its snapshot was flushed once, and the escrow no longer reports busy.
func TestRetireDefersCloseUntilInFlightRequestsDrain(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")

	held, release, acquired := registry.Acquire("1")
	if !acquired {
		t.Fatal("Acquire(1) = false, want true")
	}
	if held != EscrowSession(session) {
		t.Errorf("Acquire(1) = %v, want the escrow's own session", held)
	}
	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}

	if got := session.closeCalls.Load(); got != 0 {
		t.Fatalf("Close calls while a request is in flight = %d, want 0", got)
	}
	if !registry.IsBusy("1") {
		t.Error("IsBusy(1) with one in-flight request = false, want true")
	}
	if _, found := registry.RoutableSession("1"); found {
		t.Error("RoutableSession(1) while draining = found; a retired escrow must not take another race")
	}
	if _, _, routable := registry.Acquire("1"); routable {
		t.Error("Acquire(1) while draining = true; draining finishes existing work, it never takes new work")
	}

	release()
	awaitDrainClose(t, registry, func() bool { return session.closeCalls.Load() == 1 })

	if got := session.closeCalls.Load(); got != 1 {
		t.Errorf("Close calls after the last release = %d, want 1", got)
	}
	if got := session.flushCalls.Load(); got != 1 {
		t.Errorf("FlushSnapshot calls after the last release = %d, want 1", got)
	}
	if registry.IsBusy("1") {
		t.Error("IsBusy(1) after the last release = true, want false")
	}
}

// Test flow:
//  1. Build a registry with one escrow whose flush always fails, acquire it, and retire it.
//  2. Release the request and wait for the drain to close.
//  3. Assert `DrainCloseFailures` counts the failed close, since nobody is left to hand the failure to directly.
func TestADrainedEscrowThatFailsToCloseIsCounted(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	session.flushErr = errors.New("disk full")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	_, release, acquired := registry.Acquire("1")
	if !acquired {
		t.Fatal("Acquire(1) = false, want true")
	}
	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}

	release()
	awaitDrainClose(t, registry, func() bool { return registry.DrainCloseFailures() == 1 })

	if got := registry.DrainCloseFailures(); got != 1 {
		t.Fatalf("DrainCloseFailures() = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a registry with one escrow whose flush always fails, and add it.
//  2. Retire the escrow and assert the flush failure is reported.
//  3. Add the same escrow ID again.
//  4. Assert the add succeeds: Close still releases the store on a failed flush, freeing the id.
func TestAnEscrowWhoseFlushFailedCanBePublishedAgain(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	session.flushErr = errors.New("disk full")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	if err := registry.Retire("1"); err == nil {
		t.Fatal("Retire(1) = nil, want the flush failure reported")
	}

	if err := registry.Add(context.Background(), "1", "qwen"); err != nil {
		t.Fatalf("Add(1) after a failed flush = %v, want the released id accepted", err)
	}
}

// Test flow:
//  1. Build a registry with one escrow, acquire it, and retire it while the request is still in flight.
//  2. Assert `RoutableSession` reports it gone: a retired escrow takes no new request.
//  3. Assert `SettlementSession` still finds the same draining session: its committed nonces still owe their votes and settlement.
func TestRoutingLosesARetiredEscrowWhileSettlementKeepsIt(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	_, release, acquired := registry.Acquire("1")
	if !acquired {
		t.Fatal("Acquire(1) = false, want true")
	}
	t.Cleanup(release)

	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}

	if _, routable := registry.RoutableSession("1"); routable {
		t.Error("RoutableSession(1) after retire = found, want gone: a retired escrow must take no new request")
	}
	held, settling := registry.SettlementSession("1")
	if !settling {
		t.Fatal("SettlementSession(1) after retire = not found: every vote the escrow still owed is dropped")
	}
	if held != EscrowSession(session) {
		t.Errorf("SettlementSession(1) = %v, want the draining escrow's own session", held)
	}
}

// Test flow:
//  1. Build a registry whose session factory returns a first "draining" session, then a "replacement" one.
//  2. Add, acquire, and retire the escrow so the draining session is still holding an in-flight request.
//  3. Add the same escrow ID again while it drains.
//  4. Assert the second add fails with `ErrDraining`.
//  5. Assert `SettlementSession` still resolves to the original draining session, that only one session was ever opened, and the escrow is not routable.
func TestAddRefusesAnIdAnEarlierEntryIsStillDraining(t *testing.T) {
	t.Parallel()
	draining := newFakeSession("hostA")
	replacement := newFakeSession("hostA")
	var opened atomic.Int64
	registry := New(Deps{
		ServingSessions: func(context.Context, string) (EscrowSession, error) {
			if opened.Add(1) == 1 {
				return draining, nil
			}
			return replacement, nil
		},
		Now: fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	_, release, acquired := registry.Acquire("1")
	if !acquired {
		t.Fatal("Acquire(1) = false, want true")
	}
	t.Cleanup(release)
	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}

	err := registry.Add(context.Background(), "1", "qwen")

	if !errors.Is(err, ErrDraining) {
		t.Fatalf("Add(1) while it drains = %v, want ErrDraining", err)
	}
	held, settling := registry.SettlementSession("1")
	if !settling || held != EscrowSession(draining) {
		t.Fatalf("SettlementSession(1) = %v (found = %v), want the draining entry that owns the committed nonces", held, settling)
	}
	if got := opened.Load(); got != 1 {
		t.Errorf("serving sessions opened = %d, want 1: a second handle over the storage the draining entry still holds", got)
	}
	if _, routable := registry.RoutableSession("1"); routable {
		t.Error("RoutableSession(1) = found, want gone: the refused escrow was published anyway")
	}
}

// Test flow:
//  1. Build a registry with one escrow, acquire it, and retire it while the request is in flight.
//  2. Release the request and wait for the drain to close.
//  3. Assert `SettlementSession` no longer finds the escrow once its storage is closed: the settlement lookup outlives retirement but not the drain itself.
func TestSettlementLookupEndsWithTheDrain(t *testing.T) {
	t.Parallel()
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": newFakeSession("hostA")}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	_, release, acquired := registry.Acquire("1")
	if !acquired {
		t.Fatal("Acquire(1) = false, want true")
	}
	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}

	release()
	awaitDrainClose(t, registry, func() bool {
		_, settling := registry.SettlementSession("1")
		return !settling
	})

	if _, settling := registry.SettlementSession("1"); settling {
		t.Error("SettlementSession(1) after the last release = found, want gone: its storage is closed")
	}
}

// Test flow:
//  1. Build a registry with one idle escrow (no acquired request).
//  2. Retire it.
//  3. Assert the session closed immediately, once.
func TestRetireClosesAnIdleEscrowImmediately(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")

	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}

	if got := session.closeCalls.Load(); got != 1 {
		t.Errorf("Close calls = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a registry with one escrow whose close always fails, and add it.
//  2. Retire the escrow and assert the close failure is reported.
//  3. Add the same escrow ID again.
//  4. Assert the add fails with `ErrDraining` and the failed close was not retried: a close that failed released nothing, so the refusal must outlive it.
func TestAddRefusesAnEscrowWhoseCloseFailed(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	session.closeErr = errors.New("storage refused to close")
	sessions := newSessions(map[string]*fakeSession{"1": session})
	registry := New(Deps{ServingSessions: sessions.open, Now: fixedClock()})
	mustAdd(t, registry, "1", "qwen")

	if err := registry.Retire("1"); err == nil {
		t.Fatal("Retire(1) = nil, want the close failure")
	}

	err := registry.Add(context.Background(), "1", "qwen")
	if !errors.Is(err, ErrDraining) {
		t.Fatalf("Add(1) after a failed close = %v, want ErrDraining", err)
	}
	if got := session.closeCalls.Load(); got != 1 {
		t.Fatalf("Close calls = %d, want the failed close not to be retried by Add", got)
	}
}

// Test flow:
//  1. Build a registry with one escrow and retire it.
//  2. Assert `Acquire` refuses it.
func TestAcquireRefusesARetiredEscrow(t *testing.T) {
	t.Parallel()
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": newFakeSession("hostA")}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}

	if _, _, acquired := registry.Acquire("1"); acquired {
		t.Error("Acquire(1) after retire = true, want false")
	}
}

// Test flow:
//  1. Build a registry with one escrow and take a candidate's `Hold` before retiring, then release it.
//  2. Retire the escrow.
//  3. Assert that same candidate's `Hold` is now refused, since a nonce was committed against a retired escrow.
//  4. Assert `IsBusy` reports false: nothing is in flight after the refused commit.
func TestRetireRefusesTheNonceCommitHoldTakenBeforeIt(t *testing.T) {
	t.Parallel()
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": newFakeSession("hostA")}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	candidate := registry.Candidates("qwen")[0]

	if release, held := candidate.Hold(); !held {
		t.Fatal("Hold() before retire = false, want true")
	} else {
		release()
	}
	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}

	if _, held := candidate.Hold(); held {
		t.Error("Hold() after retire = true, want false: a nonce was committed on a retired escrow")
	}
	if registry.IsBusy("1") {
		t.Error("IsBusy(1) = true, want false: nothing is in flight after the refused commit")
	}
}

// Test flow:
//  1. Build a registry with one escrow and acquire it twice.
//  2. Assert the candidate's ActiveUsers is 2.
//  3. Release one acquire.
//  4. Assert ActiveUsers drops to 1.
func TestActiveUsersFeedsTheLoadScore(t *testing.T) {
	t.Parallel()
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": newFakeSession("hostA")}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")

	_, releaseFirst, acquiredFirst := registry.Acquire("1")
	_, _, acquiredSecond := registry.Acquire("1")
	if !acquiredFirst || !acquiredSecond {
		t.Fatal("Acquire(1) = false, want true")
	}

	if got := registry.Candidates("qwen")[0].ActiveUsers; got != 2 {
		t.Errorf("ActiveUsers = %d, want 2", got)
	}
	releaseFirst()
	if got := registry.Candidates("qwen")[0].ActiveUsers; got != 1 {
		t.Errorf("ActiveUsers after one release = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a registry with one escrow and acquire it twice.
//  2. Release the first acquire's release function twice.
//  3. Assert ActiveUsers still reflects only one release: a doubled release counts once.
func TestReleasingTwiceCountsOnce(t *testing.T) {
	t.Parallel()
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": newFakeSession("hostA")}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	_, releaseFirst, _ := registry.Acquire("1")
	if _, _, acquired := registry.Acquire("1"); !acquired {
		t.Fatal("Acquire(1) = false, want true")
	}

	releaseFirst()
	releaseFirst()

	if got := registry.Candidates("qwen")[0].ActiveUsers; got != 1 {
		t.Errorf("ActiveUsers after a doubled release = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a registry and add one escrow.
//  2. Add the same escrow ID again.
//  3. Assert the factory was called only once, the live session was never closed, and `Candidates` still reports exactly one escrow: a published escrow is not opened a second time.
func TestAddIsIdempotentAndOpensNoSecondSession(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	factory := newSessions(map[string]*fakeSession{"1": session})
	registry := New(Deps{ServingSessions: factory.open, Now: fixedClock()})
	mustAdd(t, registry, "1", "qwen")

	mustAdd(t, registry, "1", "qwen")

	if got := factory.calls.Load(); got != 1 {
		t.Fatalf("factory calls = %d, want 1", got)
	}
	if got := session.closeCalls.Load(); got != 0 {
		t.Errorf("Close calls on the live session = %d, want 0", got)
	}
	if got := len(registry.Candidates("qwen")); got != 1 {
		t.Errorf("len(Candidates(qwen)) = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a registry with two escrows, publish the first, and take a listing.
//  2. Retire both: the first while live, the second before it was ever published.
//  3. Add both through the listing and assert neither routes and no session was opened for them.
//  4. Add both through a listing taken after the retirements and assert both route again.
func TestAListingSkipsAnEscrowRetiredSinceItWasTaken(t *testing.T) {
	t.Parallel()
	factory := newSessions(map[string]*fakeSession{"1": newFakeSession("hostA"), "2": newFakeSession("hostB")})
	registry := New(Deps{ServingSessions: factory.open, Now: fixedClock()})
	mustAdd(t, registry, "1", "qwen")
	stale := registry.Listing()
	defer stale.Close()

	for _, escrowID := range []string{"1", "2"} {
		if err := registry.Retire(escrowID); err != nil {
			t.Fatalf("Retire(%s) = %v, want nil", escrowID, err)
		}
	}
	for _, escrowID := range []string{"1", "2"} {
		if err := stale.Add(context.Background(), escrowID, "qwen"); err != nil {
			t.Fatalf("stale listing Add(%s) = %v, want nil", escrowID, err)
		}
		if _, routable := registry.Routable(escrowID); routable {
			t.Errorf("escrow %s routes through a listing taken before its retirement", escrowID)
		}
	}
	if got := factory.calls.Load(); got != 1 {
		t.Fatalf("factory calls = %d, want 1", got)
	}

	fresh := registry.Listing()
	defer fresh.Close()
	for _, escrowID := range []string{"1", "2"} {
		if err := fresh.Add(context.Background(), escrowID, "qwen"); err != nil {
			t.Fatalf("fresh listing Add(%s) = %v, want nil", escrowID, err)
		}
		if _, routable := registry.Routable(escrowID); !routable {
			t.Errorf("escrow %s does not route through a listing taken after its retirement", escrowID)
		}
	}
}

// Test flow:
//  1. Build a registry whose session factory refuses a concurrent open, the way SQLite refuses a second writer.
//  2. Call `Add` for the same escrow ID from two goroutines at once.
//  3. Assert both callers succeed with no error.
//  4. Assert the factory was called only once and `Candidates` reports exactly one escrow.
func TestConcurrentAddsOfOneEscrowOpenItOnce(t *testing.T) {
	t.Parallel()
	factory := newSessions(map[string]*fakeSession{"1": newFakeSession("hostA")})
	factory.refuseConcurrentOpen = true
	registry := New(Deps{ServingSessions: factory.open, Now: fixedClock()})

	var adding sync.WaitGroup
	failures := make([]error, 2)
	for caller := range failures {
		adding.Go(func() {
			failures[caller] = registry.Add(context.Background(), "1", "qwen")
		})
	}
	adding.Wait()

	for caller, err := range failures {
		if err != nil {
			t.Fatalf("caller %d: Add = %v, want both callers to succeed", caller, err)
		}
	}
	if got := factory.calls.Load(); got != 1 {
		t.Fatalf("factory calls = %d, want 1", got)
	}
	if got := len(registry.Candidates("qwen")); got != 1 {
		t.Fatalf("len(Candidates(qwen)) = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a registry whose session factory always fails to open.
//  2. Call `Add`.
//  3. Assert the returned error wraps the factory's failure.
//  4. Assert `Candidates` reports none: the failed escrow was never published.
func TestAddReportsAFailedSessionOpen(t *testing.T) {
	t.Parallel()
	factory := newSessions(nil)
	factory.err = errors.New("chain unreachable")
	registry := New(Deps{ServingSessions: factory.open, Now: fixedClock()})

	err := registry.Add(context.Background(), "1", "qwen")

	if err == nil || !errors.Is(err, factory.err) {
		t.Fatalf("Add = %v, want it to wrap %v", err, factory.err)
	}
	if got := len(registry.Candidates("qwen")); got != 0 {
		t.Errorf("len(Candidates(qwen)) = %d, want 0", got)
	}
}

// Test flow:
//  1. Build a registry with one live escrow and one escrow acquired then retired (draining).
//  2. Call `Close` on the registry.
//  3. Assert both the live and the draining sessions were each closed once.
//  4. Assert `Add` after `Close` fails with `ErrClosed`.
func TestCloseReleasesLiveAndDrainingSessions(t *testing.T) {
	t.Parallel()
	live := newFakeSession("hostA")
	drained := newFakeSession("hostB")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": live, "2": drained}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	mustAdd(t, registry, "2", "qwen")
	if _, _, acquired := registry.Acquire("2"); !acquired {
		t.Fatal("Acquire(2) = false, want true")
	}
	if err := registry.Retire("2"); err != nil {
		t.Fatalf("Retire(2) = %v, want nil", err)
	}

	if err := registry.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	if got := live.closeCalls.Load(); got != 1 {
		t.Errorf("Close calls on the live session = %d, want 1", got)
	}
	if got := drained.closeCalls.Load(); got != 1 {
		t.Errorf("Close calls on the draining session = %d, want 1", got)
	}
	if err := registry.Add(context.Background(), "1", "qwen"); !errors.Is(err, ErrClosed) {
		t.Errorf("Add after Close = %v, want ErrClosed", err)
	}
}

// Test flow:
//  1. Build a registry with four escrows.
//  2. For each escrow, run concurrent goroutines that repeatedly add and retire it, acquire and release it, and read candidates/routable sessions/`Serves`, 200 iterations each.
//  3. Wait for all goroutines to finish.
//  4. Assert every escrow ends up not busy and was closed by at least one rotation.
func TestConcurrentPicksRotationsAndRequestsStayConsistent(t *testing.T) {
	t.Parallel()
	byEscrow := map[string]*fakeSession{}
	for index := range 4 {
		byEscrow[fmt.Sprint(index)] = newFakeSession("hostA", "hostB")
	}
	registry := New(Deps{
		ServingSessions: newSessions(byEscrow).open,
		Membership:      newRecordingMembership(),
		Now:             fixedClock(),
	})
	t.Cleanup(func() { _ = registry.Close() })

	var workers sync.WaitGroup
	for index := range 4 {
		escrowID := fmt.Sprint(index)
		workers.Add(3)
		go func() {
			defer workers.Done()
			for range 200 {
				_ = registry.Add(context.Background(), escrowID, "qwen")
				_ = registry.Retire(escrowID)
			}
		}()
		go func() {
			defer workers.Done()
			for range 200 {
				if _, release, acquired := registry.Acquire(escrowID); acquired {
					release()
				}
			}
		}()
		go func() {
			defer workers.Done()
			for range 200 {
				for _, candidate := range registry.Candidates("qwen") {
					_, _ = registry.RoutableSession(candidate.ID)
				}
				_ = registry.Serves("qwen")
			}
		}()
	}
	workers.Wait()

	for escrowID, session := range byEscrow {
		if registry.IsBusy(escrowID) {
			t.Errorf("escrow %s is still busy after every request finished", escrowID)
		}
		if got := session.closeCalls.Load(); got == 0 {
			t.Errorf("escrow %s was never closed across %d rotations", escrowID, got)
		}
	}
}

func mustAdd(t *testing.T, registry *Registry, escrowID, model string) {
	t.Helper()
	if err := registry.Add(context.Background(), escrowID, model); err != nil {
		t.Fatalf("Add(%s, %s) = %v, want nil", escrowID, model, err)
	}
}

// Test flow:
//  1. Build a registry with one escrow whose session flush blocks on an external lock held by the test.
//  2. Retire the escrow in a goroutine, letting it block inside the flush while holding the registry lock's path.
//  3. Call `IsBusy` from another goroutine while the retire is blocked.
//  4. Assert `IsBusy` answers promptly rather than deadlocking: the registry lock must not be held across the session flush, or the two locks would be taken in opposite orders.
//  5. Release the external lock and assert `Retire` completes with no error.
func TestRetireClosesTheSessionOutsideTheRegistryLock(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	var sessionLock sync.Mutex
	flushing := make(chan struct{})
	session.onFlush = func() {
		close(flushing)
		sessionLock.Lock()
		defer sessionLock.Unlock()
	}
	registry := New(Deps{ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open, Now: fixedClock()})
	mustAdd(t, registry, "1", "qwen")

	sessionLock.Lock()
	defer sessionLock.Unlock()
	retired := make(chan error, 1)
	go func() { retired <- registry.Retire("1") }()
	<-flushing

	answered := make(chan bool, 1)
	go func() { answered <- registry.IsBusy("1") }()

	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("the registry lock is held across the session flush: routing and settlement are wedged behind one retirement")
	}
	sessionLock.Unlock()
	if err := <-retired; err != nil {
		t.Fatalf("Retire = %v", err)
	}
	sessionLock.Lock()
}

// Test flow:
//  1. Build a registry with one escrow, acquire it, and retire it while the request is in flight.
//  2. Assert the snapshot still reports the escrow, not accepting, with one request in flight.
//  3. Release the request.
//  4. Assert the snapshot no longer reports the escrow once the last request has drained.
func TestSnapshotKeepsARetiredEscrowUntilItDrains(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	_, release, acquired := registry.Acquire("1")
	if !acquired {
		t.Fatal("Acquire(1) = false, want true")
	}

	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}

	draining := registry.Snapshot()
	if len(draining) != 1 {
		t.Fatalf("Snapshot() while draining = %+v, want the retired escrow still reported", draining)
	}
	if draining[0].InFlight != 1 {
		t.Errorf("InFlight while draining = %d, want 1", draining[0].InFlight)
	}
	if draining[0].Accepting {
		t.Error("Accepting while draining = true, want false")
	}

	release()

	if drained := registry.Snapshot(); len(drained) != 0 {
		t.Fatalf("Snapshot() after the last request drained = %+v, want the escrow gone", drained)
	}
}

// Test flow:
//  1. Build a registry with one escrow whose close always fails, acquire it, and retire it.
//  2. Release the request.
//  3. Assert the snapshot reports no series for that escrow even though its close failed: there is nothing left to report.
func TestSnapshotDropsADrainedEscrowWhoseCloseFailed(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	session.closeErr = errors.New("flush failed")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	_, release, _ := registry.Acquire("1")
	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}

	release()

	if drained := registry.Snapshot(); len(drained) != 0 {
		t.Fatalf("Snapshot() after a failed close = %+v, want no series for an escrow with nothing left", drained)
	}
}

// Test flow:
//  1. Build a registry with two escrows; for each, add, acquire, retire, then release it.
//  2. Wait for the drain to close both.
//  3. Assert the registry's internal draining view ends up empty once both escrows have drained.
func TestTheDrainingViewShrinksWhenAnEscrowFinishes(t *testing.T) {
	t.Parallel()
	sessions := map[string]*fakeSession{"1": newFakeSession("hostA"), "2": newFakeSession("hostB")}
	registry := New(Deps{
		ServingSessions: newSessions(sessions).open,
		Now:             fixedClock(),
	})
	for _, escrowID := range []string{"1", "2"} {
		mustAdd(t, registry, escrowID, "qwen")
		_, release, _ := registry.Acquire(escrowID)
		if err := registry.Retire(escrowID); err != nil {
			t.Fatalf("Retire(%s) = %v, want nil", escrowID, err)
		}
		release()
	}
	awaitDrainClose(t, registry, func() bool {
		view := registry.drainingView.Load()
		return view == nil || len(*view) == 0
	})

	if view := registry.drainingView.Load(); view != nil && len(*view) != 0 {
		t.Fatalf("draining view holds %d entries after both drained, want none", len(*view))
	}
}
