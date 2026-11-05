package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/store"
)

const (
	importedEpochFile = "epoch_7.db"
	liveSessionState  = "LIVE-STATE"
	importedState     = "IMPORTED-STATE"
)

func sessionSource(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, importedEpochFile), []byte(importedState), storageFileMode); err != nil {
		t.Fatalf("WriteFile(source): %v", err)
	}
	return source
}

func liveSession(t *testing.T, storageDir, escrowID string) string {
	t.Helper()
	live := api.DevshardStoragePath(storageDir, escrowID)
	if err := os.MkdirAll(live, storageDirectoryMode); err != nil {
		t.Fatalf("MkdirAll(live): %v", err)
	}
	livePath := filepath.Join(live, importedEpochFile)
	if err := os.WriteFile(livePath, []byte(liveSessionState), storageFileMode); err != nil {
		t.Fatalf("WriteFile(live): %v", err)
	}
	return livePath
}

func fileContents(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(contents)
}

// Test flow:
//  1. Store an active row for escrow 42, park it for settlement, and write its live epoch file.
//  2. Import escrow 42 from a source directory holding a different epoch file.
//  3. Assert the import returns ErrDevshardNotActivatable.
//  4. Assert the parked escrow's epoch file still holds its live state.
func TestARefusedImportLeavesTheParkedEscrowsStorageAlone(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
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

	err := operator.ImportDevshard(ctx, api.ImportDevshardRequest{EscrowID: "42", SourcePath: sessionSource(t), PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Model: "model-a"})

	if !errors.Is(err, api.ErrDevshardNotActivatable) {
		t.Fatalf("ImportDevshard(parked) = %v, want ErrDevshardNotActivatable", err)
	}
	if got := fileContents(t, livePath); got != liveSessionState {
		t.Errorf("parked escrow's epoch file = %q, want %q", got, liveSessionState)
	}
}

// Test flow:
//  1. Store an active row for escrow 42 and write its live epoch file.
//  2. Import escrow 42 from a source directory holding a different epoch file.
//  3. Assert the import returns ErrDevshardExists.
//  4. Assert the serving escrow's epoch file still holds its live state.
func TestAnImportOfAServingEscrowIsRefusedBeforeItsStorageIsTouched(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
	records := openedStore(t)
	storageDir := t.TempDir()
	if err := records.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "42", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Model: "model-a", Active: true}); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	livePath := liveSession(t, storageDir, "42")
	operator := &operations{store: records, escrows: servingRegistry(t), storageDir: storageDir}

	err := operator.ImportDevshard(ctx, api.ImportDevshardRequest{EscrowID: "42", SourcePath: sessionSource(t), PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Model: "model-a"})

	if !errors.Is(err, api.ErrDevshardExists) {
		t.Fatalf("ImportDevshard(serving) = %v, want ErrDevshardExists", err)
	}
	if got := fileContents(t, livePath); got != liveSessionState {
		t.Errorf("serving escrow's epoch file = %q, want %q", got, liveSessionState)
	}
}

// Test flow:
//  1. Leave the key variable unset and open an empty storage dir.
//  2. Import escrow 42 naming that variable.
//  3. Assert the import fails.
//  4. Assert the storage dir is still empty.
func TestAnImportWithoutItsKeyWritesNothing(t *testing.T) {
	ctx := context.Background()
	records := openedStore(t)
	storageDir := t.TempDir()
	operator := &operations{store: records, escrows: servingRegistry(t), storageDir: storageDir}

	err := operator.ImportDevshard(ctx, api.ImportDevshardRequest{EscrowID: "42", SourcePath: sessionSource(t), PrivateKeyEnv: "DEVSHARD_IMPORT_UNSET_KEY", Model: "model-a"})

	if err == nil {
		t.Fatal("ImportDevshard(no key) = nil, want an error")
	}
	entries, readErr := os.ReadDir(storageDir)
	if readErr != nil {
		t.Fatalf("ReadDir(storage): %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("storage dir holds %d entries after a refused import, want 0", len(entries))
	}
}

// Test flow:
//  1. Open a storage dir inside a parent directory and set the key variable.
//  2. Import each id that is not a canonical decimal chain escrow id, the path traversal ones included.
//  3. Assert each import returns ErrInvalidEscrowID.
//  4. Assert nothing was written beside or inside the storage dir and no row was stored.
func TestAnImportWhoseIDIsNotAChainEscrowIDWritesNothing(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
	records := openedStore(t)
	parent := t.TempDir()
	storageDir := filepath.Join(parent, "storage")
	if err := os.MkdirAll(storageDir, storageDirectoryMode); err != nil {
		t.Fatalf("MkdirAll(storage): %v", err)
	}
	operator := &operations{store: records, escrows: servingRegistry(t), storageDir: storageDir}

	for _, escrowID := range []string{"1/../../outside", "../outside", "1/2", "escrow-1", "007", "+7", " 7", "-1", "18446744073709551616"} {
		err := operator.ImportDevshard(ctx, api.ImportDevshardRequest{EscrowID: escrowID, SourcePath: sessionSource(t), PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Model: "model-a"})
		if !errors.Is(err, api.ErrInvalidEscrowID) {
			t.Errorf("ImportDevshard(%q) = %v, want ErrInvalidEscrowID", escrowID, err)
		}
	}

	for _, dir := range []string{parent, storageDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", dir, err)
		}
		for _, entry := range entries {
			if dir == parent && entry.Name() == "storage" {
				continue
			}
			t.Errorf("refused imports left %s in %s", entry.Name(), dir)
		}
	}
	known, err := records.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards(): %v", err)
	}
	if len(known) != 0 {
		t.Errorf("refused imports stored %d rows, want 0", len(known))
	}
}

