//go:build devshard_testenv

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClockFaultActiveTripFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clock-fault")
	t.Setenv(envTestenvClockFaultFile, path)
	require.False(t, clockFaultActive())
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.True(t, clockFaultActive())
	require.NoError(t, os.Remove(path))
	require.False(t, clockFaultActive())
}
