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
