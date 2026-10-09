package failover_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"devshard/chainoracle/blocks/failover"
	"devshard/chainoracle/blocks/tipcache"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
func (r *recOracle) At(_ context.Context, height int64) (*blocks.Header, error) {
	r.calls.Add(1)
	if r.err != nil {
		return nil, r.err
	}
	if r.hdr == nil || r.hdr.Height != height {
		return nil, blocks.ErrHeaderNotFound
	}
	cp := *r.hdr
	cp.BlockHash = append([]byte(nil), r.hdr.BlockHash...)
	return &cp, nil
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
	o.SetCometConnected(true)

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
	h, err := o.At(context.Background(), 99)
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

func TestOracle_DisconnectedCometSkipsCache(t *testing.T) {
	nm := &recOracle{hdr: nmHdr()}
	cache := tipcache.New(failover.CometMaxAge)
	cache.Observe(chainHdr())
	o := failover.New(cache, nm, nil)

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height, "disconnected Comet must not serve the cached tip")
	require.Equal(t, int64(1), nm.calls.Load())
}

type staleOracle struct {
	recOracle
	stale bool
}

func (s *staleOracle) Stale() bool { return s.stale }

func TestOracle_StaleCometFallsThroughToNM(t *testing.T) {
	nm := &recOracle{hdr: nmHdr()}
	tip := &staleOracle{recOracle: recOracle{hdr: chainHdr()}}
	o := failover.New(tip, nm, nil)
	o.SetCometConnected(true)

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Equal(t, int64(0), nm.calls.Load())

	tip.stale = true
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, int64(1), nm.calls.Load())
}

type timedTip struct {
	recOracle
	at time.Time
}

func (t *timedTip) LastObservedAt() time.Time { return t.at }

func TestOracle_CometMaxAgeAuthoritativeOverCacheStaleAfter(t *testing.T) {
	// Failover is authoritative: Cache.Stale() / staleAfter do not cut Latest.
	// A 10s cache still serves Comet until CometMaxAge (20s) on Oracle.now().
	nm := &recOracle{hdr: nmHdr()}
	cache := tipcache.New(10 * time.Second)
	cache.Observe(chainHdr())
	last := cache.LastObservedAt()
	require.False(t, last.IsZero())
	o := failover.New(cache, nm, nil)
	o.SetCometConnected(true)

	o.SetNow(func() time.Time { return last.Add(15 * time.Second) })
	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height, "failover clock 15s: past cache staleAfter 10s, still inside CometMaxAge")
	require.Equal(t, int64(0), nm.calls.Load())
	require.False(t, o.Stale())

	o.SetNow(func() time.Time { return last.Add(failover.CometMaxAge) })
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height, "age == CometMaxAge is still served")
	require.Equal(t, int64(0), nm.calls.Load())

	o.SetNow(func() time.Time { return last.Add(failover.CometMaxAge + time.Millisecond) })
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height, "past CometMaxAge must skip the cache")
	require.Equal(t, int64(1), nm.calls.Load())
}

func TestOracle_StaleUsesLastObservedAtNotLatest(t *testing.T) {
	tip := &timedTip{recOracle: recOracle{hdr: chainHdr()}, at: time.Now()}
	o := failover.New(tip, nil, nil)
	o.SetCometConnected(true)
	require.False(t, o.Stale())
	require.Equal(t, int64(0), tip.calls.Load(), "Stale must not clone via Latest")
}

func TestOracle_StaleDetailsNeverReceived(t *testing.T) {
	nm := &recOracle{hdr: nmHdr()}
	o := failover.New(tipcache.New(time.Hour), nm, nil)
	stale, age, height, never := o.StaleDetails()
	require.True(t, never)
	require.Equal(t, int64(0), age)
	require.Equal(t, int64(0), height)
	require.False(t, stale, "unfetched failover is not stale")
	require.Equal(t, int64(0), nm.calls.Load(), "StaleDetails must not RPC GetBlockHeader")
}

func TestOracle_StaleDetailsFromCometObserve(t *testing.T) {
	nm := &recOracle{hdr: nmHdr()}
	cache := tipcache.New(time.Hour)
	cache.Observe(chainHdr())
	o := failover.New(cache, nm, nil)
	o.SetCometConnected(true)
	last := cache.LastObservedAt()
	o.SetNow(func() time.Time { return last.Add(12 * time.Second) })

	stale, age, height, never := o.StaleDetails()
	require.False(t, never)
	require.False(t, stale)
	require.Equal(t, int64(99), height)
	require.Equal(t, int64(12_000), age)
	require.Equal(t, int64(0), nm.calls.Load(), "StaleDetails must not RPC GetBlockHeader")
}

