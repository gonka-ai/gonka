package parity

import (
	"context"
	"database/sql"
	"maps"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	_ "modernc.org/sqlite"

	"devshard/cmd/gateway/store/postgres"
	"devshard/cmd/gateway/store/sqlite"
	"devshard/cmd/gateway/store/storetest"
)

const (
	markerTable       = "gateway_database_marker"
	currentTimeToken  = "now"
	sqliteColumnsSQL  = `SELECT m.name, p.name, p.type, p."notnull" = 1, p.pk > 0, COALESCE(p.dflt_value, '') FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'`
	sqliteIndexesSQL  = `SELECT tbl_name, name FROM sqlite_master WHERE type = 'index' AND name NOT LIKE 'sqlite_autoindex%'`
	postgresIndexSQL  = `SELECT tablename, indexname FROM pg_indexes WHERE schemaname = current_schema() AND indexname NOT LIKE '%_pkey'`
	postgresColumnSQL = `SELECT c.table_name, c.column_name, c.data_type, c.is_nullable = 'NO',
		EXISTS (SELECT 1 FROM information_schema.key_column_usage k
			JOIN information_schema.table_constraints tc ON tc.constraint_schema = k.constraint_schema AND tc.constraint_name = k.constraint_name
			WHERE tc.constraint_type = 'PRIMARY KEY' AND k.table_schema = c.table_schema AND k.table_name = c.table_name AND k.column_name = c.column_name),
		COALESCE(c.column_default, '')
		FROM information_schema.columns c WHERE c.table_schema = current_schema()`
)

var (
	castSuffix     = regexp.MustCompile(`::[a-z ]+`)
	sqliteNow      = regexp.MustCompile(`^\(?datetime\('now'\)\)?$`)
	postgresNow    = regexp.MustCompile(`^\(?to_char\(\(?now\(\).*\)\)?$`)
	normalizedType = map[string]string{"INTEGER": "integer", "bigint": "integer", "TEXT": "text", "text": "text", "BLOB": "bytes", "bytea": "bytes"}
)

func normalizeDefault(raw string) string {
	raw = strings.TrimSpace(castSuffix.ReplaceAllString(raw, ""))
	if sqliteNow.MatchString(raw) || postgresNow.MatchString(raw) {
		return currentTimeToken
	}
	return raw
}

func columnFacts(t *testing.T, scan func(yield func(table, column, kind string, notNull, primaryKey bool, fallback string))) map[string]string {
	t.Helper()
	facts := map[string]string{}
	scan(func(table, column, kind string, notNull, primaryKey bool, fallback string) {
		if table == markerTable {
			return
		}
		normalized, known := normalizedType[kind]
		if !known {
			t.Errorf("%s.%s has the type %q, which the comparison does not know", table, column, kind)
		}
		facts[table+"."+column] = strings.Join([]string{normalized, boolText(notNull || primaryKey), boolText(primaryKey), normalizeDefault(fallback)}, "|")
	})
	return facts
}

func boolText(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// Test flow:
//  1. Open both backends on empty databases and read every table's columns and indexes back from each catalog.
//  2. Assert both list the same tables and columns with the same type family, nullability (a primary-key column is never null), primary-key membership and default.
//  3. Assert both hold the same secondary indexes, apart from the marker table that only Postgres has.
func TestBackendSchemasMatch(t *testing.T) {
	postgresDSN := storetest.FreshPostgresSchema(t, storetest.PostgresDSN(t))
	storageDir := t.TempDir()
	lite, err := sqlite.Open(storageDir)
	if err != nil {
		t.Fatalf("sqlite.Open(): %v", err)
	}
	defer lite.Close()
	server, err := postgres.Open(context.Background(), postgres.Options{ConnectionString: postgresDSN})
	if err != nil {
		t.Fatalf("postgres.Open(): %v", err)
	}
	defer server.Close()

	file, err := sql.Open("sqlite", sqlite.DatabasePath(storageDir))
	if err != nil {
		t.Fatalf("opening the SQLite file: %v", err)
	}
	defer file.Close()
	catalog, err := pgx.Connect(context.Background(), postgresDSN)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer catalog.Close(context.Background())

	liteColumns := columnFacts(t, func(yield func(string, string, string, bool, bool, string)) {
		rows, err := file.Query(sqliteColumnsSQL)
		if err != nil {
			t.Fatalf("reading the SQLite columns: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var table, column, kind, fallback string
			var notNull, primaryKey bool
			if err := rows.Scan(&table, &column, &kind, &notNull, &primaryKey, &fallback); err != nil {
				t.Fatalf("scanning a SQLite column: %v", err)
			}
			yield(table, column, kind, notNull, primaryKey, fallback)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterating the columns: %v", err)
		}
	})
	serverColumns := columnFacts(t, func(yield func(string, string, string, bool, bool, string)) {
		rows, err := catalog.Query(context.Background(), postgresColumnSQL)
		if err != nil {
			t.Fatalf("reading the Postgres columns: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var table, column, kind, fallback string
			var notNull, primaryKey bool
			if err := rows.Scan(&table, &column, &kind, &notNull, &primaryKey, &fallback); err != nil {
				t.Fatalf("scanning a Postgres column: %v", err)
			}
			yield(table, column, kind, notNull, primaryKey, fallback)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterating the columns: %v", err)
		}
	})
	if len(liteColumns) == 0 {
		t.Fatal("the SQLite catalog lists no columns")
	}
	for key, want := range liteColumns {
		if got, found := serverColumns[key]; !found || got != want {
			t.Errorf("%s: SQLite %q, Postgres %q (found %v)", key, want, got, found)
		}
	}
	for key := range maps.Keys(serverColumns) {
		if _, found := liteColumns[key]; !found {
			t.Errorf("%s exists in Postgres only", key)
		}
	}

	liteIndexes := map[string]bool{}
	indexRows, err := file.Query(sqliteIndexesSQL)
	if err != nil {
		t.Fatalf("reading the SQLite indexes: %v", err)
	}
	defer indexRows.Close()
	for indexRows.Next() {
		var table, name string
		if err := indexRows.Scan(&table, &name); err != nil {
			t.Fatalf("scanning a SQLite index: %v", err)
		}
		liteIndexes[table+"."+name] = true
	}
	if err := indexRows.Err(); err != nil {
		t.Fatalf("iterating the SQLite indexes: %v", err)
	}
	serverIndexes := map[string]bool{}
	pgRows, err := catalog.Query(context.Background(), postgresIndexSQL)
	if err != nil {
		t.Fatalf("reading the Postgres indexes: %v", err)
	}
	for pgRows.Next() {
		var table, name string
		if err := pgRows.Scan(&table, &name); err != nil {
			t.Fatalf("scanning a Postgres index: %v", err)
		}
		serverIndexes[table+"."+name] = true
	}
	pgRows.Close()
	if !maps.Equal(liteIndexes, serverIndexes) {
		t.Errorf("secondary indexes differ: SQLite %v, Postgres %v", liteIndexes, serverIndexes)
	}
}
