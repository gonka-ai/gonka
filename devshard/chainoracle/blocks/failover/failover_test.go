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
func (r *recOracle) At(ctx context.Context, _ int64) (*blocks.Header, error) {
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

func serveDapi(t *testing.T, latest http.HandlerFunc, prove http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/block/latest", latest)
	if prove != nil {
		mux.HandleFunc("/block/{height}/prove", prove)
	}
	mux.HandleFunc("/block/{height}", func(w http.ResponseWriter, r *http.Request) {
		writeHeader(w, dapiHdr())
	})
	mux.HandleFunc("/block/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func writeHeader(w http.ResponseWriter, h *blocks.Header) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h)
}

func httpOracle(t *testing.T, base string) *blockclient.Client {
	t.Helper()
	cli, err := blockclient.NewHTTP(context.Background(), blockclient.HTTPConfig{
		BaseURL:          base,
		ResubscribeAfter: 20 * time.Millisecond,
		StaleAfter:       time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(cli.Close)
	return cli
}

func TestHostOracle_BlockLatest200_UsesChainOracle(t *testing.T) {
	ts := serveDapi(t, func(w http.ResponseWriter, _ *http.Request) {
		writeHeader(w, dapiHdr())
	}, nil)
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(httpOracle(t, ts.URL), chain, failover.Config{ProbeInterval: time.Minute}, nil)

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Empty(t, h.Commit.Signatures)
	require.Equal(t, int64(0), chain.calls.Load(), "Comet/direct chain must not be touched on 200")
}

func TestHostOracle_BlockLatest404_FallsBackToChain(t *testing.T) {
	ts := serveDapi(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}, nil)
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(httpOracle(t, ts.URL), chain, failover.Config{ProbeInterval: time.Minute}, nil)

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Greater(t, chain.calls.Load(), int64(0))
	require.True(t, o.Legacy(), "404 is a capability miss; stay on chain until restart")
}

func TestHostOracle_DapiAndChainMissing_OmitsAndStale(t *testing.T) {
	ts := serveDapi(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}, nil)
	chain := &recOracle{err: errors.New("chain down")}
	o := failover.New(httpOracle(t, ts.URL), chain, failover.Config{ProbeInterval: time.Minute}, nil)

	_, err := o.Latest(context.Background())
	require.Error(t, err)
	require.True(t, o.Stale())
}

func TestContainerE2E_HeightSync_OldDapiChainOnly(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)

	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(httpOracle(t, ts.URL), chain, failover.Config{ProbeInterval: time.Minute}, nil)
	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.Empty(t, h.Commit.Signatures, "Strong is never claimed on hash-only chain")
	require.True(t, o.Legacy())
}

func TestHostOracle_ProveEndpointAbsent_AnchorUnaffected(t *testing.T) {
	ts := serveDapi(t, func(w http.ResponseWriter, _ *http.Request) {
		writeHeader(w, dapiHdr())
	}, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not implemented", http.StatusNotImplemented)
	})
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(httpOracle(t, ts.URL), chain, failover.Config{ProbeInterval: time.Minute}, nil)

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)

	_, err = o.Prove(context.Background(), "/escrow/1", 7)
	require.ErrorIs(t, err, blocks.ErrProveNotImplemented)
	require.False(t, o.Legacy(), "501 on Prove must not mark the oracle legacy")
	require.Equal(t, int64(0), chain.calls.Load())
}

func TestHostOracle_RuntimeFailover_DapiGoesDown(t *testing.T) {
	var down atomic.Bool
	ts := serveDapi(t, func(w http.ResponseWriter, _ *http.Request) {
		if down.Load() {
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			_ = conn.Close()
			return
		}
		writeHeader(w, dapiHdr())
	}, nil)
	chain := &recOracle{hdr: chainHdr()}
	o := failover.New(httpOracle(t, ts.URL), chain, failover.Config{ProbeInterval: time.Hour}, nil)

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)

	down.Store(true)
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)
	require.False(t, o.Legacy(), "transport failure is not a capability miss")
	require.Greater(t, chain.calls.Load(), int64(0))
}

func TestHostOracle_RuntimeFailback_DapiRecovers(t *testing.T) {
	var down atomic.Bool
	ts := serveDapi(t, func(w http.ResponseWriter, _ *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		writeHeader(w, dapiHdr())
	}, nil)
	chain := &recOracle{hdr: chainHdr()}
	now := time.Unix(1_000_000, 0)
	o := failover.New(httpOracle(t, ts.URL), chain, failover.Config{
		ProbeInterval: time.Minute,
		Now:           func() time.Time { return now },
	}, nil)

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)

	down.Store(true)
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height)

	down.Store(false)
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), h.Height, "must stay on chain until probe interval")

	now = now.Add(time.Minute)
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height, "recovered dapi becomes primary without restart")
}
