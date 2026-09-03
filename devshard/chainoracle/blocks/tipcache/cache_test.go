package tipcache_test

import (
	"context"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/tipcache"
)

func TestCache_ObserveLatestAndSubscribe(t *testing.T) {
	c := tipcache.New(time.Hour)
	_, err := c.Latest(context.Background())
	require.Error(t, err)
	require.True(t, c.Stale())

	hdr := blocks.HashOnlyHeader(5, time.Unix(10, 0).UTC(), "gonka", []byte{0xaa})
	ctx := t.Context()
	ch, err := c.Subscribe(ctx, 1)
	require.NoError(t, err)

	c.Observe(hdr)
	got, err := c.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(5), got.Height)
	require.Equal(t, []byte{0xaa}, got.BlockHash)
	require.False(t, c.Stale())

	at, err := c.At(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, []byte{0xaa}, at.BlockHash)
	_, err = c.At(context.Background(), 4)
	require.Error(t, err)

	select {
	case h := <-ch:
		require.Equal(t, int64(5), h.Height)
	case <-time.After(time.Second):
		t.Fatal("subscribe missed Observe")
	}
}

func TestWithBootstrap_FallsBackToBoot(t *testing.T) {
	boot := &static{hdr: blocks.HashOnlyHeader(3, time.Unix(1, 0).UTC(), "gonka", []byte{1})}
	w := &tipcache.WithBootstrap{Live: tipcache.New(time.Hour), Boot: boot}
	h, err := w.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(3), h.Height)

	w.Live.Observe(blocks.HashOnlyHeader(9, time.Unix(2, 0).UTC(), "gonka", []byte{2}))
	h, err = w.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(9), h.Height)
}

func TestCache_WindowKeepsLast100(t *testing.T) {
	c := tipcache.New(time.Hour)
	for h := int64(1); h <= tipcache.HistoryWindow+1; h++ {
		c.Observe(blocks.HashOnlyHeader(h, time.Unix(h, 0).UTC(), "gonka", []byte{byte(h)}))
	}
	tip := int64(tipcache.HistoryWindow + 1)
	_, err := c.At(context.Background(), 1)
	require.Error(t, err, "height 1 is outside the window")
	got, err := c.At(context.Background(), tip-(tipcache.HistoryWindow-1))
	require.NoError(t, err)
	require.Equal(t, tip-(tipcache.HistoryWindow-1), got.Height)
	got, err = c.At(context.Background(), tip)
	require.NoError(t, err)
	require.Equal(t, tip, got.Height)
}

func TestCache_RememberDoesNotAdvanceTip(t *testing.T) {
	c := tipcache.New(time.Hour)
	c.Remember(blocks.HashOnlyHeader(8, time.Unix(8, 0).UTC(), "gonka", []byte{8}))
	_, err := c.Latest(context.Background())
	require.Error(t, err)
	require.True(t, c.Stale())
	got, err := c.At(context.Background(), 8)
	require.NoError(t, err)
	require.Equal(t, int64(8), got.Height)

	c.Observe(blocks.HashOnlyHeader(200, time.Unix(200, 0).UTC(), "gonka", []byte{200}))
	_, err = c.At(context.Background(), 8)
	require.Error(t, err, "8 is outside the window of tip 200")
}

func TestCache_RememberOutsideWindowDropped(t *testing.T) {
	c := tipcache.New(time.Hour)
	c.Observe(blocks.HashOnlyHeader(200, time.Unix(200, 0).UTC(), "gonka", []byte{200}))
	c.Remember(blocks.HashOnlyHeader(50, time.Unix(50, 0).UTC(), "gonka", []byte{50}))
	_, err := c.At(context.Background(), 50)
	require.Error(t, err)
}

func TestCache_DummyNotStored(t *testing.T) {
	c := tipcache.New(time.Hour)
	c.Observe(blocks.DummyHeader(3))
	c.Remember(blocks.DummyHeader(3))
	_, err := c.At(context.Background(), 3)
	require.Error(t, err)
	_, err = c.Latest(context.Background())
	require.Error(t, err)
}

type static struct{ hdr *blocks.Header }

func (s *static) Latest(context.Context) (*blocks.Header, error) { return s.hdr, nil }
func (s *static) At(context.Context, int64) (*blocks.Header, error) {
	return s.hdr, nil
}

func (s *static) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (s *static) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}
