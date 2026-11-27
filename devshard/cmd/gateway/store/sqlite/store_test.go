package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/store"
)

func openTestStore(t *testing.T) *Backend {
	t.Helper()
	testStore, err := openBackend(t.TempDir())
	if err != nil {
		t.Fatalf("openBackend(): %v", err)
	}
	t.Cleanup(func() {
		if err := testStore.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	return testStore
}

// Test flow:
//  1. Open a store at a path whose parent directories do not yet exist.
//  2. Assert `Open` succeeds and creates `gateway.db` under that path.
func TestOpenCreatesDatabaseFileAndDirectory(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "nested", "storage")
	testStore, err := openBackend(baseDir)
	if err != nil {
		t.Fatalf("openBackend() with missing directory: %v", err)
	}
	defer testStore.Close()
	if _, err := os.Stat(filepath.Join(baseDir, "gateway.db")); err != nil {
		t.Fatalf("gateway.db not created: %v", err)
	}
}

// Test flow:
//  1. Seed a database file with table names this gateway did not create (`gateway_settings`, `gateway_devshards`, `gateway_rotation_status`) and one row.
//  2. Open the store against that same file.
//  3. Assert `Open` refuses it with `ErrLegacyDatabase`.
func TestOpenRefusesADatabaseThisGatewayDidNotCreate(t *testing.T) {
	dir := t.TempDir()

	seed, err := sql.Open("sqlite", filepath.Join(dir, gatewayDatabaseFileName))
	if err != nil {
		t.Fatalf("opening seed db: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE gateway_settings (id INTEGER PRIMARY KEY CHECK (id = 1), chain_rest TEXT NOT NULL)`,
		`CREATE TABLE gateway_devshards (escrow_id TEXT PRIMARY KEY, model TEXT NOT NULL)`,
		`CREATE TABLE gateway_rotation_status (escrow_id TEXT PRIMARY KEY, state TEXT NOT NULL)`,
		`INSERT INTO gateway_devshards (escrow_id, model) VALUES ('escrow-1', 'model-a')`,
	} {
		if _, err := seed.Exec(statement); err != nil {
			t.Fatalf("seeding a foreign db with %q: %v", statement, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}

	opened, err := openBackend(dir)
	if err == nil {
		opened.Close()
		t.Fatal("openBackend() accepted a gateway.db this gateway did not create, want a refusal")
	}
	if !errors.Is(err, ErrLegacyDatabase) {
		t.Fatalf("openBackend() error = %v, want ErrLegacyDatabase", err)
	}
}

// Test flow:
//  1. Seed a database file at schema version 1 with one legacy devshard row.
//  2. Open the store against that file.
//  3. Assert the v1 devshard survived the upgrade.
//  4. Save and load a commitment through the upgraded store, asserting the v2 table works.
func TestOpenUpgradesExistingV1Database(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	seed, err := sql.Open("sqlite", filepath.Join(dir, gatewayDatabaseFileName))
	if err != nil {
		t.Fatalf("opening seed db: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE schema_version (version INTEGER NOT NULL)`,
		migrations[0],
		`INSERT INTO schema_version (version) VALUES (1)`,
		`INSERT INTO devshards (escrow_id, model) VALUES ('legacy-1', 'm')`,
	} {
		if _, err := seed.Exec(statement); err != nil {
			t.Fatalf("seeding v1 db with %q: %v", statement, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}

	upgraded, err := openBackend(dir)
	if err != nil {
		t.Fatalf("openBackend() upgrading a v1 db: %v", err)
	}
	t.Cleanup(func() { upgraded.Close() })

	devshards, err := upgraded.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards() after upgrade: %v", err)
	}
	if len(devshards) != 1 || devshards[0].EscrowID != "legacy-1" {
		t.Fatalf("v1 data lost on upgrade to v2: %+v", devshards)
	}

	created := time.Unix(0, 123456789).UTC()
	if err := upgraded.SaveCommitment(ctx, store.Commitment{TxHash: "tx-1", Model: "m", CreatedAt: created}); err != nil {
		t.Fatalf("SaveCommitment() into upgraded db: %v", err)
	}
	commitments, err := upgraded.LoadCommitments(ctx)
	if err != nil {
		t.Fatalf("LoadCommitments() after upgrade: %v", err)
	}
	if len(commitments) != 1 || !commitments[0].CreatedAt.Equal(created) {
		t.Fatalf("v2 table unusable after upgrade: %+v", commitments)
	}
}

// Test flow:
//  1. Seed a database the build before the funding planner wrote: the first seven migrations, then its own
//     eighth, `on_hold`, where `chain_epoch` stands now, recorded as version 8, with one devshard row.
//  2. Open the store against that file.
//  3. Assert the rows list: `chain_epoch`, skipped by its version number, was added anyway.
func TestOpenAddsAColumnWhoseVersionAnEarlierBuildSpentOnAnother(t *testing.T) {
	dir := t.TempDir()
	seed, err := sql.Open("sqlite", filepath.Join(dir, gatewayDatabaseFileName))
	if err != nil {
		t.Fatalf("opening seed db: %v", err)
	}
	statements := slices.Concat([]string{`CREATE TABLE schema_version (version INTEGER NOT NULL)`}, migrations[:7], []string{
		`ALTER TABLE devshards ADD COLUMN on_hold INTEGER NOT NULL DEFAULT 0`,
		`INSERT INTO schema_version (version) VALUES (1), (2), (3), (4), (5), (6), (7), (8)`,
		`INSERT INTO devshards (escrow_id, model) VALUES ('held-1', 'm')`,
	})
	for _, statement := range statements {
		if _, err := seed.Exec(statement); err != nil {
			t.Fatalf("seeding the pre-planner db with %q: %v", statement, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}

	upgraded, err := openBackend(dir)
	if err != nil {
		t.Fatalf("openBackend() upgrading the pre-planner db: %v", err)
	}
	t.Cleanup(func() { upgraded.Close() })

	devshards, err := upgraded.ListDevshards(context.Background())
	if err != nil {
		t.Fatalf("ListDevshards() after upgrade: %v", err)
	}
	if len(devshards) != 1 || devshards[0].EscrowID != "held-1" || devshards[0].ChainEpoch != 0 {
		t.Fatalf("devshards after upgrade = %+v, want held-1 with chain_epoch 0", devshards)
	}
}

// Test flow:
//  1. Seed a database with every migration before the one adding input_tokens_source, recorded at that version, and one request row.
//  2. Open the store against that file.
//  3. Assert the row reads back with its input tokens, marked as the byte estimate it was counted as.
func TestARequestRowFromBeforeTheSourceColumnReadsAsTheByteEstimate(t *testing.T) {
	dir := t.TempDir()
	seed, err := sql.Open("sqlite", filepath.Join(dir, gatewayDatabaseFileName))
	if err != nil {
		t.Fatalf("opening seed db: %v", err)
	}
	before := slices.IndexFunc(migrations, func(migration string) bool { return strings.Contains(migration, "ADD COLUMN input_tokens_source") })
	statements := slices.Concat([]string{`CREATE TABLE schema_version (version INTEGER NOT NULL)`}, migrations[:before], []string{
		`WITH RECURSIVE versions(version) AS (SELECT 1 UNION ALL SELECT version + 1 FROM versions WHERE version < ` + strconv.Itoa(before) + `) INSERT INTO schema_version (version) SELECT version FROM versions`,
		`INSERT INTO request_accounting (request_id, input_tokens, recorded_at) VALUES ('request-1', 33, '2026-10-24T00:00:00.000000000Z')`,
	})
	for _, statement := range statements {
		if _, err := seed.Exec(statement); err != nil {
			t.Fatalf("seeding the db with %q: %v", statement, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}

	upgraded, err := openBackend(dir)
	if err != nil {
		t.Fatalf("openBackend() upgrading the db: %v", err)
	}
	t.Cleanup(func() { upgraded.Close() })

	record, found, err := upgraded.FindRequest(context.Background(), "request-1")
	if err != nil || !found {
		t.Fatalf("FindRequest() = %v, %v", found, err)
	}
	if record.InputTokens != 33 || record.InputTokensSource != filters.InputTokensBytesEstimate {
		t.Fatalf("row = %d tokens from %q, want 33 from %q", record.InputTokens, record.InputTokensSource, filters.InputTokensBytesEstimate)
	}
}

// Test flow:
//  1. Query the store's connection for `journal_mode`, `synchronous`, and `busy_timeout`.
//  2. Assert each pragma reports the value the store carries in its DSN for every connection the pool opens, not one applied as a statement that a recreated connection would lose.
func TestEveryConnectionCarriesThePragmas(t *testing.T) {
	gatewayStore := openTestStore(t)

	for _, expected := range []struct {
		pragma string
		want   string
	}{
		{pragma: "journal_mode", want: "wal"},
		{pragma: "synchronous", want: "1"},
		{pragma: "busy_timeout", want: "5000"},
	} {
		var got string
		if err := gatewayStore.db.QueryRow("PRAGMA " + expected.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", expected.pragma, err)
		}
		if !strings.EqualFold(got, expected.want) {
			t.Fatalf("%s = %q, want %q", expected.pragma, got, expected.want)
		}
	}
}

// Test flow:
//  1. Open a store in its own directory and checkpoint it, so the main database file holds everything written so far.
//  2. Save one commitment and assert it reads back.
//  3. Copy the main database file alone into a fresh directory, as a machine that lost the unsynced log would find it, and open a store on it.
//  4. Assert the copy holds the commitment: an acknowledged intent is in the main file, not only in the log `synchronous` NORMAL leaves unsynced.
func TestAnAcknowledgedCommitmentIsInTheMainDatabaseFile(t *testing.T) {
	storageDir := t.TempDir()
	gatewayStore, err := openBackend(storageDir)
	if err != nil {
		t.Fatalf("openBackend(): %v", err)
	}
	t.Cleanup(func() { gatewayStore.Close() })
	if _, err := gatewayStore.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	commitment := store.Commitment{TxHash: "TX-INTENT", Model: "model-a", Role: store.RoleTemp, Epoch: 9, PrivateKeyEnv: "MODEL_A_KEY", BlockHeight: 500, CreatedAt: time.Unix(1_700_000_000, 0).UTC()}
	if err := gatewayStore.SaveCommitment(context.Background(), commitment); err != nil {
		t.Fatalf("SaveCommitment(): %v", err)
	}
	if saved, err := gatewayStore.LoadCommitments(context.Background()); err != nil || len(saved) != 1 {
		t.Fatalf("LoadCommitments() = %+v, %v, want the saved commitment", saved, err)
	}

	mainFile, err := os.ReadFile(filepath.Join(storageDir, gatewayDatabaseFileName))
	if err != nil {
		t.Fatalf("reading the main database file: %v", err)
	}
	survivorDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(survivorDir, gatewayDatabaseFileName), mainFile, 0o600); err != nil {
		t.Fatalf("writing the copy: %v", err)
	}
	survivor, err := openBackend(survivorDir)
	if err != nil {
		t.Fatalf("openBackend(copy): %v", err)
	}
	t.Cleanup(func() { survivor.Close() })
	if recovered, err := survivor.LoadCommitments(context.Background()); err != nil || len(recovered) != 1 || recovered[0] != commitment {
		t.Fatalf("LoadCommitments(copy) = %+v, %v, want %+v: the commitment must reach the main file before SaveCommitment returns", recovered, err, commitment)
	}
}

// Test flow:
//  1. Inside a bubble, whose clock starts at 2000-01-01, open a store and register a row.
//  2. Record a settle hash for it.
//  3. Assert the broadcast stamp read back is the bubble's clock, not the database's.
func TestTheSettleStampFollowsTheProcessClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		testStore, err := openBackend(t.TempDir())
		if err != nil {
			t.Fatalf("openBackend() = %v, want nil", err)
		}
		ctx := context.Background()
		if err := testStore.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "9", Model: "qwen", Active: true}); err != nil {
			t.Fatalf("UpsertDevshard() = %v, want nil", err)
		}
		if err := testStore.SetDevshardSettleTxHash(ctx, "9", "SETTLE-TX"); err != nil {
			t.Fatalf("SetDevshardSettleTxHash() = %v, want nil", err)
		}
		hash, broadcastAt, err := testStore.DevshardSettleTxHash(ctx, "9")
		closeErr := testStore.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("DevshardSettleTxHash() = %v, Close() = %v, want nil, nil", err, closeErr)
		}
		if want := time.Now().UTC().Truncate(time.Second); hash != "SETTLE-TX" || !broadcastAt.Equal(want) {
			t.Fatalf("DevshardSettleTxHash() = %q at %v, want SETTLE-TX at %v", hash, broadcastAt, want)
		}
	})
}

// Test flow:
//  1. Provoke a constraint violation from the driver and build a plain error.
//  2. Assert neither is retryable.
func TestAConstraintViolationIsNotRetried(t *testing.T) {
	storageDir := t.TempDir()
	first, err := openBackend(storageDir)
	if err != nil {
		t.Fatalf("openBackend() = %v, want nil", err)
	}
	defer first.Close()
	_, constraintErr := first.db.Exec(`INSERT INTO config_overrides (id, overrides_json) VALUES (2, '{}')`)
	if constraintErr == nil || isLockedError(constraintErr) {
		t.Fatalf("constraint violation = %v, want a non-retryable error", constraintErr)
	}
	if isLockedError(errors.New("not a driver error")) {
		t.Fatal("isLockedError(plain error) = true, want false")
	}
}

// Test flow:
//  1. Seed a database with the migrations through the one holding `inference_completions` at version 14, and none holding `api_keys`, as a build that put `inference_completions` in that slot left it.
//  2. Open the store against that file and insert a key.
//  3. Assert the insert succeeds and the key lists, so the table was created by a later version.
func TestOpenCreatesTheAPIKeysTableOnADatabaseWhoseVersion14WasAnotherTable(t *testing.T) {
	dir := t.TempDir()
	seed, err := sql.Open("sqlite", filepath.Join(dir, gatewayDatabaseFileName))
	if err != nil {
		t.Fatalf("opening seed db: %v", err)
	}
	completions := slices.IndexFunc(migrations, func(migration string) bool { return strings.Contains(migration, "inference_completions") })
	statements := slices.Concat([]string{`CREATE TABLE schema_version (version INTEGER NOT NULL)`}, migrations[:completions-1], migrations[completions:completions+1], []string{
		`INSERT INTO schema_version (version) VALUES (1), (2), (3), (4), (5), (6), (7), (8), (9), (10), (11), (12), (13), (14)`,
	})
	for _, statement := range statements {
		if _, err := seed.Exec(statement); err != nil {
			t.Fatalf("seeding the db with %q: %v", statement, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}

	upgraded, err := openBackend(dir)
	if err != nil {
		t.Fatalf("openBackend() upgrading the db: %v", err)
	}
	t.Cleanup(func() { upgraded.Close() })

	ctx := context.Background()
	if err := upgraded.InsertAPIKey(ctx, store.APIKeyRecord{ID: "22222222", Key: "fake-stored-key-000001", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}); err != nil {
		t.Fatalf("InsertAPIKey() after the upgrade: %v", err)
	}
	if listed, err := upgraded.ListAPIKeys(ctx); err != nil || len(listed) != 1 {
		t.Fatalf("ListAPIKeys() = %+v, %v, want the inserted key", listed, err)
	}
}
