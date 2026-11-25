package store

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
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/filters"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	testStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open(): %v", err)
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
	testStore, err := Open(baseDir)
	if err != nil {
		t.Fatalf("Open() with missing directory: %v", err)
	}
	defer testStore.Close()
	if _, err := os.Stat(filepath.Join(baseDir, "gateway.db")); err != nil {
		t.Fatalf("gateway.db not created: %v", err)
	}
}

// Test flow:
//  1. Load overrides from a fresh store and assert they come back zero-valued.
//  2. Save an override and load it back, asserting it matches.
//  3. Save a second override and load again, asserting it replaces rather than appends to the first.
func TestOverridesRoundTripAndEmptyLoad(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	empty, err := testStore.LoadOverrides(ctx)
	if err != nil {
		t.Fatalf("LoadOverrides() on fresh store: %v", err)
	}
	if empty.DefaultMaxTokens != nil {
		t.Fatal("fresh store must return zero-value overrides")
	}

	maxTokens := int64(1234)
	saved := config.Overrides{Tunables: env.Tunables{DefaultMaxTokens: &maxTokens}}
	if err := testStore.SaveOverrides(ctx, saved); err != nil {
		t.Fatalf("SaveOverrides(): %v", err)
	}
	loaded, err := testStore.LoadOverrides(ctx)
	if err != nil {
		t.Fatalf("LoadOverrides() after save: %v", err)
	}
	if loaded.DefaultMaxTokens == nil || *loaded.DefaultMaxTokens != 1234 {
		t.Fatalf("loaded overrides = %+v, want DefaultMaxTokens 1234", loaded)
	}

	newTokens := int64(999)
	if err := testStore.SaveOverrides(ctx, config.Overrides{Tunables: env.Tunables{DefaultMaxTokens: &newTokens}}); err != nil {
		t.Fatalf("second SaveOverrides(): %v", err)
	}
	replaced, err := testStore.LoadOverrides(ctx)
	if err != nil {
		t.Fatalf("LoadOverrides() after replace: %v", err)
	}
	if replaced.DefaultMaxTokens == nil || *replaced.DefaultMaxTokens != 999 {
		t.Fatalf("replaced overrides = %+v, want 999", replaced)
	}
}

// Test flow:
//  1. Upsert a `DevshardRecord` and assert `ListDevshards` returns exactly it.
//  2. Deactivate it and mark it settlement-pending, and assert the listed record reflects both.
//  3. Upsert the same escrow again with a different model and active state, and assert the upsert replaces the record's fields.
//  4. Delete the escrow and assert `ListDevshards` returns nothing.
func TestDevshardCRUDLifecycle(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	record := DevshardRecord{
		EscrowID:      "escrow-7",
		PrivateKeyEnv: "GATEWAY_KEY_ESCROW_7",
		Model:         "model-a",
		Active:        true,
		RotationRole:  "regular",
		RotationEpoch: 12,
	}
	if err := testStore.UpsertDevshard(ctx, record); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}

	listed, err := testStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards(): %v", err)
	}
	if len(listed) != 1 || listed[0] != record {
		t.Fatalf("ListDevshards() = %+v, want exactly the upserted record", listed)
	}

	if err := testStore.SetDevshardActive(ctx, "escrow-7", false); err != nil {
		t.Fatalf("SetDevshardActive(): %v", err)
	}
	if err := testStore.SetDevshardSettlementPending(ctx, "escrow-7", true); err != nil {
		t.Fatalf("SetDevshardSettlementPending(): %v", err)
	}
	listed, err = testStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards() after updates: %v", err)
	}
	if listed[0].Active || !listed[0].SettlementPending {
		t.Fatalf("after updates got %+v, want inactive + settlement pending", listed[0])
	}

	record.Model = "model-b"
	record.Active = false
	if err := testStore.UpsertDevshard(ctx, record); err != nil {
		t.Fatalf("UpsertDevshard() replace: %v", err)
	}
	listed, _ = testStore.ListDevshards(ctx)
	if listed[0].Model != "model-b" {
		t.Fatalf("after replace got model %q, want model-b", listed[0].Model)
	}

	if err := testStore.DeleteDevshard(ctx, "escrow-7"); err != nil {
		t.Fatalf("DeleteDevshard(): %v", err)
	}
	listed, _ = testStore.ListDevshards(ctx)
	if len(listed) != 0 {
		t.Fatalf("after delete list = %+v, want empty", listed)
	}
}

