//go:build integration

package bridge

import (
	"os"
	"devshard/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chainGRPCURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("CHAIN_GRPC_URL")
	if u == "" {
		u = "localhost:9090"
	}
	return u
}

func TestIntegration_GetEscrow(t *testing.T) {
	b, err := NewGRPCBridgeFromURL(chainGRPCURL(t))
	require.NoError(t, err)

	info, err := b.GetEscrow("1")
	require.NoError(t, err)

	assert.Equal(t, "1", info.EscrowID)
	assert.NotZero(t, info.Amount)
	assert.NotEmpty(t, info.CreatorAddress)
	assert.NotEmpty(t, info.AppHash)
	assert.NotEmpty(t, info.Slots)
}

func TestIntegration_GetHostInfo(t *testing.T) {
	b, err := NewGRPCBridgeFromURL(chainGRPCURL(t))
	require.NoError(t, err)

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
	b, err := NewGRPCBridgeFromURL(chainGRPCURL(t))
	require.NoError(t, err)

	group, err := BuildGroup("1", b)
	require.NoError(t, err)

	assert.NotEmpty(t, group)
	assert.NoError(t, types.ValidateGroup(group))

	for i, slot := range group {
		assert.Equal(t, uint32(i), slot.SlotID)
		assert.NotEmpty(t, slot.ValidatorAddress)
	}
}
