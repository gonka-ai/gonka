package failover_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"devshard/chainoracle/blocks"
	blockclient "devshard/chainoracle/blocks/client"
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

func dapiHdr() *blocks.Header {
	return blocks.HashOnlyHeader(7, time.Unix(1_700_000_000, 0).UTC(), "gonka-test", []byte{1, 2, 3, 4})
}

func TestOracle_LatestUsesTipNotHTTP(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("tip must not hit HTTP: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)
	lookup, err := blockclient.NewLookup(blockclient.HTTPConfig{BaseURL: ts.URL})
	require.NoError(t, err)
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(&recOracle{hdr: dapiHdr()}, lookup, chain)

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, int64(0), chain.calls.Load())
}

func TestOracle_AtMissingRouteReturnsDummy(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)
	lookup, err := blockclient.NewLookup(blockclient.HTTPConfig{BaseURL: ts.URL})
	require.NoError(t, err)
	o := failover.New(&recOracle{hdr: chainHdr()}, lookup, nil)

	h, err := o.At(context.Background(), 42)
	require.NoError(t, err)
	require.True(t, blocks.IsDummyHeader(h))
	require.Equal(t, int64(42), h.Height)
}

func TestOracle_At404DoesNotUseChain(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)
	lookup, err := blockclient.NewLookup(blockclient.HTTPConfig{BaseURL: ts.URL})
	require.NoError(t, err)
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(&recOracle{hdr: dapiHdr()}, lookup, chain)
	h, err := o.At(context.Background(), 42)
	require.NoError(t, err)
	require.True(t, blocks.IsDummyHeader(h), "old dapi 404 is dummy, not chain At")
	require.Equal(t, int64(0), chain.calls.Load())
}

func TestOracle_AtHitsBlockHeight(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/block/7", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dapiHdr())
	}))
	t.Cleanup(ts.Close)
	lookup, err := blockclient.NewLookup(blockclient.HTTPConfig{BaseURL: ts.URL})
	require.NoError(t, err)
	o := failover.New(&recOracle{hdr: chainHdr()}, lookup, nil)

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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/block/7/prove" {
			http.Error(w, "not implemented", http.StatusNotImplemented)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)
	lookup, err := blockclient.NewLookup(blockclient.HTTPConfig{BaseURL: ts.URL})
	require.NoError(t, err)
	o := failover.New(&recOracle{hdr: chainHdr()}, lookup, nil)
	_, err = o.Prove(context.Background(), "/escrow/1", 7)
	require.ErrorIs(t, err, blocks.ErrProveNotImplemented)
}

func TestOracle_LatestFallsBackToChainWhenTipEmpty(t *testing.T) {
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(&recOracle{err: errors.New("no comet yet")}, nil, chain)
	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Greater(t, chain.calls.Load(), int64(0))
}

func TestOracle_AtDapiDownFallsBackToChain(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)
	lookup, err := blockclient.NewLookup(blockclient.HTTPConfig{BaseURL: ts.URL})
	require.NoError(t, err)
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(&recOracle{hdr: dapiHdr()}, lookup, chain)
	h, err := o.At(context.Background(), 99)
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Equal(t, []byte{9, 9, 9, 9}, h.BlockHash)
	require.Greater(t, chain.calls.Load(), int64(0))
}

func TestOracle_AtUsesCachedWindow(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("cached At must not hit HTTP: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)
	lookup, err := blockclient.NewLookup(blockclient.HTTPConfig{BaseURL: ts.URL})
	require.NoError(t, err)
	cache := tipcache.New(time.Hour)
	cache.Observe(dapiHdr())
	o := failover.New(cache, lookup, nil)

	h, err := o.At(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, []byte{1, 2, 3, 4}, h.BlockHash)
}

func TestOracle_AtRemembersHTTP(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		require.Equal(t, "/block/7", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dapiHdr())
	}))
	t.Cleanup(ts.Close)
	lookup, err := blockclient.NewLookup(blockclient.HTTPConfig{BaseURL: ts.URL})
	require.NoError(t, err)
	cache := tipcache.New(time.Hour)
	o := failover.New(cache, lookup, nil)

	h, err := o.At(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	h, err = o.At(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, int64(1), hits.Load())
	_, err = cache.Latest(context.Background())
	require.Error(t, err, "HTTP At must not become the Comet tip")
}
