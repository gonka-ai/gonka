package blocks_test

import (
	"testing"
	"time"

	"common/chainoracle/blocks"

	"github.com/stretchr/testify/require"
)

func TestDummyHeader_IsDummy(t *testing.T) {
	h := blocks.DummyHeader(8)
	require.True(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(8), h.Height)
	require.True(t, h.Time.IsZero())
	require.Empty(t, h.BlockHash)

	real := blocks.HashOnlyHeader(8, time.Unix(1, 0).UTC(), "gonka", []byte{1})
	require.False(t, blocks.IsDummyHeader(real))
}

func TestOldestHeight(t *testing.T) {
	require.Equal(t, int64(1), blocks.OldestHeight(0))
	require.Equal(t, int64(1), blocks.OldestHeight(blocks.HistoryWindow))
	require.Equal(t, int64(1), blocks.OldestHeight(blocks.HistoryWindow+1))
	require.Equal(t, int64(199_900), blocks.OldestHeight(200_000))
}
