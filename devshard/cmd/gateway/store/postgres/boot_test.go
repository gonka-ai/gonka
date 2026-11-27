package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"devshard/cmd/gateway/store/storetest"
)

const (
	lockHeldFor       = 4 * time.Second
	slowMigrationFor  = 6 * time.Second
	concurrentBoots   = 4
	newerSchemaMarker = 9999
)

func freshDSN(t *testing.T) string {
	t.Helper()
	return storetest.FreshPostgresSchema(t, storetest.PostgresDSN(t))
}

func adminConnection(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	admin, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() { admin.Close(context.Background()) })
	return admin
}

func countRows(t *testing.T, admin *pgx.Conn, query string) int {
	t.Helper()
	var count int
	if err := admin.QueryRow(context.Background(), query).Scan(&count); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return count
}

// Test flow:
//  1. Open a store in an empty schema and pin one suspicious host.
//  2. Close it and open the same schema again.
//  3. Assert the schema version is the migration count with one row per version, the marker exists once, and the host survived.
func TestBootIsIdempotent(t *testing.T) {
	dsn := freshDSN(t)
	ctx := context.Background()
	first, err := openBackend(ctx, Options{ConnectionString: dsn})
	if err != nil {
		t.Fatalf("openBackend(): %v", err)
	}
	if err := first.AddSuspiciousHost(ctx, "host-1"); err != nil {
		t.Fatalf("AddSuspiciousHost(): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	second, err := openBackend(ctx, Options{ConnectionString: dsn})
	if err != nil {
		t.Fatalf("second openBackend(): %v", err)
	}
	defer second.Close()
	admin := adminConnection(t, dsn)
	if versions := countRows(t, admin, `SELECT COUNT(*) FROM schema_version`); versions != len(migrations) {
		t.Fatalf("schema_version rows = %d, want %d", versions, len(migrations))
	}
	if markers := countRows(t, admin, `SELECT COUNT(*) FROM `+markerTable); markers != 1 {
		t.Fatalf("markers = %d, want 1", markers)
	}
	if hosts, _ := second.ListSuspiciousHosts(ctx); !slices.Equal(hosts, []string{"host-1"}) {
		t.Fatalf("hosts = %v, want [host-1]", hosts)
	}
}

// Test flow:
//  1. Create a schema holding a table the gateway did not create, with no marker.
//  2. Open a store on it.
//  3. Assert the open is refused with ErrForeignDatabase and the gateway created no table.
func TestARefusedForeignDatabaseIsLeftAlone(t *testing.T) {
	dsn := freshDSN(t)
	admin := adminConnection(t, dsn)
	if _, err := admin.Exec(context.Background(), `CREATE TABLE devshard_sessions (id BIGINT)`); err != nil {
		t.Fatalf("creating a foreign table: %v", err)
	}
	if _, err := Open(context.Background(), Options{ConnectionString: dsn}); !errors.Is(err, ErrForeignDatabase) {
		t.Fatalf("Open() = %v, want ErrForeignDatabase", err)
	}
	if tables := countRows(t, admin, `SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname = current_schema()`); tables != 1 {
		t.Fatalf("tables after the refusal = %d, want only the foreign one", tables)
	}
}

// Test flow:
//  1. Start several opens of one empty schema at the same time.
//  2. Assert every open succeeds and the schema holds one row per migration version.
func TestConcurrentBootsSerialize(t *testing.T) {
	dsn := freshDSN(t)
	var group sync.WaitGroup
	errs := make(chan error, concurrentBoots)
	for range concurrentBoots {
		group.Go(func() {
			opened, err := Open(context.Background(), Options{ConnectionString: dsn})
			if err == nil {
				err = opened.Close()
			}
			errs <- err
		})
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Open(): %v", err)
		}
	}
	if versions := countRows(t, adminConnection(t, dsn), `SELECT COUNT(*) FROM schema_version`); versions != len(migrations) {
		t.Fatalf("schema_version rows = %d, want %d", versions, len(migrations))
	}
}

// Test flow:
//  1. Open an empty schema, then record a schema version this build does not know.
//  2. Open the schema again.
//  3. Assert the open succeeds and applies no migration twice.
func TestAnOlderBuildBootsOnANewerSchema(t *testing.T) {
	dsn := freshDSN(t)
	first, err := Open(context.Background(), Options{ConnectionString: dsn})
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	first.Close()
	admin := adminConnection(t, dsn)
	if _, err := admin.Exec(context.Background(), `INSERT INTO schema_version (version) VALUES ($1)`, newerSchemaMarker); err != nil {
		t.Fatalf("recording a newer version: %v", err)
	}
	second, err := Open(context.Background(), Options{ConnectionString: dsn})
	if err != nil {
		t.Fatalf("Open() on a newer schema = %v, want nil", err)
	}
	second.Close()
	if versions := countRows(t, admin, `SELECT COUNT(*) FROM schema_version`); versions != len(migrations)+1 {
		t.Fatalf("schema_version rows = %d, want %d", versions, len(migrations)+1)
	}
}

// Test flow:
//  1. Hold the boot's advisory lock from another connection for longer than the pool's lock timeout.
//  2. Open a store while the lock is held.
//  3. Assert the open waits for the lock and succeeds instead of failing on the lock timeout.
func TestBootWaitsForAHeldLockPastTheLockTimeout(t *testing.T) {
	dsn := freshDSN(t)
	holder := adminConnection(t, dsn)
	ctx := context.Background()
	transaction, err := holder.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	if _, err := transaction.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, bootLockName); err != nil {
		t.Fatalf("taking the boot lock: %v", err)
	}
	released := make(chan error, 1)
	go func() {
		_, err := transaction.Exec(ctx, `SELECT pg_sleep($1)`, lockHeldFor.Seconds())
		if err == nil {
			err = transaction.Commit(ctx)
		}
		released <- err
	}()
	opened, err := Open(ctx, Options{ConnectionString: dsn})
	if err != nil {
		t.Fatalf("Open() while the lock was held = %v, want nil after the wait", err)
	}
	opened.Close()
	if err := <-released; err != nil {
		t.Fatalf("holding the lock: %v", err)
	}
}

