package failover_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"devshard/chainoracle/blocks/failover"
	"devshard/chainoracle/blocks/tipcache"

	"github.com/stretchr/testify/require"
)

type recOracle struct {
	hdr   *blocks.Header
	err   error
	calls atomic.Int64
}

func (r *recOracle) Latest(context.Context) (*blocks.Header, error) {
	r.calls.Add(1)
	if r.err != nil {
		return nil, r.err
	}
	if r.hdr == nil {
		return nil, errors.New("no header")
	}
	cp := *r.hdr
	cp.BlockHash = append([]byte(nil), r.hdr.BlockHash...)
	return &cp, nil
}
func (r *recOracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	return r.Latest(ctx)
}
func (r *recOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}
func (r *recOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func chainHdr() *blocks.Header {
	return blocks.HashOnlyHeader(99, time.Unix(1_700_000_100, 0).UTC(), "gonka-test", []byte{9, 9, 9, 9})
}

func nmHdr() *blocks.Header {
	return blocks.HashOnlyHeader(7, time.Unix(1_700_000_000, 0).UTC(), "gonka-test", []byte{1, 2, 3, 4})
}

func TestOracle_LatestPrefersCometCache(t *testing.T) {
	chain := &recOracle{hdr: chainHdr()}
	nm := &recOracle{hdr: nmHdr()}
	tip := &recOracle{hdr: chainHdr()}
	o := failover.New(tip, nm, chain)

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Equal(t, []byte{9, 9, 9, 9}, h.BlockHash)
	require.Equal(t, int64(0), nm.calls.Load())
	require.Equal(t, int64(0), chain.calls.Load())
}

func TestOracle_LatestFallsBackToGetBlockHeaderWhenCacheEmpty(t *testing.T) {
	chain := &recOracle{hdr: chainHdr()}
	nm := &recOracle{hdr: nmHdr()}
	o := failover.New(tipcache.New(time.Hour), nm, chain)
	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, []byte{1, 2, 3, 4}, h.BlockHash)
	require.Equal(t, int64(0), chain.calls.Load(), "GetLatestBlock is not tried when GetBlockHeader succeeds")
}

func TestOracle_LatestFallsBackToGetLatestBlockWhenHeaderMisses(t *testing.T) {
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(tipcache.New(time.Hour), &recOracle{err: blocks.ErrHeaderNotFound}, chain)
	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Equal(t, []byte{9, 9, 9, 9}, h.BlockHash)
	require.Greater(t, chain.calls.Load(), int64(0))
}

func TestOracle_AtMissingRouteReturnsDummy(t *testing.T) {
	o := failover.New(nil, &recOracle{err: blocks.ErrHeaderNotFound}, nil)
	h, err := o.At(context.Background(), 42)
	require.NoError(t, err)
	require.True(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(42), h.Height)
}

func TestOracle_AtNMMissFallsBackToChain(t *testing.T) {
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(nil, &recOracle{err: blocks.ErrHeaderNotFound}, chain)
	h, err := o.At(context.Background(), 42)
	require.NoError(t, err)
	require.False(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(99), h.Height)
	require.Greater(t, chain.calls.Load(), int64(0))
}

func TestOracle_AtUsesNodeManager(t *testing.T) {
	o := failover.New(nil, &recOracle{hdr: nmHdr()}, nil)
	h, err := o.At(context.Background(), 7)
	require.NoError(t, err)
	require.False(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, []byte{1, 2, 3, 4}, h.BlockHash)
}

func TestOracle_NilHistoryAtIsDummy(t *testing.T) {
	o := failover.New(&recOracle{hdr: chainHdr()}, nil, nil)
	h, err := o.At(context.Background(), 3)
	require.NoError(t, err)
	require.True(t, blocks.IsDummyHeader(h))
}

func TestOracle_TipDownIsStale(t *testing.T) {
	o := failover.New(&recOracle{err: errors.New("chain down")}, nil, nil)
	_, err := o.Latest(context.Background())
	require.Error(t, err)
	require.True(t, o.Stale())
}

func TestOracle_ProveAbsent(t *testing.T) {
	o := failover.New(nil, &recOracle{hdr: nmHdr()}, nil)
	_, err := o.Prove(context.Background(), "/escrow/1", 7)
	require.ErrorIs(t, err, blocks.ErrProveNotImplemented)
}

func TestOracle_AtChainDownAfterNMMissIsDummy(t *testing.T) {
	chain := &recOracle{err: errors.New("down")}
	o := failover.New(nil, &recOracle{err: blocks.ErrHeaderNotFound}, chain)
	h, err := o.At(context.Background(), 99)
	require.NoError(t, err)
	require.True(t, blocks.IsDummyHeader(h))
}

func TestOracle_AtUsesCachedWindow(t *testing.T) {
	nm := &recOracle{hdr: nmHdr()}
	cache := tipcache.New(time.Hour)
	cache.Observe(nmHdr())
	o := failover.New(cache, nm, nil)

	h, err := o.At(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, int64(0), nm.calls.Load(), "cached At must not hit NodeManager")
}

func TestOracle_AtRemembersNodeManager(t *testing.T) {
	nm := &recOracle{hdr: nmHdr()}
	cache := tipcache.New(time.Hour)
	o := failover.New(cache, nm, nil)

	h, err := o.At(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	h, err = o.At(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, int64(1), nm.calls.Load())
	_, err = cache.Latest(context.Background())
	require.Error(t, err, "At must not become the Comet tip")
}
