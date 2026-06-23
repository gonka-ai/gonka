package apiconfig

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openMemDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// BaseURL/AuthToken must survive a write/read round-trip through SQLite.
func TestUpsertReadNodes_PreservesBaseURLAndAuthToken(t *testing.T) {
	ctx := context.Background()
	db := openMemDB(t)
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	in := []InferenceNodeConfig{{
		Id: "n1", Host: "h", InferencePort: 5000, PoCPort: 8080, MaxConcurrent: 1,
		BaseURL: "http://svc.provider.com/path/", AuthToken: "tok",
	}}
	if err := UpsertInferenceNodes(ctx, db, in); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	out, err := ReadNodes(ctx, db)
	if err != nil {
		t.Fatalf("ReadNodes: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d nodes, want 1", len(out))
	}
	if out[0].BaseURL != "http://svc.provider.com/path/" {
		t.Errorf("BaseURL = %q, want %q", out[0].BaseURL, "http://svc.provider.com/path/")
	}
	if out[0].AuthToken != "tok" {
		t.Errorf("AuthToken = %q, want %q", out[0].AuthToken, "tok")
	}
}

// EnsureSchema must migrate a pre-existing inference_nodes table that lacks the
// base_url/auth_token columns by adding them, so old databases keep working.
func TestEnsureSchema_MigratesLegacyTable(t *testing.T) {
	ctx := context.Background()
	db := openMemDB(t)

	// Old schema: no base_url / auth_token columns.
	_, err := db.ExecContext(ctx, `
CREATE TABLE inference_nodes (
  id TEXT PRIMARY KEY,
  host TEXT NOT NULL,
  inference_segment TEXT NOT NULL,
  inference_port INTEGER NOT NULL,
  poc_segment TEXT NOT NULL,
  poc_port INTEGER NOT NULL,
  max_concurrent INTEGER NOT NULL,
  models_json TEXT NOT NULL,
  hardware_json TEXT NOT NULL,
  updated_at DATETIME NOT NULL DEFAULT (STRFTIME('%Y-%m-%d %H:%M:%f','now')),
  created_at DATETIME NOT NULL DEFAULT (STRFTIME('%Y-%m-%d %H:%M:%f','now'))
);`)
	if err != nil {
		t.Fatalf("create legacy table: %v", err)
	}

	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatalf("EnsureSchema (migration): %v", err)
	}

	// Round-trip should now carry the new fields.
	in := []InferenceNodeConfig{{Id: "n1", Host: "h", InferencePort: 5000, PoCPort: 8080, MaxConcurrent: 1, BaseURL: "http://svc/", AuthToken: "tok"}}
	if err := UpsertInferenceNodes(ctx, db, in); err != nil {
		t.Fatalf("Upsert after migration: %v", err)
	}
	out, err := ReadNodes(ctx, db)
	if err != nil {
		t.Fatalf("ReadNodes after migration: %v", err)
	}
	if len(out) != 1 || out[0].BaseURL != "http://svc/" || out[0].AuthToken != "tok" {
		t.Fatalf("migration lost data: %+v", out)
	}
}