// Test flow:
//  1. Store an inactive row for escrow 42 and write its epoch file.
//  2. Import escrow 42 from a source directory holding a different epoch file.
//  3. Assert the import succeeds.
//  4. Assert the epoch file now holds the imported state.
func TestReimportingAnInactiveEscrowReplacesItsStorage(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
	records := openedStore(t)
	storageDir := t.TempDir()
	if err := records.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "42", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Model: "model-a"}); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	livePath := liveSession(t, storageDir, "42")
	operator := &operations{store: records, escrows: servingRegistry(t), storageDir: storageDir}

	if err := operator.ImportDevshard(ctx, api.ImportDevshardRequest{EscrowID: "42", SourcePath: sessionSource(t), PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Model: "model-a"}); err != nil {
		t.Fatalf("ImportDevshard(inactive) = %v, want nil", err)
	}

	if got := fileContents(t, livePath); got != importedState {
		t.Errorf("epoch file = %q, want %q", got, importedState)
	}
}

// Test flow:
//  1. Register escrow 42 active, so the registry routes it.
//  2. Register escrow 42 again with Activate false.
//  3. Assert the registry no longer routes escrow 42 and its stored row is inactive.
func TestRegisteringALiveEscrowInactiveStopsRoutingIt(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
	records := openedStore(t)
	escrows := servingRegistry(t)
	operator := &operations{store: records, escrows: escrows, storageDir: t.TempDir()}
	if err := operator.AddDevshard(ctx, api.AddDevshardRequest{EscrowID: "42", Model: "model-a", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Activate: true}); err != nil {
		t.Fatalf("AddDevshard(active) = %v, want nil", err)
	}

	if err := operator.AddDevshard(ctx, api.AddDevshardRequest{EscrowID: "42", Model: "model-a", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY"}); err != nil {
		t.Fatalf("AddDevshard(inactive) = %v, want nil", err)
	}

	if _, routable := escrows.Routable("42"); routable {
		t.Error("escrow 42 is still routable after an inactive registration")
	}
	if storedDevshard(t, records, "42").Active {
		t.Error("escrow 42's row is active after an inactive registration")
	}
}

// Test flow:
//  1. Store an inactive row for escrow 42, publish it in the registry anyway, and write its live epoch file.
//  2. Import escrow 42 from a source directory holding a different epoch file.
//  3. Assert the import returns ErrDevshardExists.
//  4. Assert the routed escrow's epoch file still holds its live state.
func TestAnImportOfAnEscrowTheRegistryRoutesIsRefusedWhateverItsRow(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
	records := openedStore(t)
	storageDir := t.TempDir()
	if err := records.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "42", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Model: "model-a"}); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	escrows := servingRegistry(t)
	if err := escrows.Add(ctx, "42", "model-a"); err != nil {
		t.Fatalf("Add(): %v", err)
	}
	livePath := liveSession(t, storageDir, "42")
	operator := &operations{store: records, escrows: escrows, storageDir: storageDir}

	err := operator.ImportDevshard(ctx, api.ImportDevshardRequest{EscrowID: "42", SourcePath: sessionSource(t), PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Model: "model-a"})

	if !errors.Is(err, api.ErrDevshardExists) {
		t.Fatalf("ImportDevshard(routed) = %v, want ErrDevshardExists", err)
	}
	if got := fileContents(t, livePath); got != liveSessionState {
		t.Errorf("routed escrow's epoch file = %q, want %q", got, liveSessionState)
	}
}

// Test flow:
//  1. Register escrow 42 active, write its live epoch file, and hold its session for settlement as an admin finalize or state read does.
//  2. Deactivate escrow 42, then import it from a source holding a different epoch file.
//  3. Assert the import returns ErrDevshardExists.
//  4. Assert the held session's epoch file still holds its live state.
func TestAnImportRefusesAnEscrowWhoseSessionIsHeldForSettlement(t *testing.T) {
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

	err := operator.ImportDevshard(ctx, api.ImportDevshardRequest{EscrowID: "42", SourcePath: sessionSource(t), PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Model: "model-a"})

	if !errors.Is(err, api.ErrDevshardExists) {
		t.Errorf("ImportDevshard(held for settlement) = %v, want ErrDevshardExists", err)
	}
	if got := fileContents(t, livePath); got != liveSessionState {
		t.Errorf("held session's epoch file = %q, want %q", got, liveSessionState)
	}
}
