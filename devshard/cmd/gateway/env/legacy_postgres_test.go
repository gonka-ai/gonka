package env

import (
	"slices"
	"testing"
)

// Test flow:
//  1. Set two of devshardctl's Postgres variables, leaving the rest unset.
//  2. Assert exactly those two are reported as set and ignored: the gateway keeps its own store, so a drop-in with Postgres settings must be told.
func TestTheLegacyPostgresVariablesSetAreReported(t *testing.T) {
	t.Setenv("PGHOST", "db")
	t.Setenv("PGUSER", "gateway")

	got := LegacyPostgresVariablesSet()

	if !slices.Equal(got, []string{"PGHOST", "PGUSER"}) {
		t.Fatalf("LegacyPostgresVariablesSet() = %v, want [PGHOST PGUSER]", got)
	}
}
