package heightsync_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"devshard/chainoracle/blocks"
	blockclient "devshard/chainoracle/blocks/client"
	"devshard/chainoracle/blocks/failover"
	"devshard/heightsync"

	"github.com/stretchr/testify/require"
)

type recChain struct {
	hdr *blocks.Header
	err error
}

func (r *recChain) Latest(context.Context) (*blocks.Header, error) {
	if r.err != nil {
		return nil, r.err
	}
	cp := *r.hdr
	cp.BlockHash = append([]byte(nil), r.hdr.BlockHash...)
	return &cp, nil
}
func (r *recChain) At(ctx context.Context, _ int64) (*blocks.Header, error) { return r.Latest(ctx) }
func (r *recChain) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}
func (r *recChain) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func newChainOracleHTTP(t *testing.T, h http.Handler) *blockclient.Client {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	cli, err := blockclient.NewHTTP(context.Background(), blockclient.HTTPConfig{
		BaseURL:          ts.URL,
		ResubscribeAfter: 20 * time.Millisecond,
		StaleAfter:       time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(cli.Close)
	return cli
}

func TestHostOracle_BlockLatest200_UsesChainOracle(t *testing.T) {
	hdr := blocks.HashOnlyHeader(7, time.Unix(1, 0).UTC(), "gonka-test", []byte{1, 2, 3, 4})
	httpCli := newChainOracleHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/block/latest" {
			_ = json.NewEncoder(w).Encode(hdr)
			return
		}
		if r.URL.Path == "/block/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			<-r.Context().Done()
			return
		}
		http.NotFound(w, r)
	}))
	o := failover.New(httpCli, &recChain{hdr: blocks.HashOnlyHeader(99, time.Unix(2, 0).UTC(), "gonka-test", []byte{9})}, failover.Config{ProbeInterval: time.Minute}, nil)
	sec, err, miss := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, o).Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, int64(7), sec.MainnetHeight)
}

func TestHostOracle_BlockLatest404_FallsBackToChain(t *testing.T) {
	httpCli := newChainOracleHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	o := failover.New(httpCli, &recChain{hdr: blocks.HashOnlyHeader(99, time.Unix(2, 0).UTC(), "gonka-test", []byte{9, 9})}, failover.Config{ProbeInterval: time.Minute}, nil)
	sec, err, miss := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, o).Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, int64(99), sec.MainnetHeight)
}

func TestHostOracle_DapiAndChainMissing_OmitsAndStale(t *testing.T) {
	httpCli := newChainOracleHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	o := failover.New(httpCli, &recChain{err: errors.New("chain down")}, failover.Config{ProbeInterval: time.Minute}, nil)
	sec, err, miss := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, o).Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.True(t, miss)
	require.Nil(t, sec)
	require.Equal(t, heightsync.ConfirmStale, heightsync.NewConfirmationIndex(heightsync.ConfirmationConfig{
		Roster: []string{"host-a"},
		Oracle: o,
	}).IsStrictlyConfirmed(1))
}
