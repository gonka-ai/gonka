package v0_2_18

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemovedModuleStores(t *testing.T) {
	deleted := RemovedModuleStores().Deleted
	require.ElementsMatch(t, []string{
		"capability",
		"circuit",
		"crisis",
		"feeibc",
		"nft",
		"params",
	}, deleted)

	for _, kept := range []string{"ibc", "transfer", "staking", "inference", "wasm", "auth", "bank"} {
		require.NotContains(t, deleted, kept)
	}
}