// Test flow:
//  1. Upsert a devshard and mark it settlement-pending.
//  2. Re-import (upsert again) the same escrow with a different model and active state, the way a re-import would build the record with `SettlementPending` at its zero value.
//  3. Assert the settlement-pending flag survived the re-import while every other field was replaced.
func TestUpsertDevshardKeepsAQueuedSettlementWhileReplacingEveryOtherField(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	parked := DevshardRecord{EscrowID: "escrow-9", PrivateKeyEnv: "GATEWAY_KEY_9", Model: "model-a", RotationRole: "regular"}
	if err := testStore.UpsertDevshard(ctx, parked); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	if err := testStore.SetDevshardSettlementPending(ctx, "escrow-9", true); err != nil {
		t.Fatalf("SetDevshardSettlementPending(): %v", err)
	}

	reimported := parked
	reimported.Model = "model-b"
	reimported.Active = true
	if err := testStore.UpsertDevshard(ctx, reimported); err != nil {
		t.Fatalf("UpsertDevshard() re-import: %v", err)
	}

	listed, err := testStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards(): %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListDevshards() = %+v, want exactly one row", listed)
	}
	if !listed[0].SettlementPending {
		t.Fatal("the re-import cleared the queued settlement; nothing will ever settle that escrow")
	}
	if listed[0].Model != "model-b" || !listed[0].Active {
		t.Fatalf("re-imported row = %+v, want model-b and active (every other field replaced)", listed[0])
	}
}

// Test flow:
//  1. Register two escrows, the second parked.
//  2. Read the second by id and assert the row matches what the list returns for it.
//  3. Read an id that was never registered and assert it is reported missing without an error.
func TestDevshardReadsOneRowAsTheListDoes(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()
	for _, escrowID := range []string{"1", "2"} {
		if err := testStore.UpsertDevshard(ctx, DevshardRecord{EscrowID: escrowID, PrivateKeyEnv: "KEY_" + escrowID, Model: "model", Active: true}); err != nil {
			t.Fatalf("UpsertDevshard(%s): %v", escrowID, err)
		}
	}
	if err := testStore.ParkForSettlement(ctx, "2"); err != nil {
		t.Fatalf("ParkForSettlement(2): %v", err)
	}

	listed, err := testStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards(): %v", err)
	}
	record, found, err := testStore.Devshard(ctx, "2")
	if err != nil || !found {
		t.Fatalf("Devshard(2) = found %v, err %v", found, err)
	}
	if record != listed[1] {
		t.Fatalf("Devshard(2) = %+v, want the listed row %+v", record, listed[1])
	}

	if _, found, err := testStore.Devshard(ctx, "ghost"); err != nil || found {
		t.Fatalf("Devshard(ghost) = found %v, err %v, want missing and no error", found, err)
	}
}

// Test flow:
//  1. Call `SetDevshardActive`, `SetDevshardSettlementPending`, and `DeleteDevshard` on an escrow id that was never registered.
//  2. Assert each returns `ErrDevshardNotFound`.
func TestMissingDevshardReturnsSentinel(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()
	if err := testStore.SetDevshardActive(ctx, "ghost", true); !errors.Is(err, ErrDevshardNotFound) {
		t.Fatalf("SetDevshardActive(ghost) = %v, want ErrDevshardNotFound", err)
	}
	if err := testStore.SetDevshardSettlementPending(ctx, "ghost", true); !errors.Is(err, ErrDevshardNotFound) {
		t.Fatalf("SetDevshardSettlementPending(ghost) = %v, want ErrDevshardNotFound", err)
	}
	if err := testStore.DeleteDevshard(ctx, "ghost"); !errors.Is(err, ErrDevshardNotFound) {
		t.Fatalf("DeleteDevshard(ghost) = %v, want ErrDevshardNotFound", err)
	}
}

