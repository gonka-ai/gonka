package admin

import (
	"decentralized-api/apiconfig"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLaterDuplicateBatchIndex(t *testing.T) {
	nodes := []apiconfig.InferenceNodeConfig{
		{Id: "a"},
		{Id: "b"},
		{Id: "a"},
		{Id: "b"},
		{Id: "c"},
	}

	_, dup := laterDuplicateBatchIndex(nodes, 0)
	require.False(t, dup)
	_, dup = laterDuplicateBatchIndex(nodes, 1)
	require.False(t, dup)

	first, dup := laterDuplicateBatchIndex(nodes, 2)
	require.True(t, dup)
	require.Equal(t, 0, first)

	first, dup = laterDuplicateBatchIndex(nodes, 3)
	require.True(t, dup)
	require.Equal(t, 1, first)

	_, dup = laterDuplicateBatchIndex(nodes, 4)
	require.False(t, dup)
}
