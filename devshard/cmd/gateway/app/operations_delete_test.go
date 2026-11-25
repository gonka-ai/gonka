package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/store"
)

func requireStoredDevshard(t *testing.T, records *store.Store, escrowID string) {
	t.Helper()
	if _, err := findDevshard(context.Background(), records, escrowID); err != nil {
		t.Errorf("escrow %s's row is gone: %v", escrowID, err)
	}
}

func requireFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("escrow storage %s is gone: %v", path, err)
	}
}

// Test flow:
//  1. Register escrow 42 inactive and write its epoch file.
//  2. Delete escrow 42.
//  3. Assert the delete succeeds, the row is gone and the storage directory no longer exists.
func TestDeletingAnInactiveEscrowRemovesItsRowAndItsStorage(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
	records := openedStore(t)
	storageDir := t.TempDir()
	operator := &operations{store: records, escrows: servingRegistry(t), storageDir: storageDir}
	if err := operator.AddDevshard(ctx, api.AddDevshardRequest{EscrowID: "42", Model: "model-a", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY"}); err != nil {
		t.Fatalf("AddDevshard(inactive) = %v", err)
	}
	liveSession(t, storageDir, "42")

	if err := operator.DeleteDevshard(ctx, "42"); err != nil {
		t.Fatalf("DeleteDevshard = %v", err)
	}

	if _, err := findDevshard(ctx, records, "42"); !errors.Is(err, escrow.ErrUnknownEscrow) {
		t.Errorf("row lookup after delete = %v, want ErrUnknownEscrow", err)
	}
	if _, err := os.Stat(api.DevshardStoragePath(storageDir, "42")); !os.IsNotExist(err) {
		t.Errorf("escrow 42's storage survived the delete: %v", err)
	}
}

// Test flow:
//  1. Register escrow 42 inactive, write its epoch file, and take its write lock as an operator Activate does.
//  2. Start a delete of escrow 42 and wait until it blocks on the lock, failing if it finishes first.
//  3. Activate the row and route the escrow under the lock, then release it.
//  4. Assert the delete returns ErrDevshardBusy and escrow 42 still routes, with its row and its epoch file.
func TestADeleteWaitsForAnActivateHoldingTheEscrowsLock(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
	records := openedStore(t)
	storageDir := t.TempDir()
	escrows := servingRegistry(t)
	operator := &operations{store: records, escrows: escrows, storageDir: storageDir}
	if err := operator.AddDevshard(ctx, api.AddDevshardRequest{EscrowID: "42", Model: "model-a", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY"}); err != nil {
		t.Fatalf("AddDevshard(inactive) = %v", err)
	}
	livePath := liveSession(t, storageDir, "42")
	unlock := operator.lockWrites("42")

	deleted := make(chan error, 1)
	go func() { deleted <- operator.DeleteDevshard(ctx, "42") }()
	awaitBlockedInLockWrites(t, "(*operations).DeleteDevshard", deleted, unlock)
	_, activateErr := records.ActivateDevshardUnlessParked(ctx, "42")
	addErr := escrows.Add(ctx, "42", "model-a")
	unlock()
	err := <-deleted

	if activateErr != nil || addErr != nil {
		t.Errorf("activating under the lock: row %v, routing %v", activateErr, addErr)
	}
	if !errors.Is(err, escrow.ErrDevshardBusy) {
		t.Errorf("DeleteDevshard(activated meanwhile) = %v, want ErrDevshardBusy", err)
	}
	if _, routable := escrows.Routable("42"); !routable {
		t.Error("escrow 42 is not routable after the activate")
	}
	requireStoredDevshard(t, records, "42")
	requireFile(t, livePath)
}

// Test flow:
//  1. Register escrow 42 active, write its epoch file, and hold its session for settlement as an admin finalize or state read does.
//  2. Deactivate escrow 42, so its row is inactive and its session drains idle under the hold.
//  3. Delete escrow 42.
//  4. Assert the delete returns ErrDevshardBusy and the row and the epoch file are still there.
func TestADeleteRefusesAnEscrowWhoseSessionIsHeldForSettlement(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
	records := openedStore(t)
	storageDir := t.TempDir()
	escrows := servingRegistry(t)
	operator := &operations{store: records, escrows: escrows, storageDir: storageDir}
	if err := operator.AddDevshard(ctx, api.AddDevshardRequest{EscrowID: "42", Model: "model-a", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Activate: true}); err != nil {
		t.Fatalf("AddDevshard(active) = %v", err)
	}
	livePath := liveSession(t, storageDir, "42")
	_, release, held := escrows.HoldSettlement("42")
	if !held {
		t.Fatal("HoldSettlement(42) = false")
	}
	defer release()
	if err := operator.Deactivate(ctx, "42"); err != nil {
		t.Fatalf("Deactivate = %v", err)
	}

	err := operator.DeleteDevshard(ctx, "42")

	if !errors.Is(err, escrow.ErrDevshardBusy) {
		t.Errorf("DeleteDevshard(held for settlement) = %v, want ErrDevshardBusy", err)
	}
	requireStoredDevshard(t, records, "42")
	requireFile(t, livePath)
}

// Test flow:
//  1. Store an active row for escrow 42, park it for settlement, and write its epoch file.
//  2. Delete escrow 42.
//  3. Assert the delete returns ErrDevshardBusy saying the escrow is parked for settlement, and the row and the epoch file are still there.
func TestADeleteRefusesAnEscrowParkedForSettlement(t *testing.T) {
	ctx := context.Background()
	records := openedStore(t)
	storageDir := t.TempDir()
	if err := records.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "42", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Model: "model-a", Active: true}); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	if err := records.ParkForSettlement(ctx, "42"); err != nil {
		t.Fatalf("ParkForSettlement(): %v", err)
	}
	livePath := liveSession(t, storageDir, "42")
	operator := &operations{store: records, escrows: servingRegistry(t), storageDir: storageDir}

	err := operator.DeleteDevshard(ctx, "42")

	if !errors.Is(err, escrow.ErrDevshardBusy) || !strings.Contains(fmt.Sprint(err), "parked for settlement") {
		t.Errorf("DeleteDevshard(parked) = %v, want ErrDevshardBusy, parked for settlement", err)
	}
	requireStoredDevshard(t, records, "42")
	requireFile(t, livePath)
}

