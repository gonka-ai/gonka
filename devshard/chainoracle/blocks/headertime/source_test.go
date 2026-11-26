package headertime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"devshard/types"
)

type countingOracle struct {
	headers map[int64]*blocks.Header
	calls   atomic.Int64
}

type movingTipOracle struct {
	countingOracle
	tip *blocks.Header
}

func (o *movingTipOracle) Latest(context.Context) (*blocks.Header, error) { return o.tip, nil }

func TestSourceUsesLiveTipWithoutChangingHistoricalVerification(t *testing.T) {
	at := time.Now()
	history := &countingOracle{headers: map[int64]*blocks.Header{
		10: blocks.HashOnlyHeader(10, at, "c", []byte{10}),
	}}
	live := &movingTipOracle{tip: blocks.HashOnlyHeader(11, at, "c", []byte{11})}
	s := New(history, WithLatestOracle(live))
	h, err := s.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(11), h.Height)
	live.tip = blocks.HashOnlyHeader(12, at, "c", []byte{12})
	h, err = s.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(12), h.Height)
	_, err = s.HeaderTimeMs(10, []byte{10})
	require.NoError(t, err)
	require.Equal(t, int64(1), history.calls.Load())
	require.Zero(t, live.calls.Load(), "history must still come from the verifying oracle")
}

func (o *countingOracle) Latest(context.Context) (*blocks.Header, error) {
	return nil, blocks.ErrHeaderNotFound
}

func (o *countingOracle) At(_ context.Context, height int64) (*blocks.Header, error) {
	o.calls.Add(1)
	if h, ok := o.headers[height]; ok {
		return h, nil
	}
	return blocks.DummyHeader(height), nil
}

func (o *countingOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *countingOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	return nil, blocks.ErrProveNotImplemented
}

func TestSourceReturnsTheBlockTimeOnlyForTheMatchingHash(t *testing.T) {
	at := time.Unix(1_800_000_000, 123_000_000)
	o := &countingOracle{headers: map[int64]*blocks.Header{
		10: blocks.HashOnlyHeader(10, at, "c", []byte{0x0a}),
	}}
	s := New(o)

	ms, err := s.HeaderTimeMs(10, []byte{0x0a})
	require.NoError(t, err)
	require.Equal(t, at.UnixMilli(), ms)

	_, err = s.HeaderTimeMs(10, []byte{0x0b})
	require.ErrorIs(t, err, types.ErrHeaderMismatch)

	_, err = s.HeaderTimeMs(11, []byte{0x0b})
	require.ErrorIs(t, err, types.ErrHeaderPending, "a height the chain has not produced is a wait")

	require.Equal(t, int64(2), o.calls.Load(), "a resolved height is served from the cache")
}

func TestSourceTakesObservedTipHeadersWithoutAFetch(t *testing.T) {
	o := &countingOracle{}
	s := New(o)
	at := time.Unix(1_800_000_000, 0)
	s.Observe(blocks.HashOnlyHeader(20, at, "c", []byte{0x14}))
	s.Observe(blocks.DummyHeader(21))

	ms, err := s.HeaderTimeMs(20, []byte{0x14})
	require.NoError(t, err)
	require.Equal(t, at.UnixMilli(), ms)
	_, err = s.HeaderTimeMs(21, []byte{0x15})
	require.ErrorIs(t, err, types.ErrHeaderPending, "a dummy header carries no block time")
	require.Equal(t, int64(1), o.calls.Load())
}

func TestSourceEvictsTheOldestHeights(t *testing.T) {
	s := New(&countingOracle{}, WithCapacity(2))
	at := time.Unix(1_800_000_000, 0)
	for h := int64(1); h <= 3; h++ {
		s.Observe(blocks.HashOnlyHeader(h, at, "c", []byte{byte(h)}))
	}
	_, err := s.HeaderTimeMs(1, []byte{1})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	_, err = s.HeaderTimeMs(3, []byte{3})
	require.NoError(t, err)
}

type slowOracle struct {
	countingOracle
	release chan struct{}
}

func (o *slowOracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	select {
	case <-o.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return o.countingOracle.At(ctx, height)
}

func TestSourceDoesNotHoldTheCallerForASlowLookup(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	o := &slowOracle{
		countingOracle: countingOracle{headers: map[int64]*blocks.Header{7: blocks.HashOnlyHeader(7, at, "c", []byte{0x07})}},
		release:        make(chan struct{}),
	}
	s := New(o, WithMissWait(20*time.Millisecond))

	began := time.Now()
	_, err := s.HeaderTimeMs(7, []byte{0x07})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	require.Less(t, time.Since(began), time.Second, "a miss waits only the miss budget")

	close(o.release)
	require.Eventually(t, func() bool {
		ms, err := s.HeaderTimeMs(7, []byte{0x07})
		return err == nil && ms == at.UnixMilli()
	}, 2*time.Second, 10*time.Millisecond, "the lookup keeps running and fills the cache")
	require.Equal(t, int64(1), o.calls.Load(), "concurrent misses share one fetch")
}

type canonicalCountingOracle struct {
	*countingOracle
	canonical *blocks.Header
}

func (o canonicalCountingOracle) CanonicalHeaderForProof(context.Context, int64) (*blocks.Header, error) {
	o.calls.Add(1)
	return o.canonical, nil
}

func TestCanonicalProofLookupUsesAndPopulatesHeaderCache(t *testing.T) {
	at := time.Now().UTC()
	first := blocks.HashOnlyHeader(100, at, "gonka", []byte{100})
	second := blocks.HashOnlyHeader(101, at.Add(time.Second), "gonka", []byte{101})
	wrong := blocks.HashOnlyHeader(101, second.Time, "gonka", []byte{7})
	o := canonicalCountingOracle{&countingOracle{headers: map[int64]*blocks.Header{101: wrong}}, second}
	s := New(o)
	s.Observe(first)
	_, err := s.HeaderTimeMs(101, second.BlockHash)
	require.ErrorIs(t, err, types.ErrHeaderMismatch)
	for _, h := range []*blocks.Header{first, second, second} {
		got, err := s.CanonicalHeaderForProof(t.Context(), h.Height)
		require.NoError(t, err)
		require.Equal(t, h.BlockHash, got.BlockHash)
	}
	require.Equal(t, int64(2), o.calls.Load(), "one floor lookup and one independent canonical lookup")
}
