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
