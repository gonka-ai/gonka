package host

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"common/chainoracle/blocks"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

var errFakeOracleNotImpl = errors.New("fakeOracle: not implemented")

type fakeOracle struct {
	height atomic.Int64
	errVal atomic.Pointer[error]
	stale  atomic.Bool

	latestCalls atomic.Int64
	hash        atomic.Value // []byte
}

func (f *fakeOracle) setHeight(h int64) { f.height.Store(h) }
func (f *fakeOracle) setErr(err error) {
	if err == nil {
		f.errVal.Store(nil)
		return
	}
	e := err
	f.errVal.Store(&e)
}

func (f *fakeOracle) setHash(h []byte) { f.hash.Store(append([]byte(nil), h...)) }
func (f *fakeOracle) setStale(v bool)  { f.stale.Store(v) }
func (f *fakeOracle) Stale() bool      { return f.stale.Load() }

func (f *fakeOracle) Latest(ctx context.Context) (*blocks.Header, error) {
	f.latestCalls.Add(1)
	if p := f.errVal.Load(); p != nil {
		return nil, *p
	}
	h := f.height.Load()
	var hash []byte
	if v := f.hash.Load(); v != nil {
		hash = append([]byte(nil), v.([]byte)...)
	}
	return &blocks.Header{Height: h, ChainID: "fake-chain", BlockHash: hash}, nil
}

func (f *fakeOracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	return nil, errFakeOracleNotImpl
}

func (f *fakeOracle) Prove(ctx context.Context, path string, height int64) (*blocks.Proof, error) {
	return nil, errFakeOracleNotImpl
}

func (f *fakeOracle) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blocks.Header, error) {
	return nil, errFakeOracleNotImpl
}

type blockingOracle struct {
	entered chan struct{}
	release chan struct{}
	hdr     *blocks.Header
}

func (o *blockingOracle) Latest(ctx context.Context) (*blocks.Header, error) {
	select {
	case <-o.entered:
	default:
		close(o.entered)
	}
	select {
	case <-o.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}

func (o *blockingOracle) At(context.Context, int64) (*blocks.Header, error) {
	return nil, errFakeOracleNotImpl
}

func (o *blockingOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, errFakeOracleNotImpl
}

func (o *blockingOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	return nil, errFakeOracleNotImpl
}

func newHostWithOracleOpts(t *testing.T, opts ...HostOption) *Host {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[0], engine, "escrow-1", group, nil, opts...)
	require.NoError(t, err)
	_ = storage.Storage(nil)
	_ = types.Diff{}
	return h
}

func TestHost_LatestHeight_ReturnsErrorWhenUnwired(t *testing.T) {
	h := newHostWithOracleOpts(t)
	got, err := h.LatestHeight(context.Background())
	require.ErrorIs(t, err, ErrNoChainOracle)
	require.Equal(t, int64(0), got)
	require.Nil(t, h.ChainOracle(), "ChainOracle() must reflect unwired state")
}

func TestHost_LatestHeight_ReturnsOracleHeight(t *testing.T) {
	f := &fakeOracle{}
	f.setHeight(1234567)
	h := newHostWithOracleOpts(t, WithChainOracle(f))

	got, err := h.LatestHeight(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1234567), got)
	require.Equal(t, int64(1), f.latestCalls.Load())
	require.Same(t, f, h.ChainOracle(), "ChainOracle() must expose the injected instance verbatim")
}

func TestHost_LatestHeight_ReflectsOracleUpdates(t *testing.T) {
	f := &fakeOracle{}
	f.setHeight(100)
	h := newHostWithOracleOpts(t, WithChainOracle(f))

	got1, err := h.LatestHeight(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(100), got1)

	f.setHeight(101)
	got2, err := h.LatestHeight(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(101), got2)
	require.Equal(t, int64(2), f.latestCalls.Load())
}

func TestHost_LatestHeight_PropagatesOracleError(t *testing.T) {
	f := &fakeOracle{}
	sentinel := errors.New("upstream unreachable")
	f.setErr(sentinel)
	h := newHostWithOracleOpts(t, WithChainOracle(f))

	got, err := h.LatestHeight(context.Background())
	require.ErrorIs(t, err, sentinel)
	require.NotErrorIs(t, err, ErrNoChainOracle)
	require.Equal(t, int64(0), got)
}

func TestHost_LatestHeight_NilOptionIsNoop(t *testing.T) {
	h := newHostWithOracleOpts(t, WithChainOracle(nil))
	got, err := h.LatestHeight(context.Background())
	require.ErrorIs(t, err, ErrNoChainOracle)
	require.Equal(t, int64(0), got)
}

func TestHost_LatestHeight_CanceledContext(t *testing.T) {
	f := &fakeOracle{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.setErr(ctx.Err())
	h := newHostWithOracleOpts(t, WithChainOracle(f))

	_, err := h.LatestHeight(ctx)
	require.ErrorIs(t, err, context.Canceled)
}
