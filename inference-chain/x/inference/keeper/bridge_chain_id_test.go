package keeper

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChainIdMappings_Consistent(t *testing.T) {
	// Verify that mintChainIdMapping and chainIdMapping contain the same chains.
	// If they diverge, users could withdraw to a chain they cannot mint back from.
	for chain, id := range mintChainIdMapping {
		withdrawId, found := chainIdMapping[chain]
		require.True(t, found, "chain %q exists in mintChainIdMapping but not in withdrawal chainIdMapping", chain)
		require.Equal(t, id, withdrawId, "chain ID mismatch for %q: mint=%s, withdrawal=%s", chain, id, withdrawId)
	}
	for chain, id := range chainIdMapping {
		mintId, found := mintChainIdMapping[chain]
		require.True(t, found, "chain %q exists in withdrawal chainIdMapping but not in mintChainIdMapping", chain)
		require.Equal(t, id, mintId, "chain ID mismatch for %q: mint=%s, withdrawal=%s", chain, mintId, id)
	}
}
