package postgres

import (
	"context"
	"testing"

	"devshard/cmd/gateway/store"
	"devshard/cmd/gateway/store/storetest"
)

func contractFactory(tb testing.TB) storetest.Opener {
	dsn := storetest.FreshPostgresSchema(tb, storetest.PostgresDSN(tb))
	return func() store.Store {
		opened, err := Open(context.Background(), Options{ConnectionString: dsn})
		if err != nil {
			tb.Fatalf("Open(): %v", err)
		}
		tb.Cleanup(func() { opened.Close() })
		return opened
	}
}

// Test flow:
//  1. Run the contract suite, one fresh schema per case.
//  2. Assert every case passes on the Postgres backend.
func TestContract(t *testing.T) {
	storetest.PostgresDSN(t)
	storetest.Run(t, contractFactory)
}

// Test flow:
//  1. Hand the upsert statement to the shared check.
//  2. Assert every column it should update is updated and every deliberate exception is left alone.
func TestUpsertStatementUpdatesEveryField(t *testing.T) {
	storetest.CheckUpsertStatement(t, upsertDevshardStatement)
}

// Test flow:
//  1. Run the shared benchmarks against this backend: the completion journal, the ledger, one retention sweep and the per-request statement set.
func BenchmarkBackend(b *testing.B) {
	storetest.PostgresDSN(b)
	storetest.RunBenchmarks(b, contractFactory)
}
