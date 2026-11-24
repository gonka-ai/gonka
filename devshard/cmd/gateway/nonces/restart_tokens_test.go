package nonces

import (
	"fmt"
	"maps"
	"testing"
	"testing/synctest"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/registry"
	"devshard/types"
	"devshard/user"
)

const (
	restartInputLength       = 400
	restartFirstInputTokens  = 57
	restartSecondInputTokens = 43
	restartOutputTokens      = 31
	restartBothInputTokens   = restartFirstInputTokens + restartSecondInputTokens
)

var crashLossBound = time.Duration(config.Defaults().NonceAccounting.SnapshotSeconds) * time.Second

type scriptedSession struct {
	registry.EscrowSession
	state   types.EscrowState
	sealed  map[uint64]types.InferenceRecord
	lookups map[uint64]int

	onSnapshot func()
}

func (s *scriptedSession) UserSession() *user.Session { return nil }
func (s *scriptedSession) SnapshotState() types.EscrowState {
	snapshot := s.state
	snapshot.Inferences = maps.Clone(s.state.Inferences)
	if s.onSnapshot != nil {
		s.onSnapshot()
	}
	return snapshot
}
func (s *scriptedSession) SealedInference(id uint64) (types.InferenceRecord, bool) {
	s.lookups[id]++
	record, kept := s.sealed[id]
	return record, kept
}

func (s *scriptedSession) finishLive(nonce, inputTokens uint64) {
	s.state.Inferences[nonce] = finishedRecord(nonce, inputTokens)
}

func (s *scriptedSession) finishAndSeal(nonce, inputTokens uint64) {
	delete(s.state.Inferences, nonce)
	s.sealed[nonce] = *finishedRecord(nonce, inputTokens)
}

type scriptedSource struct{ session *scriptedSession }

func (s scriptedSource) Snapshot() []registry.EscrowState {
	return []registry.EscrowState{{ID: tokenEscrowID, Model: "llama"}}
}

func (s scriptedSource) RoutableSession(string) (registry.EscrowSession, bool) {
	return s.session, true
}

func startedRecord(nonce uint64) *types.InferenceRecord {
	return &types.InferenceRecord{Status: types.StatusStarted, ExecutorSlot: uint32(nonce % 3), InputLength: restartInputLength, MaxTokens: 64, ReservedCost: 900}
}

func finishedRecord(nonce, inputTokens uint64) *types.InferenceRecord {
	record := startedRecord(nonce)
	record.Status, record.InputTokens, record.OutputTokens, record.ActualCost = types.StatusFinished, inputTokens, restartOutputTokens, 300
	return record
}

func twoStartedNonces() *scriptedSession {
	return &scriptedSession{
		state: types.EscrowState{
			Group:       []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1a"}, {SlotID: 1, ValidatorAddress: "gonka1b"}, {SlotID: 2, ValidatorAddress: "gonka1c"}},
			LatestNonce: 2,
			Inferences:  map[uint64]*types.InferenceRecord{1: startedRecord(1), 2: startedRecord(2)},
		},
		sealed:  map[uint64]types.InferenceRecord{},
		lookups: map[uint64]int{},
	}
}

