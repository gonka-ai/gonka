package accounting

import (
	"context"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

const otherEscrow = "escrow-2"

func storedLatestNonce(t *testing.T, store *Store, escrowID string) uint64 {
	t.Helper()
	var latest uint64
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT latest_nonce FROM accounting_escrows WHERE escrow_id = ?`, escrowID).Scan(&latest); err != nil {
		t.Fatalf("reading latest_nonce of %s: %v", escrowID, err)
	}
	return latest
}

// Test flow:
//  1. Open two escrows and save the book.
//  2. Overwrite the first escrow's stored watermark behind the book's back, then change only the second escrow.
//  3. Save again and assert the first escrow's row was left alone while the second escrow's change landed.
func TestASaveWritesOnlyTheEscrowsChangedSinceTheLastSave(t *testing.T) {
	book := newTestBook(t, 4)
	openTestEscrow(t, book, otherEscrow, testEpoch, 4)
	store := openTestStore(t)
	if err := store.Save(t.Context(), book); err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}
	if _, err := store.db.ExecContext(t.Context(),
		`UPDATE accounting_escrows SET latest_nonce = 999 WHERE escrow_id = ?`, testEscrow); err != nil {
		t.Fatalf("marking the stored row: %v", err)
	}
	if err := book.ObserveLatestNonce(otherEscrow, 7); err != nil {
		t.Fatalf("ObserveLatestNonce() = %v, want nil", err)
	}

	if err := store.Save(t.Context(), book); err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}

	if got := storedLatestNonce(t, store, testEscrow); got != 999 {
		t.Fatalf("stored latest_nonce of the unchanged escrow = %d, want 999", got)
	}
	if got := storedLatestNonce(t, store, otherEscrow); got != 7 {
		t.Fatalf("stored latest_nonce of the changed escrow = %d, want 7", got)
	}
}

// Test flow:
//  1. Open two escrows in different epochs and save the book.
//  2. Reset the first escrow's epoch, save, and reload.
//  3. Assert only the second escrow comes back.
func TestAnEscrowDroppedFromTheBookLeavesTheStore(t *testing.T) {
	book := newTestBook(t, 4)
	openTestEscrow(t, book, otherEscrow, testEpoch+1, 4)
	store := openTestStore(t)
	if err := store.Save(t.Context(), book); err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}
	book.ResetEpoch(testEpoch)

	restored := saveAndReload(t, book, store)

	if got := restored.EscrowIDs(); len(got) != 1 || got[0] != otherEscrow {
		t.Fatalf("restored escrows = %v, want [%s]", got, otherEscrow)
	}
}

// Test flow:
//  1. Open an escrow, save, and change its watermark.
//  2. Save with a cancelled context, then save again with a live one.
//  3. Assert the change the failed save carried reached the store.
func TestAFailedSaveKeepsItsChangesForTheNextSave(t *testing.T) {
	book := newTestBook(t, 4)
	store := openTestStore(t)
	if err := store.Save(t.Context(), book); err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}
	if err := book.ObserveLatestNonce(testEscrow, 11); err != nil {
		t.Fatalf("ObserveLatestNonce() = %v, want nil", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.Save(cancelled, book); err == nil {
		t.Fatal("Save(cancelled) = nil, want an error")
	}

	if err := store.Save(t.Context(), book); err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}

	if got := storedLatestNonce(t, store, testEscrow); got != 11 {
		t.Fatalf("stored latest_nonce = %d, want 11", got)
	}
}

// Test flow:
//  1. Save a book with one escrow and restore it into a fresh book.
//  2. Save the restored book into a second, empty store.
//  3. Assert the second store holds the escrow, because a restore is itself a change to write.
func TestARestoredBookWritesEverythingOnItsFirstSave(t *testing.T) {
	book := newTestBook(t, 4)
	restored := saveAndReload(t, book, openTestStore(t))

	second := openTestStore(t)
	again := saveAndReload(t, restored, second)

	if got := again.EscrowIDs(); len(got) != 1 || got[0] != testEscrow {
		t.Fatalf("escrows in the second store = %v, want [%s]", got, testEscrow)
	}
}

// Test flow:
//  1. Build a service over a store and open an escrow in its book.
//  2. Request a flush and wait for the service's own goroutine to go idle.
//  3. Assert the escrow reached the store without the caller writing it.
func TestARequestedFlushIsWrittenByTheServiceItself(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := openTestStore(t)
		service, err := NewService(Settings{Store: store, Now: func() time.Time { return time.Unix(0, 0).UTC() }})
		if err != nil {
			t.Fatalf("NewService() = %v, want nil", err)
		}
		t.Cleanup(func() { _ = service.Close() })
		openTestEscrow(t, service.Book, testEscrow, testEpoch, 4)
		if err := service.Book.ObserveLatestNonce(testEscrow, 5); err != nil {
			t.Fatalf("ObserveLatestNonce() = %v, want nil", err)
		}

		service.RequestFlush()
		synctest.Wait()

		if got := storedLatestNonce(t, store, testEscrow); got != 5 {
			t.Fatalf("stored latest_nonce = %d, want 5", got)
		}
	})
}

// Test flow:
//  1. Save a book holding escrow-1, then corrupt the stored updated_at so the next load fails.
//  2. Start a service over that store, which starts empty, open escrow-2 and close the service.
//  3. Reopen the store and assert it holds only escrow-2, as a ledger that started empty must.
func TestALedgerThatFailedToLoadIsReplacedWholeByTheFirstSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() = %v, want nil", err)
	}
	if err := store.Save(t.Context(), newTestBook(t, 4)); err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}
	if _, err := store.db.ExecContext(t.Context(),
		`UPDATE accounting_meta SET value = 'garbage' WHERE key = ?`, metaUpdatedAt); err != nil {
		t.Fatalf("corrupting updated_at: %v", err)
	}
	service, err := NewService(Settings{Store: store, Now: func() time.Time { return time.Unix(0, 0).UTC() }})
	if err == nil {
		t.Fatal("NewService() over a corrupt ledger = nil, want the load error")
	}
	openTestEscrow(t, service.Book, otherEscrow, testEpoch, 4)
	if err := service.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() = %v, want nil", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	snapshot, err := reopened.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}

	if len(snapshot.Escrows) != 1 || snapshot.Escrows[0].Metadata.EscrowID != otherEscrow {
		t.Fatalf("stored escrows = %d, want only %s", len(snapshot.Escrows), otherEscrow)
	}
}

// Test flow:
//  1. Open and retire escrow-1 in an old epoch, open escrow-2 in the current one, and save.
//  2. Prune the epochs before the current one, save and reload.
//  3. Assert only escrow-2 comes back.
func TestAPrunedEscrowLeavesTheStore(t *testing.T) {
	book := newTestBook(t, 4)
	book.RetireEscrow(testEscrow)
	openTestEscrow(t, book, otherEscrow, testEpoch+5, 4)
	store := openTestStore(t)
	if err := store.Save(t.Context(), book); err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}
	book.PruneBefore(testEpoch + 5)

	restored := saveAndReload(t, book, store)

	if got := restored.EscrowIDs(); len(got) != 1 || got[0] != otherEscrow {
		t.Fatalf("restored escrows = %v, want [%s]", got, otherEscrow)
	}
}

// Test flow:
//  1. Save a book holding escrow-1, corrupt the stored updated_at, and restore a fresh book from it, which fails.
//  2. Open escrow-2, save it with a cancelled context, then save again with a live one.
//  3. Assert the store holds only escrow-2: the failed save kept the duty to replace the ledger whole.
func TestAFailedSaveKeepsTheDutyToReplaceALedgerThatFailedToLoad(t *testing.T) {
	store := openTestStore(t)
	if err := store.Save(t.Context(), newTestBook(t, 4)); err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}
	if _, err := store.db.ExecContext(t.Context(),
		`UPDATE accounting_meta SET value = 'garbage' WHERE key = ?`, metaUpdatedAt); err != nil {
		t.Fatalf("corrupting updated_at: %v", err)
	}
	service, err := NewService(Settings{Store: store, Now: func() time.Time { return time.Unix(0, 0).UTC() }})
	if err == nil {
		t.Fatal("NewService() over a corrupt ledger = nil, want the load error")
	}
	service.cancel()
	<-service.stopped
	openTestEscrow(t, service.Book, otherEscrow, testEpoch, 4)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.Save(cancelled, service.Book); err == nil {
		t.Fatal("Save(cancelled) = nil, want an error")
	}

	if err := store.Save(t.Context(), service.Book); err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}

	stored, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	if len(stored.Escrows) != 1 || stored.Escrows[0].Metadata.EscrowID != otherEscrow {
		t.Fatalf("stored escrows = %d, want only %s", len(stored.Escrows), otherEscrow)
	}
}
