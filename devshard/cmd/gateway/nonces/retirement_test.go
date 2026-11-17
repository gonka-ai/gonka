package nonces

import (
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/registry"
	"devshard/state"
	"devshard/types"
	"devshard/user"
)

type retiringSession struct {
	registry.EscrowSession
	machine *state.StateMachine
}

func (s retiringSession) SnapshotState() types.EscrowState { return s.machine.SnapshotState() }
func (s retiringSession) UserSession() *user.Session       { return nil }

func newOpenedLedger(t *testing.T, escrow *liveEscrow) *Recorder {
	t.Helper()
	service, err := accounting.NewService(accounting.Settings{Now: func() time.Time { return time.Unix(0, 0).UTC() }})
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	if err := service.Book.OpenEscrow(accounting.EscrowMetadata{
		EscrowID: tokenEscrowID, Model: "llama", CreationEpoch: 7, Slots: escrow.machine.SnapshotState().Group,
	}); err != nil {
		t.Fatalf("OpenEscrow(): %v", err)
	}
	return recorderFor(service)
}

// Test flow:
//  1. Start and finish one inference on a live 3-slot escrow.
//  2. Retire the escrow through `ledger.EscrowRetiring`.
//  3. Assert the folded records' input tokens equal the finish's own count and counted nonces equal 1, so retirement counts what the chain finished since the last sweep.
func TestRetiringAnEscrowCountsWhatTheChainFinishedSinceTheLastSweep(t *testing.T) {
	t.Parallel()
	escrow := newLiveEscrow(t, 3)
	ledger := newOpenedLedger(t, escrow)
	inference := escrow.start(t, 60, 64)
	escrow.finish(t, inference, 60, 64, 57, 31)

	ledger.EscrowRetiring(tokenEscrowID, retiringSession{machine: escrow.machine})

	totals := foldRecords(ledger.service.Book.Query(accounting.QueryFilter{}))
	if totals.InputTokens != 57 {
		t.Fatalf("input_tokens = %d after the escrow retired, want the 57 the finish carried", totals.InputTokens)
	}
	if totals.CountedNonces != 1 {
		t.Fatalf("counted_nonces = %d, want the one inference the chain finished", totals.CountedNonces)
	}
}

// Test flow:
//  1. Retire a freshly opened escrow with no traffic through `ledger.EscrowRetiring`.
//  2. Assert every record's latest-nonce entries are marked retired.
func TestARetiredEscrowIsMarkedRetiredInTheLedger(t *testing.T) {
	t.Parallel()
	escrow := newLiveEscrow(t, 3)
	ledger := newOpenedLedger(t, escrow)

	ledger.EscrowRetiring(tokenEscrowID, retiringSession{machine: escrow.machine})

	for _, record := range ledger.service.Book.Query(accounting.QueryFilter{}) {
		for _, latest := range record.LatestNonces {
			if !latest.Retired {
				t.Fatalf("escrow %s is still serving in the ledger after it retired", latest.EscrowID)
			}
		}
	}
}

// Test flow:
//  1. Retire an escrow by passing a nil session to `ledger.EscrowRetiring`.
//  2. Assert the ledger still holds records for the escrow.
//  3. Assert every one of them is marked retired, so a session the registry could not hand back still leaves the ledger consistent.
func TestRetiringWithoutASessionStillRetiresTheEscrow(t *testing.T) {
	t.Parallel()
	escrow := newLiveEscrow(t, 3)
	ledger := newOpenedLedger(t, escrow)

	ledger.EscrowRetiring(tokenEscrowID, nil)

	records := ledger.service.Book.Query(accounting.QueryFilter{})
	if len(records) == 0 {
		t.Fatal("the ledger forgot the escrow it retired")
	}
	for _, record := range records {
		for _, latest := range record.LatestNonces {
			if !latest.Retired {
				t.Fatalf("escrow %s is still serving in the ledger after it retired", latest.EscrowID)
			}
		}
	}
}

// Test flow:
//  1. Bind `EscrowRetiring` from a nil `*Recorder`.
//  2. Call the bound method with a nil session.
//  3. Assert it does not panic.
func TestARecorderThatWasNeverOpenedStillAnswersARetirement(t *testing.T) {
	t.Parallel()
	var ledger *Recorder

	retiring := ledger.EscrowRetiring

	retiring(tokenEscrowID, nil)
}

// Test flow:
//  1. Open an escrow in a ledger backed by a store, inside a synctest bubble.
//  2. Retire it through `ledger.EscrowRetiring` and wait for the ledger's own goroutine to go idle.
//  3. Assert the store holds the escrow marked retired, so a retirement reaches disk without the caller writing it.
func TestARetirementIsWrittenToTheStoreByTheLedgerItself(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, err := accounting.OpenStore(filepath.Join(t.TempDir(), "accounting.db"))
		if err != nil {
			t.Fatalf("OpenStore() = %v, want nil", err)
		}
		service, err := accounting.NewService(accounting.Settings{Store: store, Now: func() time.Time { return time.Unix(0, 0).UTC() }})
		if err != nil {
			t.Fatalf("NewService() = %v, want nil", err)
		}
		t.Cleanup(func() { _ = service.Close() })
		if err := service.Book.OpenEscrow(accounting.EscrowMetadata{
			EscrowID: tokenEscrowID, Model: "llama", CreationEpoch: 7, Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1validator"}},
		}); err != nil {
			t.Fatalf("OpenEscrow() = %v, want nil", err)
		}
		ledger := recorderFor(service)

		ledger.EscrowRetiring(tokenEscrowID, nil)
		synctest.Wait()

		stored, err := store.Load(t.Context())
		if err != nil {
			t.Fatalf("Load() = %v, want nil", err)
		}
		if len(stored.Escrows) != 1 || !stored.Escrows[0].Retired {
			t.Fatalf("stored escrows = %+v, want %s stored retired", stored.Escrows, tokenEscrowID)
		}
	})
}
