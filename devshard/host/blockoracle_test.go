package host

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/blockoracle"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

// --- fakeOracle: minimal blockoracle.BlockOracle stub for seam tests. ---
//
// Only Latest is exercised in tree today; At/Prove/Subscribe return
// ErrNotImplemented so accidental new callers fail loudly instead of
// silently stamping zero values. atomicHeight + swappable err model the
// two things a cPoC stamping test cares about: stable reads and
// surfaced upstream failures.

var errFakeOracleNotImpl = errors.New("fakeOracle: not implemented")

type fakeOracle struct {
	height atomic.Int64
	errVal atomic.Pointer[error]

	latestCalls atomic.Int64
}

func (f *fakeOracle) setHeight(h int64) { f.height.Store(h) }
func (f *fakeOracle) setErr(err error) {
	if err == nil {
		f.errVal.Store(nil)
		return
	}
	// atomic.Pointer requires a non-nil pointer; store by copy.
	e := err
	f.errVal.Store(&e)
}

func (f *fakeOracle) Latest(ctx context.Context) (*blockoracle.Header, error) {
	f.latestCalls.Add(1)
	if p := f.errVal.Load(); p != nil {
		return nil, *p
	}
	h := f.height.Load()
	return &blockoracle.Header{Height: h, ChainID: "fake-chain"}, nil
}

func (f *fakeOracle) At(ctx context.Context, height int64) (*blockoracle.Header, error) {
	return nil, errFakeOracleNotImpl
}

func (f *fakeOracle) Prove(ctx context.Context, path string, height int64) (*blockoracle.Proof, error) {
	return nil, errFakeOracleNotImpl
}

func (f *fakeOracle) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blockoracle.Header, error) {
	return nil, errFakeOracleNotImpl
}

// --- local test helper ---
//
// newHostWithOracleOpts builds a minimal Host with room for arbitrary
// HostOptions so tests can wire (or omit) WithBlockOracle without
// pulling the full newTestHost ceremony.

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
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier)
	require.NoError(t, err)
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[0], engine, "escrow-1", group, nil, opts...)
	require.NoError(t, err)
	// touch storage package so tests that don't use it still exercise
	// the import wiring used by the full host suite (matches existing
	// test helpers' import discipline).
	_ = storage.Storage(nil)
	_ = types.Diff{}
	return h
}

// --- Tests ---

func TestHost_LatestHeight_ReturnsErrorWhenUnwired(t *testing.T) {
	// Host with no WithBlockOracle option must refuse to stamp a height
	// rather than return zero. cPoC treats ErrNoBlockOracle as a
	// configuration error surfaced to the operator.
	h := newHostWithOracleOpts(t)
	got, err := h.LatestHeight(context.Background())
	require.ErrorIs(t, err, ErrNoBlockOracle)
	require.Equal(t, int64(0), got)
	require.Nil(t, h.BlockOracle(), "BlockOracle() must reflect unwired state")
}

func TestHost_LatestHeight_ReturnsOracleHeight(t *testing.T) {
	f := &fakeOracle{}
	f.setHeight(1234567)
	h := newHostWithOracleOpts(t, WithBlockOracle(f))

	got, err := h.LatestHeight(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1234567), got)
	require.Equal(t, int64(1), f.latestCalls.Load())
	require.Same(t, f, h.BlockOracle(), "BlockOracle() must expose the injected instance verbatim")
}

func TestHost_LatestHeight_ReflectsOracleUpdates(t *testing.T) {
	// Stamping must read through to the oracle's current height on
	// every call. Caching live here would freeze H(V) and silently
	// break freshness bounds; guard against a future regression.
	f := &fakeOracle{}
	f.setHeight(100)
	h := newHostWithOracleOpts(t, WithBlockOracle(f))

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
	// An oracle failure must surface unchanged so callers can
	// distinguish unwired (ErrNoBlockOracle) from transient upstream
	// failure and defer stamping rather than retrying in-place.
	f := &fakeOracle{}
	sentinel := errors.New("upstream unreachable")
	f.setErr(sentinel)
	h := newHostWithOracleOpts(t, WithBlockOracle(f))

	got, err := h.LatestHeight(context.Background())
	require.ErrorIs(t, err, sentinel)
	require.NotErrorIs(t, err, ErrNoBlockOracle)
	require.Equal(t, int64(0), got)
}

func TestHost_LatestHeight_NilOptionIsNoop(t *testing.T) {
	// WithBlockOracle(nil) must be a no-op; accidentally passing a nil
	// interface value is common in wiring code, and the seam must not
	// leave the host in a state where LatestHeight panics or silently
	// pretends a zero-height oracle is present.
	h := newHostWithOracleOpts(t, WithBlockOracle(nil))
	got, err := h.LatestHeight(context.Background())
	require.ErrorIs(t, err, ErrNoBlockOracle)
	require.Equal(t, int64(0), got)
}

func TestHost_LatestHeight_CanceledContext(t *testing.T) {
	// Context cancellation from the caller must propagate through the
	// oracle implementation; fake honors ctx only via the oracle's
	// own error channel, so simulate by injecting ctx.Err() via setErr.
	f := &fakeOracle{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.setErr(ctx.Err())
	h := newHostWithOracleOpts(t, WithBlockOracle(f))

	_, err := h.LatestHeight(ctx)
	require.ErrorIs(t, err, context.Canceled)
}
