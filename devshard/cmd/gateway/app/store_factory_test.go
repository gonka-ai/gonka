package app

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"common/storage/mode"

	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/store/sqlite"
	"devshard/cmd/gateway/store/storetest"
)

func selectPostgres(t *testing.T) {
	t.Helper()
	dsn, err := url.Parse(storetest.FreshPostgresSchema(t, storetest.PostgresDSN(t)))
	if err != nil {
		t.Fatalf("parsing the test DSN: %v", err)
	}
	password, _ := dsn.User.Password()
	t.Setenv(mode.EnvStorageMode, "postgres")
	t.Setenv(env.InstanceIDVariable, "gw-test")
	t.Setenv("PGHOST", dsn.Hostname())
	t.Setenv("PGPORT", dsn.Port())
	t.Setenv("PGUSER", dsn.User.Username())
	t.Setenv("PGPASSWORD", password)
	t.Setenv("PGDATABASE", dsn.Path[1:])
	t.Setenv("PGSSLMODE", "disable")
	t.Setenv("PGOPTIONS", "-c search_path="+dsn.Query().Get("search_path"))
}

// Test flow:
//  1. Leave the storage mode unset and choose the store on a directory that does not exist yet.
//  2. Assert the choice is SQLite carrying that directory, and opening it creates the database there.
func TestTheStoreIsSQLiteWhenTheModeIsUnset(t *testing.T) {
	t.Setenv(mode.EnvStorageMode, "")
	storageDir := filepath.Join(t.TempDir(), "state")
	choice, err := chooseStore(&storageDir)
	if err != nil || choice != (sqliteChoice{directory: storageDir}) {
		t.Fatalf("chooseStore() = %+v, %v, want SQLite on %s", choice, err, storageDir)
	}
	opened, err := choice.open(context.Background())
	if err != nil {
		t.Fatalf("open() = %v, want nil", err)
	}
	defer opened.Close()
	if _, err := os.Stat(sqlite.DatabasePath(storageDir)); err != nil {
		t.Fatalf("no gateway database under the storage directory: %v", err)
	}
}

// Test flow:
//  1. Select hybrid mode and choose the store.
//  2. Assert the choice is refused.
func TestAModeTheGatewayDoesNotSupportChoosesNoStore(t *testing.T) {
	t.Setenv(mode.EnvStorageMode, "hybrid")
	storageDir := t.TempDir()
	if _, err := chooseStore(&storageDir); err == nil {
		t.Fatal("chooseStore() = nil, want a refusal")
	}
}

// Test flow:
//  1. Select Postgres and point the storage directory at a path that does not exist.
//  2. Assert the choice is Postgres for the instance.
//  3. Assert the store opens on the empty Postgres database and the storage path was never created.
func TestPostgresChoiceNeverTouchesTheStorageDirectory(t *testing.T) {
	selectPostgres(t)
	storageDir := filepath.Join(t.TempDir(), "state")
	choice, err := chooseStore(&storageDir)
	if err != nil || choice != (postgresChoice{instanceID: "gw-test"}) {
		t.Fatalf("chooseStore() = %+v, %v, want Postgres for gw-test", choice, err)
	}
	opened, err := choice.open(context.Background())
	if err != nil {
		t.Fatalf("open() = %v, want nil", err)
	}
	opened.Close()
	if _, err := os.Stat(storageDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat(storage directory) = %v, want it not to exist", err)
	}
}

// Test flow:
//  1. Prepare a storage directory nested under two directories that do not exist yet.
//  2. Assert the path comes back unchanged and the directory exists afterwards.
func TestPrepareStorageDirCreatesTheDirectory(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "a", "state")
	got, err := prepareStorageDir(&storageDir)
	if err != nil || got != storageDir {
		t.Fatalf("prepareStorageDir() = %q, %v, want %q", got, err, storageDir)
	}
	if info, err := os.Stat(storageDir); err != nil || !info.IsDir() {
		t.Fatalf("stat(storage directory) = %v, %v, want a directory", info, err)
	}
}
