package v0_2_13

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpgradeName pins the on-chain proposal name. If the v0.2.13 governance
// proposal is announced under a different string, change either the proposal
// or this constant — but they MUST match exactly. Cosmovisor uses the name
// to schedule the binary swap; a mismatch silently bypasses the handler.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.13", UpgradeName)
}
