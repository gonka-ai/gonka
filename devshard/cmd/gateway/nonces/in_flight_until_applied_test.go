package nonces

import (
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/registry"
	"devshard/types"
	"devshard/user"
)

// wonRace is the gateway reporting a race its nonce won, as it does the moment the client has its answer.
func wonRace(t *testing.T, nonce uint64) func(*accounting.Book) {
	t.Helper()
	return func(book *accounting.Book) {
		if err := book.RecordAssigned(tokenEscrowID, nonce, "request-1"); err != nil {
			t.Fatalf("RecordAssigned(): %v", err)
		}
		if err := book.RecordRace(tokenEscrowID, []accounting.Attempt{{
			Nonce: nonce, RequestID: "request-1", Sent: true, Acknowledged: true,
			Usage: accounting.UsageWinner, Terminal: "won",
		}}); err != nil {
			t.Fatalf("RecordRace(): %v", err)
		}
	}
}

// Test flow:
//  1. Start and confirm an inference on a live escrow, and report its race as won: the client has its answer and the host's Finish sits in the gateway's mempool, not in the escrow's log.
//  2. Observe the escrow the way the sweep does.
//  3. Assert the nonce is still in flight and counted under no finished disposition.
func TestAFinishTheChainHasNotAppliedKeepsItsNonceInFlight(t *testing.T) {
	t.Parallel()
	escrow := newLiveEscrow(t, 3)
	nonce := escrow.start(t, 60, 64)
	escrow.confirm(t, nonce, 60, 64)

	observed := observedBy(t, escrow, wonRace(t, nonce))

	if observed.InFlight != 1 {
		t.Fatalf("in_flight = %d with the Finish only in the mempool, want 1: the nonce is open until the chain applies it", observed.InFlight)
	}
	if got := observed.Dispositions[accounting.DispositionFinishedUsed]; got != 0 {
		t.Fatalf("finished_used = %d before the Finish landed, want 0", got)
	}
}

// Test flow:
//  1. Start, confirm and finish an inference on a live escrow, so its Finish is applied in the escrow's log, and report its race as won.
//  2. Observe the escrow the way the sweep does.
//  3. Assert the nonce has left flight and is counted once as finished and used.
func TestAFinishTheChainAppliedCountsItsNonceOnce(t *testing.T) {
	t.Parallel()
	escrow := newLiveEscrow(t, 3)
	nonce := escrow.start(t, 60, 64)
	escrow.finish(t, nonce, 60, 64, 50, 10)

	observed := observedBy(t, escrow, wonRace(t, nonce))

	if observed.InFlight != 0 {
		t.Fatalf("in_flight = %d after the Finish landed, want 0", observed.InFlight)
	}
	if got := observed.Dispositions[accounting.DispositionFinishedUsed]; got != 1 {
		t.Fatalf("finished_used = %d after the Finish landed, want 1", got)
	}
}

type sealedSession struct {
	registry.EscrowSession
	sealed  map[uint64]types.InferenceRecord
	lookups map[uint64]int
}

func (s sealedSession) SealedInference(id uint64) (types.InferenceRecord, bool) {
	if s.lookups != nil {
		s.lookups[id]++
	}
	record, kept := s.sealed[id]
	return record, kept
}

func (s sealedSession) UserSession() *user.Session { return nil }

// Test flow:
//  1. Report the races of nonces 4, 5 and 6 as won, as the client has its answers, while the sweep never read any of them live.
//  2. Close sealed nonces against a window that holds none of them: 4 sealed validated, 5 sealed timed out, 6 with no copy kept.
//  3. Assert only the nonce sealed with an applied Finish is counted finished; the others stay where they were.
func TestANonceSealedBetweenSweepsIsClosedByTheRecordKeptAtSeal(t *testing.T) {
	t.Parallel()
	service, err := accounting.NewService(accounting.Settings{Now: func() time.Time { return time.Unix(0, 0).UTC() }})
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	ledger := recorderFor(service)
	escrow := newLiveEscrow(t, 3)
	snapshot := escrow.machine.SnapshotState()
	if err := service.Book.OpenEscrow(accounting.EscrowMetadata{EscrowID: tokenEscrowID, Model: "llama", CreationEpoch: 7, Slots: snapshot.Group}); err != nil {
		t.Fatalf("OpenEscrow(): %v", err)
	}
	for _, nonce := range []uint64{4, 5, 6} {
		wonRace(t, nonce)(service.Book)
	}
	snapshot.LatestNonce = 6

	ledger.closeSealed(tokenEscrowID, snapshot, newSealedRecords(sealedSession{sealed: map[uint64]types.InferenceRecord{
		4: {Status: types.StatusValidated},
		5: {Status: types.StatusTimedOut},
	}}))

	if got := service.Book.UnfinishedNonces(tokenEscrowID); !slices.Equal(got, []uint64{5, 6}) {
		t.Fatalf("UnfinishedNonces() = %v, want [5 6]: only the nonce sealed with an applied Finish is closed", got)
	}
}
