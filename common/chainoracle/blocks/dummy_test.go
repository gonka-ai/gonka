package blocks_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
)

func TestDummyHeader_IsDummy(t *testing.T) {
	h := blocks.DummyHeader(8)
	require.True(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(8), h.Height)
	require.True(t, h.Time.IsZero())
	require.Empty(t, h.BlockHash)

	actual := blocks.HashOnlyHeader(8, time.Unix(1, 0).UTC(), "gonka", []byte{1})
	require.False(t, blocks.IsDummyHeader(actual))
}
