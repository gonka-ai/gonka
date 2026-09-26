package tipcache

import (
	"context"
	"testing"
	"time"

	"common/chainoracle/blocks"

	"github.com/stretchr/testify/require"
)

func hdr(height int64, b byte) *blocks.Header {
	return blocks.HashOnlyHeader(height, time.Unix(height, 0).UTC(), "gonka-test", []byte{b})
}

func TestCache_ObserveStoresHistoricalAndAdvancesTip(t *testing.T) {
	c := New(time.Hour)
	c.Observe(hdr(10, 10))
	c.Observe(hdr(9, 9)) // older than tip: kept for At, does not move Latest

	got, err := c.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(10), got.Height)

	h9, err := c.At(context.Background(), 9)
	require.NoError(t, err)
	require.Equal(t, int64(9), h9.Height)
	require.Equal(t, []byte{9}, h9.BlockHash)
}

func TestCache_RememberDoesNotMoveLatest(t *testing.T) {
	c := New(time.Hour)
	c.Observe(hdr(20, 20))
	c.Remember(hdr(15, 15))

	got, err := c.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(20), got.Height)

	h15, err := c.At(context.Background(), 15)
	require.NoError(t, err)
	require.Equal(t, []byte{15}, h15.BlockHash)
}

func TestCache_EvictsOutsideHistoryWindow(t *testing.T) {
	c := New(time.Hour)
	c.Observe(hdr(HistoryWindow+5, 1))
	_, err := c.At(context.Background(), 1)
	require.Error(t, err)

	h, err := c.At(context.Background(), HistoryWindow+5)
	require.NoError(t, err)
	require.Equal(t, int64(HistoryWindow+5), h.Height)
}

func TestCache_DummyIgnored(t *testing.T) {
	c := New(time.Hour)
	c.Observe(blocks.DummyHeader(3))
	_, err := c.Latest(context.Background())
	require.Error(t, err)
}
