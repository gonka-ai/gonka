package tipcache

import (
	"context"
	"sync"
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

func TestCache_LPA1a_WindowIncludesTipMinusHistoryWindow(t *testing.T) {
	c := New(time.Hour)
	h := int64(200_000)
	floor := h - HistoryWindow
	c.Observe(hdr(floor, 1))
	c.Observe(hdr(h, 2))

	got, err := c.At(context.Background(), floor)
	require.NoError(t, err)
	require.Equal(t, floor, got.Height)
	_, err = c.At(context.Background(), floor-1)
	require.Error(t, err)
	got, err = c.At(context.Background(), h)
	require.NoError(t, err)
	require.Equal(t, h, got.Height)
}

func TestCache_LPA1b_AdvancingTipEvictsOldFloor(t *testing.T) {
	c := New(time.Hour)
	h := int64(200_000)
	floor := h - HistoryWindow
	c.Observe(hdr(floor, 1))
	c.Observe(hdr(floor+1, 2))
	c.Observe(hdr(h, 3))
	_, err := c.At(context.Background(), floor)
	require.NoError(t, err)

	c.Observe(hdr(h+1, 4))
	_, err = c.At(context.Background(), floor)
	require.Error(t, err, "old floor evicted when tip advances")
	got, err := c.At(context.Background(), floor+1)
	require.NoError(t, err)
	require.Equal(t, floor+1, got.Height)
}

func TestCache_LPA1c_RememberDoesNotAdvanceTip(t *testing.T) {
	c := New(time.Hour)
	c.Remember(hdr(8, 8))
	_, err := c.Latest(context.Background())
	require.Error(t, err)
	require.True(t, c.Stale())
	got, err := c.At(context.Background(), 8)
	require.NoError(t, err)
	require.Equal(t, int64(8), got.Height)

	c.Observe(hdr(100, 100))
	c.Remember(hdr(90, 90))
	got, err = c.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(100), got.Height)
	require.False(t, c.Stale())
	got, err = c.At(context.Background(), 90)
	require.NoError(t, err)
	require.Equal(t, int64(90), got.Height)
}

func TestCache_RememberOutsideWindowDropped(t *testing.T) {
	c := New(time.Hour)
	tip := int64(HistoryWindow + 50)
	c.Observe(hdr(tip, 1))
	c.Remember(hdr(1, 1))
	_, err := c.At(context.Background(), 1)
	require.Error(t, err)
}

func TestCache_NonAdvancingObserveKeepsWindow(t *testing.T) {
	c := New(time.Hour)
	c.Observe(hdr(100, 100))
	c.Remember(hdr(90, 90))
	c.Observe(hdr(99, 99))
	got, err := c.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(100), got.Height)
	got, err = c.At(context.Background(), 90)
	require.NoError(t, err)
	require.Equal(t, int64(90), got.Height)
}

func TestCache_TipJumpEvictsBelowNewFloor(t *testing.T) {
	c := New(time.Hour)
	c.Observe(hdr(50, 50))
	c.Observe(hdr(200_000, 1))
	_, err := c.At(context.Background(), 50)
	require.Error(t, err)
	got, err := c.At(context.Background(), 200_000)
	require.NoError(t, err)
	require.Equal(t, int64(200_000), got.Height)
}

func TestCache_StoredOldestSparseTip(t *testing.T) {
	c := New(time.Hour)
	require.Equal(t, int64(0), c.StoredOldest())
	c.Observe(hdr(200_000, 1))
	require.Equal(t, int64(200_000), c.StoredOldest())
	c.Remember(hdr(150_000, 2))
	require.Equal(t, int64(150_000), c.StoredOldest())
}

func TestCache_StoredOldestAfterTipJump(t *testing.T) {
	c := New(time.Hour)
	c.Observe(hdr(50, 50))
	c.Observe(hdr(200_000, 1))
	require.Equal(t, int64(200_000), c.StoredOldest())
}

func TestCache_SameHeightHashChangeWakesCaughtUpWaiter(t *testing.T) {
	c := New(time.Hour)
	c.Observe(hdr(10, 10))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch, err := c.Subscribe(ctx, 11)
	require.NoError(t, err)

	c.Observe(hdr(10, 10))
	select {
	case <-ch:
		t.Fatal("duplicate Observe must not wake a from=tip+1 waiter")
	case <-time.After(50 * time.Millisecond):
	}

	c.Observe(blocks.HashOnlyHeader(10, time.Unix(10, 0).UTC(), "gonka", []byte{0xff}))
	select {
	case h := <-ch:
		require.Equal(t, int64(10), h.Height)
		require.Equal(t, []byte{0xff}, h.BlockHash)
	case <-ctx.Done():
		t.Fatal("same-height hash change did not wake from=tip+1 waiter")
	}
}

func TestCache_ObserveDuringUnsubscribeNoPanic(t *testing.T) {
	c := New(time.Hour)
	c.Observe(hdr(1, 1))
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			ch, err := c.Subscribe(ctx, 1)
			require.NoError(t, err)
			cancel()
			for range ch {
			}
		}()
		c.Observe(hdr(int64(i+2), byte(i+2)))
	}
	wg.Wait()
}

func TestCache_LPA1d_DummyNotStored(t *testing.T) {
	c := New(time.Hour)
	c.Observe(blocks.DummyHeader(3))
	c.Remember(blocks.DummyHeader(3))
	_, err := c.At(context.Background(), 3)
	require.Error(t, err)
	_, err = c.Latest(context.Background())
	require.Error(t, err)
}
