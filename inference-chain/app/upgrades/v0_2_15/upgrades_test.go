package v0_2_15

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.15", UpgradeName)
}