// Test flow:
//  1. Append a migration that sleeps longer than the pool's statement timeout and open an empty schema.
//  2. Assert the boot succeeds, because it lifts the statement timeout for its own transaction.
func TestBootOutlastsTheStatementTimeout(t *testing.T) {
	dsn := freshDSN(t)
	original := migrations
	migrations = append(slices.Clone(original), fmt.Sprintf(`SELECT pg_sleep(%d)`, int(slowMigrationFor.Seconds())))
	t.Cleanup(func() { migrations = original })
	opened, err := Open(context.Background(), Options{ConnectionString: dsn})
	if err != nil {
		t.Fatalf("Open() with a %v migration = %v, want nil", slowMigrationFor, err)
	}
	opened.Close()
}

// Test flow:
//  1. Open a store and, in a transaction of its pool, turn synchronous_commit off.
//  2. Assert the durability check refuses it with ErrDurabilityOff.
//  3. Turn it on again and assert the check passes.
func TestBootRefusesAServerThatDoesNotFlushCommits(t *testing.T) {
	ctx := context.Background()
	backend, err := openBackend(ctx, Options{ConnectionString: freshDSN(t)})
	if err != nil {
		t.Fatalf("openBackend(): %v", err)
	}
	defer backend.Close()
	for _, testCase := range []struct {
		setting string
		wantOff bool
	}{{"off", true}, {"on", false}} {
		transaction, err := backend.database.pool.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx(): %v", err)
		}
		if _, err := transaction.ExecContext(ctx, `SET LOCAL synchronous_commit = `+testCase.setting); err != nil {
			t.Fatalf("setting synchronous_commit: %v", err)
		}
		err = requireDurableCommits(ctx, transaction)
		transaction.Rollback()
		if errors.Is(err, ErrDurabilityOff) != testCase.wantOff {
			t.Fatalf("synchronous_commit = %s: check = %v, want refusal %v", testCase.setting, err, testCase.wantOff)
		}
	}
}