// Test flow:
//  1. Open a store, upsert one devshard, and close it.
//  2. Reopen the store from the same directory, re-running its migrations.
//  3. Assert the devshard survived the restart.
func TestOpenIsIdempotentAcrossRestarts(t *testing.T) {
	baseDir := t.TempDir()
	first, err := Open(baseDir)
	if err != nil {
		t.Fatalf("first Open(): %v", err)
	}
	ctx := context.Background()
	if err := first.UpsertDevshard(ctx, DevshardRecord{EscrowID: "escrow-1", Model: "m", RotationRole: "regular"}); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	second, err := Open(baseDir)
	if err != nil {
		t.Fatalf("second Open(): %v", err)
	}
	defer second.Close()
	listed, err := second.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards() after reopen: %v", err)
	}
	if len(listed) != 1 || listed[0].EscrowID != "escrow-1" {
		t.Fatalf("data lost across reopen: %+v", listed)
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

	opened, err := Open(dir)
	if err == nil {
		opened.Close()
		t.Fatal("Open() accepted a gateway.db this gateway did not create, want a refusal")
	}
	if !errors.Is(err, ErrLegacyDatabase) {
		t.Fatalf("Open() error = %v, want ErrLegacyDatabase", err)
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

	upgraded, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() upgrading a v1 db: %v", err)
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
	if err := upgraded.SaveCommitment(ctx, Commitment{TxHash: "tx-1", Model: "m", CreatedAt: created}); err != nil {
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

	upgraded, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() upgrading the pre-planner db: %v", err)
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

	upgraded, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() upgrading the db: %v", err)
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
//  1. Upsert an active devshard.
//  2. Park it for settlement via `ParkForSettlement`.
//  3. Assert the listed record is both inactive and settlement-pending, set together as one statement so a crash between the two can never leave the escrow out of service and never settled.
func TestParkingForSettlementSetsBothFieldsAtOnce(t *testing.T) {
	gatewayStore := openTestStore(t)
	ctx := context.Background()
	if err := gatewayStore.UpsertDevshard(ctx, DevshardRecord{EscrowID: "7", Model: "qwen", Active: true}); err != nil {
		t.Fatalf("UpsertDevshard: %v", err)
	}

	if err := gatewayStore.ParkForSettlement(ctx, "7"); err != nil {
		t.Fatalf("ParkForSettlement: %v", err)
	}

	records, err := gatewayStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Active {
		t.Fatal("a parked escrow is still routable")
	}
	if !records[0].SettlementPending {
		t.Fatal("a parked escrow is not pending, so nothing will ever settle it")
	}
}

// Test flow:
//  1. Call `ParkForSettlement` on an escrow id that was never registered.
//  2. Assert it returns `ErrDevshardNotFound`.
func TestParkingAnUnknownEscrowIsReported(t *testing.T) {
	gatewayStore := openTestStore(t)

	err := gatewayStore.ParkForSettlement(context.Background(), "404")

	if !errors.Is(err, ErrDevshardNotFound) {
		t.Fatalf("ParkForSettlement = %v, want ErrDevshardNotFound", err)
	}
}

// Test flow:
//  1. Upsert an active devshard.
//  2. Call `ParkForSettlementIfActive`.
//  3. Assert it reports that it parked the escrow, and the listed record is inactive and settlement-pending.
func TestParkingIfActiveParksAServingEscrow(t *testing.T) {
	gatewayStore := openTestStore(t)
	ctx := context.Background()
	if err := gatewayStore.UpsertDevshard(ctx, DevshardRecord{EscrowID: "7", Model: "qwen", Active: true}); err != nil {
		t.Fatalf("UpsertDevshard: %v", err)
	}

	parked, err := gatewayStore.ParkForSettlementIfActive(ctx, "7")

	if err != nil {
		t.Fatalf("ParkForSettlementIfActive: %v", err)
	}
	if !parked {
		t.Fatal("parking a serving escrow did not report that it parked it")
	}
	records, err := gatewayStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Active || !records[0].SettlementPending {
		t.Fatalf("record = %+v, want inactive and settlement-pending", records[0])
	}
}

// Test flow:
//  1. Call `ParkForSettlementIfActive` on an escrow id that was never registered.
//  2. Assert it returns no error and reports that nothing was parked.
func TestParkingIfActiveReportsNoParkForAnUnknownEscrow(t *testing.T) {
	gatewayStore := openTestStore(t)

	parked, err := gatewayStore.ParkForSettlementIfActive(context.Background(), "404")

	if err != nil || parked {
		t.Fatalf("ParkForSettlementIfActive = %v, %v; want false, nil", parked, err)
	}
}

// Test flow:
//  1. Upsert an inactive devshard.
//  2. Call `ParkForSettlementIfActive`.
//  3. Assert it reports no park happened and the record's settlement-pending flag stays clear.
func TestParkingIfActiveLeavesAnEscrowAlreadyOutOfServiceUntouched(t *testing.T) {
	gatewayStore := openTestStore(t)
	ctx := context.Background()
	if err := gatewayStore.UpsertDevshard(ctx, DevshardRecord{EscrowID: "7", Model: "qwen", Active: false}); err != nil {
		t.Fatalf("UpsertDevshard: %v", err)
	}

	parked, err := gatewayStore.ParkForSettlementIfActive(ctx, "7")

	if err != nil {
		t.Fatalf("ParkForSettlementIfActive: %v", err)
	}
	if parked {
		t.Fatal("parking an escrow already out of service reported a park")
	}
	records, err := gatewayStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].SettlementPending {
		t.Fatal("an escrow taken out of service without a settlement was queued for one")
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
	gatewayStore, err := Open(storageDir)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() { gatewayStore.Close() })
	if _, err := gatewayStore.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	commitment := Commitment{TxHash: "TX-INTENT", Model: "model-a", Role: RoleTemp, Epoch: 9, PrivateKeyEnv: "MODEL_A_KEY", BlockHeight: 500, CreatedAt: time.Unix(1_700_000_000, 0).UTC()}
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
	survivor, err := Open(survivorDir)
	if err != nil {
		t.Fatalf("Open(copy): %v", err)
	}
	t.Cleanup(func() { survivor.Close() })
	if recovered, err := survivor.LoadCommitments(context.Background()); err != nil || len(recovered) != 1 || recovered[0] != commitment {
		t.Fatalf("LoadCommitments(copy) = %+v, %v, want %+v: the commitment must reach the main file before SaveCommitment returns", recovered, err, commitment)
	}
}
