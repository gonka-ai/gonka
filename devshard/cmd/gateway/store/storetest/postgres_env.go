package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// PostgresDSNVariable names the URL of a throwaway Postgres database whose role may create schemas; RequirePostgresVariable makes a missing one fail the run instead of skipping it.
const (
	PostgresDSNVariable     = "GATEWAY_TEST_POSTGRES_DSN"
	RequirePostgresVariable = "GATEWAY_TEST_REQUIRE_POSTGRES"
	schemaPrefix            = "gateway_test_"
	schemaSuffixBytes       = 6
	searchPathParameter     = "search_path"
)

// PostgresDSN returns the test database's URL; without one the test skips, or fails when RequirePostgresVariable is set.
func PostgresDSN(tb testing.TB) string {
	tb.Helper()
	dsn := os.Getenv(PostgresDSNVariable)
	if dsn != "" {
		return dsn
	}
	if os.Getenv(RequirePostgresVariable) != "" {
		tb.Fatalf("%s is not set but %s is: the Postgres half of the suite must run", PostgresDSNVariable, RequirePostgresVariable)
	}
	tb.Skipf("%s is not set", PostgresDSNVariable)
	return ""
}

// FreshPostgresSchema creates an empty schema for one test, drops it afterwards and returns dsn with its search_path.
func FreshPostgresSchema(tb testing.TB, dsn string) string {
	tb.Helper()
	suffix := make([]byte, schemaSuffixBytes)
	if _, err := rand.Read(suffix); err != nil {
		tb.Fatalf("rand.Read(): %v", err)
	}
	schema := schemaPrefix + hex.EncodeToString(suffix)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		tb.Fatalf("connecting to the test database: %v", err)
	}
	tb.Cleanup(func() { _ = admin.Close(ctx) })
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		tb.Fatalf("creating schema %s: %v", schema, err)
	}
	tb.Cleanup(func() {
		if _, err := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			tb.Errorf("dropping schema %s: %v", schema, err)
		}
	})
	return WithSearchPath(tb, dsn, schema)
}

// WithSearchPath returns dsn with its search_path set to schema.
func WithSearchPath(tb testing.TB, dsn, schema string) string {
	tb.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		tb.Fatalf("parsing %s as a URL: %v", PostgresDSNVariable, err)
	}
	query := parsed.Query()
	query.Set(searchPathParameter, schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
