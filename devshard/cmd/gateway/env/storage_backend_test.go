package env

import (
	"os"
	"slices"
	"strings"
	"testing"

	"common/storage/mode"
)

// Test flow:
//  1. Resolve the backend with the mode unset, then with sqlite.
//  2. Assert both are SQLite, so an existing deployment keeps its store.
func TestStorageBackendDefaultsToSQLite(t *testing.T) {
	for _, value := range []string{"", "sqlite"} {
		t.Setenv(mode.EnvStorageMode, value)
		backend, err := ResolveStorageBackend()
		if err != nil || backend.Mode != mode.SQLite {
			t.Fatalf("ResolveStorageBackend() with %q = %+v, %v; want sqlite", value, backend, err)
		}
	}
}

// Test flow:
//  1. Resolve the backend with the mode set to hybrid, auto and an unknown word.
//  2. Assert each is refused with an error naming the variable.
func TestStorageBackendRefusesEveryModeButSQLiteAndPostgres(t *testing.T) {
	for _, value := range []string{"hybrid", "auto", "mysql"} {
		t.Setenv(mode.EnvStorageMode, value)
		if _, err := ResolveStorageBackend(); err == nil || !strings.Contains(err.Error(), mode.EnvStorageMode) {
			t.Fatalf("ResolveStorageBackend() with %q = %v, want an error naming %s", value, err, mode.EnvStorageMode)
		}
	}
}

// Test flow:
//  1. Select postgres and leave out, in turn, the instance id and the host.
//  2. Assert each is an error naming the missing variable.
//  3. Set both and assert the backend carries the instance id.
func TestStorageBackendPostgresNeedsAnInstanceIDAndAHost(t *testing.T) {
	t.Setenv(mode.EnvStorageMode, "postgres")
	t.Setenv("PGSSLMODE", "")
	t.Setenv(InstanceIDVariable, "")
	t.Setenv("PGHOST", "localhost")
	if _, err := ResolveStorageBackend(); err == nil || !strings.Contains(err.Error(), InstanceIDVariable) {
		t.Fatalf("without an instance id: %v, want an error naming %s", err, InstanceIDVariable)
	}
	t.Setenv(InstanceIDVariable, "gw-1")
	t.Setenv("PGHOST", "")
	if _, err := ResolveStorageBackend(); err == nil || !strings.Contains(err.Error(), "PGHOST") {
		t.Fatalf("without a host: %v, want an error naming PGHOST", err)
	}
	t.Setenv("PGHOST", "localhost")
	backend, err := ResolveStorageBackend()
	if err != nil || backend.Mode != mode.Postgres || backend.InstanceID != "gw-1" {
		t.Fatalf("ResolveStorageBackend() = %+v, %v; want postgres with instance gw-1", backend, err)
	}
}

// Test flow:
//  1. Set two Postgres variables, leaving the rest unset.
//  2. Assert exactly those two are reported, so an operator who set them while the store is SQLite is told they have no effect.
func TestIgnoredPostgresVariablesAreReported(t *testing.T) {
	for _, name := range postgresVariables {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	t.Setenv("PGHOST", "db")
	t.Setenv("PGUSER", "gateway")
	if got := IgnoredPostgresVariables(); !slices.Equal(got, []string{"PGHOST", "PGUSER"}) {
		t.Fatalf("IgnoredPostgresVariables() = %v, want [PGHOST PGUSER]", got)
	}
}