// Test flow:
//  1. Open a store holding no row for escrow 42, as after a settle the delete waited behind.
//  2. Delete escrow 42.
//  3. Assert it returns ErrUnknownDevshard.
func TestADeleteOfAGoneRowAnswersUnknown(t *testing.T) {
	operator := &operations{store: openedStore(t), escrows: servingRegistry(t), storageDir: t.TempDir()}

	if err := operator.DeleteDevshard(context.Background(), "42"); !errors.Is(err, api.ErrUnknownDevshard) {
		t.Fatalf("DeleteDevshard(42) = %v, want ErrUnknownDevshard", err)
	}
}

func holdDatabaseWriteLock(t *testing.T, storeDir string) func() {
	t.Helper()
	ctx := context.Background()
	databases, err := filepath.Glob(filepath.Join(storeDir, "*.db"))
	if err != nil || len(databases) != 1 {
		t.Fatalf("Glob(%s/*.db) = %v, %v, want one database", storeDir, databases, err)
	}
	db, err := sql.Open("sqlite", databases[0])
	if err != nil {
		t.Fatalf("sql.Open(): %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn(): %v", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("BEGIN IMMEDIATE: %v", err)
	}
	return func() {
		if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			t.Errorf("ROLLBACK: %v", err)
		}
		if err := errors.Join(conn.Close(), db.Close()); err != nil {
			t.Errorf("closing the second connection: %v", err)
		}
	}
}

// Test flow:
//  1. Register escrow 42 inactive, write its epoch file, and take the database's write lock from a second connection.
//  2. Start a delete of escrow 42 and wait until it is removing the row, failing if it finishes first.
//  3. Try escrow 42's write lock, then release the database's write lock.
//  4. Assert escrow 42's lock was held while the row was being removed, and the delete succeeds and removes the storage.
func TestADeleteHoldsTheEscrowsLockWhileRemovingTheRowAndTheStorage(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
	storeDir := t.TempDir()
	records, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() { _ = records.Close() })
	storageDir := t.TempDir()
	operator := &operations{store: records, escrows: servingRegistry(t), storageDir: storageDir}
	if err := operator.AddDevshard(ctx, api.AddDevshardRequest{EscrowID: "42", Model: "model-a", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY"}); err != nil {
		t.Fatalf("AddDevshard(inactive) = %v", err)
	}
	liveSession(t, storageDir, "42")
	release := holdDatabaseWriteLock(t, storeDir)

	deleted := make(chan error, 1)
	go func() { deleted <- operator.DeleteDevshard(ctx, "42") }()
	awaitRunningIn(t, "(*Store).DeleteDevshard", deleted, release)
	lock, _ := operator.writes.Load("42")
	writes := lock.(*sync.Mutex)
	lockHeld := !writes.TryLock()
	if !lockHeld {
		writes.Unlock()
	}
	release()
	err = <-deleted

	if !lockHeld {
		t.Error("the delete removed escrow 42's row without its write lock held")
	}
	if err != nil {
		t.Fatalf("DeleteDevshard = %v", err)
	}
	if _, err := os.Stat(api.DevshardStoragePath(storageDir, "42")); !os.IsNotExist(err) {
		t.Errorf("escrow 42's storage survived the delete: %v", err)
	}
}
