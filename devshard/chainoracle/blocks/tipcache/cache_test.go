package tipcache_test

import (
	"context"
	"testing"
	"time"

	"devshard/chainoracle/blocks"
	"devshard/chainoracle/blocks/tipcache"

	"github.com/stretchr/testify/require"
)

func TestCache_ObserveLatestAndSubscribe(t *testing.T) {
	c := tipcache.New(time.Hour)
	_, err := c.Latest(context.Background())
	require.Error(t, err)
	require.True(t, c.Stale())

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
