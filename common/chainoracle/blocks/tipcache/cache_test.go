package tipcache_test

import (
	"context"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/tipcache"

	"github.com/stretchr/testify/require"
)

func TestCache_ObserveLatestAndSubscribe(t *testing.T) {
	c := tipcache.New(time.Hour)
	_, err := c.Latest(context.Background())
	require.Error(t, err)
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)
	require.True(t, c.Stale())
	require.True(t, c.LastObservedAt().IsZero())

	hdr := blocks.HashOnlyHeader(5, time.Unix(10, 0).UTC(), "gonka", []byte{0xaa})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Subscribe(ctx, 1)
	require.NoError(t, err)

	c.Observe(hdr)
	got, err := c.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(5), got.Height)
	require.Equal(t, []byte{0xaa}, got.BlockHash)
	require.False(t, c.Stale())
	require.False(t, c.LastObservedAt().IsZero())
	require.InDelta(t, time.Now().UnixNano(), c.LastObservedAt().UnixNano(), float64(time.Second))

	at, err := c.At(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, []byte{0xaa}, at.BlockHash)
	_, err = c.At(context.Background(), 4)
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)

	select {
	case h := <-ch:
		require.Equal(t, int64(5), h.Height)
	case <-time.After(time.Second):
		t.Fatal("subscribe missed Observe")
	}
}

func TestCache_LPA1a_WindowIncludesTipMinusHistoryWindow(t *testing.T) {
	c := tipcache.New(time.Hour)
	h := int64(200_000)
	floor := h - tipcache.HistoryWindow
	c.Observe(hdr(floor))
	c.Observe(hdr(h))

	got, err := c.At(context.Background(), floor)
	require.NoError(t, err)
	require.Equal(t, floor, got.Height)
	_, err = c.At(context.Background(), floor-1)
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)
	got, err = c.At(context.Background(), h)
	require.NoError(t, err)
	require.Equal(t, h, got.Height)
}

func TestCache_LPA1b_AdvancingTipEvictsOldFloor(t *testing.T) {
	c := tipcache.New(time.Hour)
	h := int64(200_000)
	floor := h - tipcache.HistoryWindow
	c.Observe(hdr(floor))
	c.Observe(hdr(floor + 1))
	c.Observe(hdr(h))
	_, err := c.At(context.Background(), floor)
	require.NoError(t, err)

	c.Observe(hdr(h + 1))
	_, err = c.At(context.Background(), floor)
	require.Error(t, err, "old floor evicted when tip advances")
	got, err := c.At(context.Background(), floor+1)
	require.NoError(t, err)
	require.Equal(t, floor+1, got.Height)
}

func TestCache_LPA1c_RememberDoesNotAdvanceTip(t *testing.T) {
	c := tipcache.New(time.Hour)
	c.Remember(hdr(8))
	_, err := c.Latest(context.Background())
	require.Error(t, err)
	require.True(t, c.Stale())
	got, err := c.At(context.Background(), 8)
	require.NoError(t, err)
	require.Equal(t, int64(8), got.Height)

	c.Observe(hdr(100))
	c.Remember(hdr(90))
	got, err = c.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(100), got.Height)
	require.False(t, c.Stale())
	got, err = c.At(context.Background(), 90)
	require.NoError(t, err)
	require.Equal(t, int64(90), got.Height)
}

func TestCache_RememberOutsideWindowDropped(t *testing.T) {
	c := tipcache.New(time.Hour)
	tip := int64(tipcache.HistoryWindow + 50)
	c.Observe(hdr(tip))
	c.Remember(hdr(1))
	_, err := c.At(context.Background(), 1)
	require.Error(t, err)
}

func TestCache_LPA1d_DummyNotStored(t *testing.T) {
	c := tipcache.New(time.Hour)
	c.Observe(blocks.DummyHeader(3))
	c.Remember(blocks.DummyHeader(3))
	_, err := c.At(context.Background(), 3)
	require.Error(t, err)
	_, err = c.Latest(context.Background())
	require.Error(t, err)
}

func hdr(height int64) *blocks.Header {
	return blocks.HashOnlyHeader(height, time.Unix(height, 0).UTC(), "gonka", []byte{byte(height)})
}
