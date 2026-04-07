//go:build integration

package bridge

import (
	"os"
	"subnet/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chainURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("CHAIN_REST_URL")
	if u == "" {
		u = "http://localhost:1317"
	}
	return u
}

func TestIntegration_GetEscrow(t *testing.T) {
	b := NewRESTBridge(chainURL(t))

	info, err := b.GetEscrow("1")
	require.NoError(t, err)

	assert.Equal(t, "1", info.EscrowID)
	assert.NotZero(t, info.Amount)
	assert.NotEmpty(t, info.CreatorAddress)
	assert.NotEmpty(t, info.AppHash)
	assert.NotEmpty(t, info.Slots)
}

func TestIntegration_GetHostInfo(t *testing.T) {
	b := NewRESTBridge(chainURL(t))

	// First get an escrow to find a real host address
	escrow, err := b.GetEscrow("1")
	require.NoError(t, err)
	require.NotEmpty(t, escrow.Slots)

	info, err := b.GetHostInfo(escrow.Slots[0])
	require.NoError(t, err)

	assert.NotEmpty(t, info.Address)
	assert.NotEmpty(t, info.URL)
}

func TestIntegration_BuildGroup(t *testing.T) {
	b := NewRESTBridge(chainURL(t))

	group, err := BuildGroup("1", b)
	require.NoError(t, err)

	assert.NotEmpty(t, group)
	assert.NoError(t, types.ValidateGroup(group))

	for i, slot := range group {
		assert.Equal(t, uint32(i), slot.SlotID)
		assert.NotEmpty(t, slot.ValidatorAddress)
	}
}