func TestOracle_NMUnavailableBackoffThenChain(t *testing.T) {
	nm := &recOracle{err: status.Error(codes.Unavailable, "dapi down")}
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(tipcache.New(failover.CometMaxAge), nm, chain)
	now := time.Now()
	o.SetNow(func() time.Time { return now })

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Equal(t, int64(1), nm.calls.Load())
	require.Equal(t, int64(1), chain.calls.Load())

	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Equal(t, int64(1), nm.calls.Load(), "GetBlockHeader must stay in 1m backoff")
	require.Equal(t, int64(1), chain.calls.Load(), "GetLatestBlock reused inside 10s")

	o.SetNow(func() time.Time { return now.Add(time.Minute + time.Second) })
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Equal(t, int64(2), nm.calls.Load())
	require.Equal(t, int64(2), chain.calls.Load())
}

func TestOracle_NMNotFoundBackoffThenChain(t *testing.T) {
	nm := &recOracle{err: blocks.ErrHeaderNotFound}
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(nil, nm, chain)
	now := time.Now()
	o.SetNow(func() time.Time { return now })

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Equal(t, int64(1), nm.calls.Load())

	nm.err = nil
	nm.hdr = nmHdr()
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height, "chain cache while GetBlockHeader is in backoff")
	require.Equal(t, int64(1), nm.calls.Load(), "ErrHeaderNotFound must back off GetBlockHeader")

	o.SetNow(func() time.Time { return now.Add(time.Minute + time.Second) })
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, int64(2), nm.calls.Load())
}

func TestOracle_NMDummyHeaderBackoffThenChain(t *testing.T) {
	nm := &recOracle{hdr: blocks.DummyHeader(0)}
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(nil, nm, chain)
	now := time.Now()
	o.SetNow(func() time.Time { return now })

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.False(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(1), nm.calls.Load())

	nm.hdr = nmHdr()
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Equal(t, int64(1), nm.calls.Load(), "dummy GetBlockHeader must back off")

	o.SetNow(func() time.Time { return now.Add(time.Minute + time.Second) })
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, int64(2), nm.calls.Load())
}

func TestOracle_AtNotFoundDoesNotBackoffLatest(t *testing.T) {
	nm := &recOracle{hdr: nmHdr()}
	o := failover.New(nil, nm, nil)

	h, err := o.At(context.Background(), 50)
	require.NoError(t, err)
	require.True(t, blocks.IsDummyHeader(h))

	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, int64(2), nm.calls.Load(), "At miss must not put Latest GetBlockHeader in backoff")
}

func TestOracle_NMUnimplementedReprobesEvery15m(t *testing.T) {
	nm := &recOracle{err: blocks.ErrHeaderRPCUnimplemented}
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(nil, nm, chain)
	now := time.Now()
	o.SetNow(func() time.Time { return now })

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Equal(t, int64(1), nm.calls.Load())

	o.SetNow(func() time.Time { return now.Add(failover.NMUnimplementedReprobe - time.Millisecond) })
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Equal(t, int64(1), nm.calls.Load(), "Unimplemented must skip GetBlockHeader until 15m")

	o.SetNow(func() time.Time { return now.Add(failover.NMUnimplementedReprobe + time.Second) })
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Equal(t, int64(2), nm.calls.Load(), "one re-probe after 15m")

	nm.err = nil
	nm.hdr = nmHdr()
	o.SetNow(func() time.Time {
		return now.Add(2*failover.NMUnimplementedReprobe + 2*time.Second)
	})
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, int64(3), nm.calls.Load())

	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, int64(4), nm.calls.Load(), "success must clear the Unimplemented skip")
}

func TestOracle_ChainRefreshServesCachedHeader(t *testing.T) {
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(nil, &recOracle{err: blocks.ErrHeaderNotFound}, chain)
	now := time.Now()
	o.SetNow(func() time.Time { return now })

	_, err := o.Latest(context.Background())
	require.NoError(t, err)
	_, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1), chain.calls.Load())

	o.SetNow(func() time.Time { return now.Add(failover.ChainRefresh + time.Millisecond) })
	_, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(2), chain.calls.Load())
}

func TestOracle_AtLookbackSkipsRPC(t *testing.T) {
	nm := &recOracle{hdr: nmHdr()}
	chain := &recOracle{hdr: chainHdr()}
	cache := tipcache.New(time.Hour)
	tip := blocks.HashOnlyHeader(500, time.Unix(1_700_000_500, 0).UTC(), "gonka-test", []byte{5, 0, 0})
	cache.Observe(tip)
	o := failover.New(cache, nm, chain)

	h, err := o.At(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(1), h.Height)
	require.Equal(t, int64(0), nm.calls.Load(), "height below AtLookback must not hit NodeManager")
	require.Equal(t, int64(0), chain.calls.Load(), "height below AtLookback must not hit chain")

	inWindow := int64(500 - (failover.AtLookback - 1))
	h, err = o.At(context.Background(), inWindow)
	require.NoError(t, err)
	require.True(t, blocks.IsDummyHeader(h), "in-window miss still dummies when backends lack that height")
	require.Equal(t, int64(1), nm.calls.Load())
	require.Equal(t, int64(1), chain.calls.Load())
}

