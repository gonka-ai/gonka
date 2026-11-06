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

	tracker := openAccountingTracker(dir, defaultAccountingRetentionEpochs)

	require.NotNil(t, tracker)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	require.FileExists(t, filepath.Join(dir, "accounting.db"))
}

// Disabling must cost nothing on disk, not merely hide the API.
func TestOpenAccountingTracker_DisabledOpensNoDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVSHARD_STATS_ENABLED", "false")

	require.Nil(t, openAccountingTracker(dir, defaultAccountingRetentionEpochs))
	require.NoFileExists(t, filepath.Join(dir, "accounting.db"))
}

// Readers already parse the booleans, so they stay; the count is what tells a single refusal apart
// from a build that refuses everything. Deriving one from the other keeps them from disagreeing.
func TestAccountingCapability_DerivesTheFlagsFromTheCounts(t *testing.T) {
	perf := NewPerfTracker(nil)
	perf.RecordVersionUnsupported("p1")
	perf.RecordVersionUnsupported("p1")
	perf.RecordContextLimit("p1", "m", 4096)
	lookup := accountingCapability(&Gateway{perf: perf})
	require.NotNil(t, lookup)

	refused := lookup("p1", "m")
	require.True(t, refused.ProtocolVersionUnsupported)
	require.Equal(t, uint64(2), refused.VersionRefusals)
	require.False(t, refused.ToolChoiceUnsupported, "a host that never refused tools is not flagged for them")
	require.Equal(t, uint64(4096), refused.ContextLimit)

	clean := lookup("never-seen", "m")
	require.False(t, clean.ProtocolVersionUnsupported)
	require.Zero(t, clean.VersionRefusals)
}

// Test flow:
//  1. Read the accounting retention with DEVSHARD_STATS_RETENTION_EPOCHS unset, then set to 0.
//  2. Require two epochs by default and an explicit 0 to keep everything.
func TestAccountingRetentionEpochsDefaultsToTwo(t *testing.T) {
	t.Setenv("DEVSHARD_STATS_RETENTION_EPOCHS", "")
	require.Equal(t, uint64(2), accountingRetentionEpochs())

	t.Setenv("DEVSHARD_STATS_RETENTION_EPOCHS", "0")
	require.Equal(t, uint64(0), accountingRetentionEpochs())
}
