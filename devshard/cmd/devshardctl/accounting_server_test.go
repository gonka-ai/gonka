package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A gateway nobody configured keeps its ledger: the join template opts out explicitly, so a flipped
// fallback here would disable accounting everywhere else without anyone editing a config.
func TestOpenAccountingTracker_DefaultsToEnabled(t *testing.T) {
	dir := t.TempDir()

	tracker := openAccountingTracker(dir)

	require.NotNil(t, tracker)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	require.FileExists(t, filepath.Join(dir, "accounting.db"))
}

// Disabling must cost nothing on disk, not merely hide the API.
func TestOpenAccountingTracker_DisabledOpensNoDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVSHARD_STATS_ENABLED", "false")

	require.Nil(t, openAccountingTracker(dir))
	require.NoFileExists(t, filepath.Join(dir, "accounting.db"))
}
