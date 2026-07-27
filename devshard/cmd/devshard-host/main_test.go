package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupFromKeys_DerivesCompactSlotGroup(t *testing.T) {
	group, err := groupFromKeys(defaultHostPrivateKeys())
	require.NoError(t, err)
	require.Len(t, group, 3)
	for i, slot := range group {
		require.Equal(t, uint32(i), slot.SlotID)
		require.NotEmpty(t, slot.ValidatorAddress)
	}
}

func TestSplitCSV_TrimsEmptyValues(t *testing.T) {
	require.Equal(t, []string{"a", "b", "c"}, splitCSV(" a, b ,, c "))
	require.Nil(t, splitCSV(""))
}

func TestSessionConfigFromEnv_MapsEscrowTimeouts(t *testing.T) {
	t.Setenv("DEVSHARD_REFUSAL_TIMEOUT", "5")
	t.Setenv("DEVSHARD_EXECUTION_TIMEOUT", "17")

	cfg := sessionConfigFromEnv(3)
	require.Equal(t, int64(5), cfg.RefusalTimeout)
	require.Equal(t, int64(17), cfg.ExecutionTimeout)
}

func TestBoolEnv(t *testing.T) {
	t.Setenv("DEVSHARD_STUB_EXECUTION_HANG", "true")

	value, err := boolEnv("DEVSHARD_STUB_EXECUTION_HANG", false)
	require.NoError(t, err)
	require.True(t, value)

	t.Setenv("DEVSHARD_STUB_EXECUTION_HANG", "invalid")
	_, err = boolEnv("DEVSHARD_STUB_EXECUTION_HANG", false)
	require.Error(t, err)
}