func storedRecorder(t *testing.T, path string, group []types.SlotAssignment) *Recorder {
	t.Helper()
	store, err := accounting.OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() = %v, want nil", err)
	}
	service, err := accounting.NewService(accounting.Settings{
		Store:            store,
		SnapshotInterval: crashLossBound,
		Now:              func() time.Time { return time.Unix(0, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("NewService() = %v, want nil", err)
	}
	if err := service.Book.OpenEscrow(accounting.EscrowMetadata{EscrowID: tokenEscrowID, Model: "llama", CreationEpoch: 7, Slots: group}); err != nil {
		t.Fatalf("OpenEscrow() = %v, want nil", err)
	}
	return recorderFor(service)
}

type restoredLedger struct {
	totals         accounting.ParticipantRecord
	estimatedError uint64
	retired        bool
}

func storedLedger(t *testing.T, path string) restoredLedger {
	t.Helper()
	store, err := accounting.OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() = %v, want nil", err)
	}
	defer func() { _ = store.Close() }()
	snapshot, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	book := accounting.NewBook(func() time.Time { return time.Unix(0, 0).UTC() })
	if err := book.Restore(snapshot); err != nil {
		t.Fatalf("Restore() = %v, want nil", err)
	}
	restored := restoredLedger{totals: foldRecords(book.Query(accounting.QueryFilter{}))}
	for _, record := range book.Query(accounting.QueryFilter{}) {
		restored.estimatedError += record.EstimatedError
	}
	for _, escrow := range snapshot.Escrows {
		restored.retired = restored.retired || escrow.Retired
	}
	return restored
}

func recordWonRace(t *testing.T, ledger *Recorder, nonce uint64) {
	t.Helper()
	attempt := accounting.Attempt{Nonce: nonce, RequestID: "request", OutputTokens: restartOutputTokens, Sent: true}
	if err := ledger.service.Book.RecordRace(tokenEscrowID, []accounting.Attempt{attempt}); err != nil {
		t.Fatalf("RecordRace() = %v, want nil", err)
	}
}

func closeService(t *testing.T, ledger *Recorder) {
	t.Helper()
	if err := ledger.service.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
}

func assertBothNoncesCounted(t *testing.T, restored restoredLedger, when string) {
	t.Helper()
	if restored.totals.InputTokens != restartBothInputTokens || restored.totals.CountedNonces != 2 {
		t.Fatalf("%s: input_tokens = %d counted_nonces = %d, want %d and 2", when, restored.totals.InputTokens, restored.totals.CountedNonces, restartBothInputTokens)
	}
	if restored.estimatedError != 0 {
		t.Fatalf("%s: estimated_error_tokens = %d, want 0 (one stale nonce files %d)", when, restored.estimatedError, filters.EstimatedPromptTokens(restartInputLength))
	}
}

// Test flow:
//  1. Sweep an escrow holding two started nonces into a ledger backed by a store.
//  2. Finish nonce 1 live and finish and seal nonce 2, as the chain does inside the window after the last sweep.
//  3. Shut down the way the gateway does: the registry hands the session to `EscrowClosing`, then the ledger closes and writes.
//  4. Assert the saved ledger, read with no sweep after it, holds both nonces' real input tokens, no estimated error, and the escrow not retired.
func TestAGracefulShutdownKeepsWhatTheChainRecordedSinceTheLastSweep(t *testing.T) {
	path := DatabasePath(t.TempDir())
	session := twoStartedNonces()
	ledger := storedRecorder(t, path, session.state.Group)
	ledger.sweep(t.Context(), scriptedSource{session: session}, nil)

	session.finishLive(1, restartFirstInputTokens)
	session.finishAndSeal(2, restartSecondInputTokens)
	ledger.EscrowClosing(tokenEscrowID, session)
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	restored := storedLedger(t, path)
	assertBothNoncesCounted(t, restored, "after a graceful restart")
	if restored.retired {
		t.Fatalf("the escrow was saved retired by a shutdown that only closed it")
	}
}

// Test flow:
//  1. Sweep two started nonces into a stored ledger and close it with no last reading, so the save holds both as started.
//  2. Finish nonce 1 live and finish and seal nonce 2 while the gateway is down, reopen the ledger on the same store and sweep once.
//  3. Assert the ledger holds both nonces' real input tokens and no estimated error.
func TestASavedNonceSealedWhileTheGatewayWasDownFoldsTheRecordKeptAtSeal(t *testing.T) {
	path := DatabasePath(t.TempDir())
	session := twoStartedNonces()
	ledger := storedRecorder(t, path, session.state.Group)
	ledger.sweep(t.Context(), scriptedSource{session: session}, nil)
	closeService(t, ledger)

	session.finishLive(1, restartFirstInputTokens)
	session.finishAndSeal(2, restartSecondInputTokens)
	restarted := storedRecorder(t, path, session.state.Group)
	restarted.sweep(t.Context(), scriptedSource{session: session}, nil)
	closeService(t, restarted)

	assertBothNoncesCounted(t, storedLedger(t, path), "after the first sweep of the restart")
}

// Test flow:
//  1. Sweep two started nonces into a stored ledger, record a won race with output tokens on nonce 1, and close it.
//  2. Finish nonce 1 live and finish and seal nonce 2, reopen the ledger and sweep three times with a write after each, then reopen it again and sweep twice more.
//  3. Assert input tokens, counted nonces and output tokens equal one count of each nonce after every reopening.
func TestARestoreAndTheSweepsAfterItCountEveryNonceOnce(t *testing.T) {
	path := DatabasePath(t.TempDir())
	session := twoStartedNonces()
	ledger := storedRecorder(t, path, session.state.Group)
	ledger.sweep(t.Context(), scriptedSource{session: session}, nil)
	recordWonRace(t, ledger, 1)
	closeService(t, ledger)
	session.finishLive(1, restartFirstInputTokens)
	session.finishAndSeal(2, restartSecondInputTokens)

	for reopening, sweeps := range []int{3, 2} {
		restarted := storedRecorder(t, path, session.state.Group)
		for range sweeps {
			restarted.sweep(t.Context(), scriptedSource{session: session}, nil)
			if err := restarted.service.Flush(); err != nil {
				t.Fatalf("Flush() = %v, want nil", err)
			}
		}
		closeService(t, restarted)

		restored := storedLedger(t, path)
		assertBothNoncesCounted(t, restored, fmt.Sprintf("reopening %d", reopening+1))
		if restored.totals.OutputTokens != restartOutputTokens {
			t.Fatalf("reopening %d: output_tokens = %d, want the %d of the one race", reopening+1, restored.totals.OutputTokens, restartOutputTokens)
		}
	}
}

// Test flow:
//  1. Open a stored ledger with the default snapshot interval inside a synctest bubble and record a won race with output tokens on nonce 1.
//  2. Let one snapshot interval pass and record a second race on nonce 2.
//  3. Read the store without closing the ledger, as a kill -9 leaves it, before and after another snapshot interval passes.
//  4. Assert the store held the first race's tokens within the interval, lost only the second until the next write, and held both after it.
func TestACrashLosesAtMostOneSnapshotIntervalOfOutputTokens(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := DatabasePath(t.TempDir())
		session := twoStartedNonces()
		ledger := storedRecorder(t, path, session.state.Group)
		defer closeService(t, ledger)
		recordWonRace(t, ledger, 1)

		time.Sleep(crashLossBound)
		synctest.Wait()
		recordWonRace(t, ledger, 2)
		afterOneInterval := storedLedger(t, path).totals.OutputTokens
		time.Sleep(crashLossBound)
		synctest.Wait()
		afterTwoIntervals := storedLedger(t, path).totals.OutputTokens

		if afterOneInterval != restartOutputTokens {
			t.Fatalf("stored output_tokens = %d one snapshot interval after the first race, want its %d", afterOneInterval, restartOutputTokens)
		}
		if afterTwoIntervals != 2*restartOutputTokens {
			t.Fatalf("stored output_tokens = %d one snapshot interval after the second race, want both races' %d", afterTwoIntervals, 2*restartOutputTokens)
		}
	})
}

// Test flow:
//  1. Call `EscrowClosing` on a nil recorder and on an opened one with a nil session.
//  2. Assert neither panics and the opened ledger read nothing.
func TestClosingWithoutALedgerOrASessionReadsNothing(t *testing.T) {
	t.Parallel()
	var disabled *Recorder
	disabled.EscrowClosing(tokenEscrowID, nil)
	escrow := newLiveEscrow(t, 3)
	ledger := newOpenedLedger(t, escrow)

	ledger.EscrowClosing(tokenEscrowID, nil)

	if totals := foldRecords(ledger.service.Book.Query(accounting.QueryFilter{})); totals.CountedNonces != 0 || totals.InputTokens != 0 {
		t.Fatalf("counted_nonces = %d input_tokens = %d after closing with no session, want 0 and 0", totals.CountedNonces, totals.InputTokens)
	}
}