func TestOracle_AtUnimplementedSkipsNM(t *testing.T) {
	nm := &recOracle{err: blocks.ErrHeaderRPCUnimplemented}
	chain := &recOracle{hdr: blocks.HashOnlyHeader(450, time.Unix(1, 0).UTC(), "gonka-test", []byte{4, 5, 0})}
	cache := tipcache.New(failover.CometMaxAge)
	cache.Observe(blocks.HashOnlyHeader(500, time.Unix(1_700_000_500, 0).UTC(), "gonka-test", []byte{5, 0, 0}))
	o := failover.New(cache, nm, chain)

	_, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1), nm.calls.Load())
	require.Equal(t, int64(1), chain.calls.Load())

	h, err := o.At(context.Background(), 300)
	require.NoError(t, err)
	require.True(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(1), nm.calls.Load(), "Unimplemented skip must still apply to At")
	require.Equal(t, int64(1), chain.calls.Load(), "At below lookback must not hit chain")

	h, err = o.At(context.Background(), 450)
	require.NoError(t, err)
	require.False(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(450), h.Height)
	require.Equal(t, int64(1), nm.calls.Load(), "in-window At during Unimplemented skip still skips nm")
	require.Equal(t, int64(2), chain.calls.Load())
}

func TestOracle_AtNegativeCacheSuppressesRPC(t *testing.T) {
	nm := &recOracle{err: blocks.ErrHeaderNotFound}
	chain := &recOracle{err: errors.New("down")}
	o := failover.New(nil, nm, chain)
	now := time.Now()
	o.SetNow(func() time.Time { return now })

	h, err := o.At(context.Background(), 50)
	require.NoError(t, err)
	require.True(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(1), nm.calls.Load())
	require.Equal(t, int64(1), chain.calls.Load())

	h, err = o.At(context.Background(), 50)
	require.NoError(t, err)
	require.True(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(1), nm.calls.Load(), "negative cache must skip nm")
	require.Equal(t, int64(1), chain.calls.Load(), "negative cache must skip chain")

	o.SetNow(func() time.Time { return now.Add(failover.AtNegativeTTL + time.Millisecond) })
	h, err = o.At(context.Background(), 50)
	require.NoError(t, err)
	require.True(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(2), nm.calls.Load())
	require.Equal(t, int64(2), chain.calls.Load())
}

func TestOracle_AtBackoffSkipsNM(t *testing.T) {
	nm := &recOracle{err: status.Error(codes.Unavailable, "dapi down")}
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(nil, nm, chain)
	now := time.Now()
	o.SetNow(func() time.Time { return now })

	_, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1), nm.calls.Load())

	h, err := o.At(context.Background(), 99)
	require.NoError(t, err)
	require.False(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(99), h.Height)
	require.Equal(t, int64(1), nm.calls.Load(), "At must honor Latest GetBlockHeader backoff")
	require.Equal(t, int64(2), chain.calls.Load())
}

type gateAtOracle struct {
	hdr     *blocks.Header
	calls   atomic.Int64
	release chan struct{}
}

func (g *gateAtOracle) Latest(context.Context) (*blocks.Header, error) {
	return nil, blocks.ErrHeaderNotFound
}
func (g *gateAtOracle) At(context.Context, int64) (*blocks.Header, error) {
	g.calls.Add(1)
	<-g.release
	cp := *g.hdr
	cp.BlockHash = append([]byte(nil), g.hdr.BlockHash...)
	return &cp, nil
}
func (g *gateAtOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}
func (g *gateAtOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func TestOracle_AtSingleflightSharesRPC(t *testing.T) {
	g := &gateAtOracle{
		hdr:     nmHdr(),
		release: make(chan struct{}),
	}
	o := failover.New(nil, g, nil)

	var started atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2)
	got := make([]*blocks.Header, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			started.Add(1)
			got[i], errs[i] = o.At(context.Background(), 7)
		}()
	}
	require.Eventually(t, func() bool {
		return started.Load() == 2 && g.calls.Load() == 1
	}, time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	close(g.release)
	wg.Wait()

	require.Equal(t, int64(1), g.calls.Load())
	for i := 0; i < 2; i++ {
		require.NoError(t, errs[i])
		require.Equal(t, int64(7), got[i].Height)
		require.Equal(t, []byte{1, 2, 3, 4}, got[i].BlockHash)
	}
}
