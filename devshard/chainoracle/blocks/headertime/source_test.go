package headertime

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/observer"
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

func oracleSource(o blocks.BlockOracle, opts ...Option) *Source {
	all := make([]Option, 0, len(opts)+1)
	all = append(all, WithFiller(OracleFiller(o, OriginChain, false)))
	all = append(all, opts...)
	return New(all...)
}

func hdr(height int64, at time.Time, hash []byte) *blocks.Header {
	return blocks.HashOnlyHeader(height, at, "c", hash)
}

func TestSourceReturnsTheBlockTimeOnlyForTheMatchingHash(t *testing.T) {
	at := time.Unix(1_800_000_000, 123_000_000)
	o := &countingOracle{headers: map[int64]*blocks.Header{
		10: hdr(10, at, []byte{0x0a}),
	}}
	s := oracleSource(o)

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
	s := oracleSource(o)
	at := time.Unix(1_800_000_000, 0)
	s.Observe(hdr(20, at, []byte{0x14}), OriginComet, true)
	s.Observe(blocks.DummyHeader(21), OriginComet, true)

	ms, err := s.HeaderTimeMs(20, []byte{0x14})
	require.NoError(t, err)
	require.Equal(t, at.UnixMilli(), ms)
	_, err = s.HeaderTimeMs(21, []byte{0x15})
	require.ErrorIs(t, err, types.ErrHeaderPending, "a dummy header carries no block time")
	require.Equal(t, int64(1), o.calls.Load())
}

func TestSourceEvictsTheOldestHeights(t *testing.T) {
	s := oracleSource(&countingOracle{}, WithCapacity(2))
	at := time.Unix(1_800_000_000, 0)
	for h := int64(1); h <= 3; h++ {
		s.Observe(hdr(h, at, []byte{byte(h)}), OriginComet, false)
	}
	_, err := s.HeaderTimeMs(1, []byte{1})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	_, err = s.HeaderTimeMs(3, []byte{3})
	require.NoError(t, err)
}

type linkPendingOracle struct{ countingOracle }

func (o *linkPendingOracle) At(context.Context, int64) (*blocks.Header, error) {
	o.calls.Add(1)
	return nil, observer.ErrHeaderPendingLink
}

func TestSourcePendingLinkIsAWaitUntilTheVerifierPublishes(t *testing.T) {
	o := &linkPendingOracle{}
	s := New(WithGateway(), WithFiller(OracleFiller(o, OriginChain, true)), WithMissWait(50*time.Millisecond))
	at := time.Unix(1_800_000_000, 0)

	_, err := s.HeaderTimeMs(160, []byte{0xa0})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	_, ok := s.StoredAt(160)
	require.False(t, ok, "a buffered dapi header is not in the map before it links to the trusted header")

	s.Observe(hdr(160, at, []byte{0xa0}), OriginDAPI, true)
	ms, err := s.HeaderTimeMs(160, []byte{0xa0})
	require.NoError(t, err)
	require.Equal(t, at.UnixMilli(), ms)
	rec, ok := s.StoredAt(160)
	require.True(t, ok)
	require.Equal(t, OriginDAPI, rec.Origin)
	require.True(t, rec.Verified)
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
		countingOracle: countingOracle{headers: map[int64]*blocks.Header{7: hdr(7, at, []byte{0x07})}},
		release:        make(chan struct{}),
	}
	s := oracleSource(o, WithMissWait(20*time.Millisecond))

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

type fillerFunc func(context.Context, int64) (Fill, error)

func (f fillerFunc) Fill(ctx context.Context, height int64) (Fill, error) {
	return f(ctx, height)
}

func TestSourceMissWithoutFillerIsPending(t *testing.T) {
	s := New()
	_, err := s.HeaderTimeMs(4, []byte{0x04})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	_, ok := s.StoredAt(4)
	require.False(t, ok)
}

func TestSourceFillerIsSharedAcrossConcurrentLookups(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	release := make(chan struct{})
	var calls atomic.Int64
	s := New(WithMissWait(time.Second), WithFiller(fillerFunc(func(ctx context.Context, height int64) (Fill, error) {
		calls.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
			return Fill{}, ctx.Err()
		}
		return Fill{Header: hdr(height, at, []byte{0x09}), Origin: OriginChain, Verified: true}, nil
	})))

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.HeaderTimeMs(9, []byte{0x09})
			errCh <- err
		}()
	}
	require.Eventually(t, func() bool { return calls.Load() == 1 }, time.Second, 5*time.Millisecond)
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), calls.Load())

	ms, err := s.HeaderTimeMs(9, []byte{0x09})
	require.NoError(t, err)
	require.Equal(t, at.UnixMilli(), ms)
	require.Equal(t, int64(1), calls.Load(), "the next lookup reads the stored header")
	rec, ok := s.StoredAt(9)
	require.True(t, ok)
	require.Equal(t, OriginChain, rec.Origin)
	require.True(t, rec.Verified)
}

func TestSourceRejectsDummyZeroTimeAndEmptyHash(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	s := New()
	s.Observe(blocks.DummyHeader(1), OriginComet, true)
	s.Observe(hdr(2, time.Time{}, []byte{0x02}), OriginComet, true)
	s.Observe(&blocks.Header{Height: 3, Time: at}, OriginComet, true)
	s.Observe(nil, OriginComet, true)

	for _, h := range []uint64{1, 2, 3} {
		_, ok := s.StoredAt(h)
		require.False(t, ok, h)
	}

	var calls atomic.Int64
	s.SetFiller(fillerFunc(func(context.Context, int64) (Fill, error) {
		calls.Add(1)
		return Fill{Header: blocks.DummyHeader(4)}, nil
	}))
	_, err := s.HeaderTimeMs(4, []byte{0x04})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	require.Equal(t, int64(1), calls.Load())
	_, ok := s.StoredAt(4)
	require.False(t, ok)

	s.SetFiller(fillerFunc(func(context.Context, int64) (Fill, error) {
		calls.Add(1)
		return Fill{Header: hdr(5, time.Time{}, []byte{0x05})}, nil
	}))
	_, err = s.HeaderTimeMs(5, []byte{0x05})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	_, ok = s.StoredAt(5)
	require.False(t, ok)

	s.SetFiller(fillerFunc(func(context.Context, int64) (Fill, error) {
		calls.Add(1)
		return Fill{Header: &blocks.Header{Height: 6, Time: at}}, nil
	}))
	_, err = s.HeaderTimeMs(6, []byte{0x06})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	_, ok = s.StoredAt(6)
	require.False(t, ok)
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestSourceUpgradesOriginOnTheSameHash(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	s := New()
	s.Observe(hdr(8, at, []byte{0x08}), OriginDAPI, false)
	s.Observe(hdr(8, at.Add(time.Second), []byte{0x08}), OriginComet, true)

	rec, ok := s.StoredAt(8)
	require.True(t, ok)
	require.Equal(t, []byte{0x08}, rec.Hash)
	require.Equal(t, OriginComet, rec.Origin)
	require.True(t, rec.Verified)
	require.Equal(t, at.UnixMilli(), rec.TimeMs, "the first time stays")
	require.Equal(t, uint64(0), s.Conflicts())

	s.Observe(hdr(8, at, []byte{0x08}), OriginDAPI, false)
	rec, ok = s.StoredAt(8)
	require.True(t, ok)
	require.Equal(t, OriginComet, rec.Origin)
	require.True(t, rec.Verified, "a lower write does not clear verified")
}

func TestSourceGatewayFillerDoesNotRelabelDAPI(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	s := New(WithGateway(), WithFiller(fillerFunc(func(context.Context, int64) (Fill, error) {
		return Fill{Header: hdr(7, at, []byte{7}), Origin: OriginChain, Verified: true}, nil
	})))
	s.Observe(hdr(7, at, []byte{7}), OriginDAPI, true)
	s.Observe(hdr(7, at, []byte{7}), OriginChain, true)
	rec, ok := s.StoredAt(7)
	require.True(t, ok)
	require.Equal(t, OriginDAPI, rec.Origin)
	require.True(t, rec.Verified)
}

func TestSourceGatewayVerifiedDAPIBeatsChainOnSameHash(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	s := New(WithGateway())
	s.Observe(hdr(7, at, []byte{7}), OriginChain, true)
	s.Observe(hdr(7, at, []byte{7}), OriginDAPI, true)
	rec, ok := s.StoredAt(7)
	require.True(t, ok)
	require.Equal(t, OriginDAPI, rec.Origin)
	require.True(t, rec.Verified)
}

type fromDAPIOracle struct {
	countingOracle
	dapi map[int64]bool
}

func (o *fromDAPIOracle) FromDAPI(height int64) bool {
	return o.dapi[height]
}

func TestOracleFillerReportsDAPIOrigin(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	o := &fromDAPIOracle{
		countingOracle: countingOracle{headers: map[int64]*blocks.Header{
			7: hdr(7, at, []byte{7}),
			8: hdr(8, at, []byte{8}),
		}},
		dapi: map[int64]bool{7: true},
	}
	f := OracleFiller(o, OriginChain, true)
	got, err := f.Fill(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, OriginDAPI, got.Origin)
	require.True(t, got.Verified)

	got, err = f.Fill(context.Background(), 8)
	require.NoError(t, err)
	require.Equal(t, OriginChain, got.Origin)
}

func TestSourceGatewayStoresFillerDAPIOrigin(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	o := &fromDAPIOracle{
		countingOracle: countingOracle{headers: map[int64]*blocks.Header{
			9: hdr(9, at, []byte{9}),
		}},
		dapi: map[int64]bool{9: true},
	}
	s := New(WithGateway(), WithFiller(OracleFiller(o, OriginChain, true)), WithMissWait(time.Second))
	ms, err := s.HeaderTimeMs(9, []byte{9})
	require.NoError(t, err)
	require.Equal(t, at.UnixMilli(), ms)
	rec, ok := s.StoredAt(9)
	require.True(t, ok)
	require.Equal(t, OriginDAPI, rec.Origin)
	require.True(t, rec.Verified)
}

func TestSourceDevsharddHashConflict(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	logs := captureLogs(t)

	t.Run("comet replaces dapi", func(t *testing.T) {
		logs.Reset()
		s := New()
		before := testutil.ToFloat64(hashConflicts.WithLabelValues("replaced"))
		s.Observe(hdr(3, at, []byte{0x0a}), OriginDAPI, true)
		s.Observe(hdr(3, at.Add(time.Second), []byte{0x0b}), OriginComet, true)
		rec, ok := s.StoredAt(3)
		require.True(t, ok)
		require.Equal(t, []byte{0x0b}, rec.Hash)
		require.Equal(t, OriginComet, rec.Origin)
		require.Equal(t, at.Add(time.Second).UnixMilli(), rec.TimeMs)
		require.Equal(t, uint64(1), s.Conflicts())
		require.Equal(t, before+1, testutil.ToFloat64(hashConflicts.WithLabelValues("replaced")))
		require.Contains(t, logs.String(), `"level":"WARN"`)
		require.NotContains(t, logs.String(), `"level":"ERROR"`)
	})

	t.Run("chain replaces dapi", func(t *testing.T) {
		s := New()
		s.Observe(hdr(3, at, []byte{0x0a}), OriginDAPI, true)
		s.Observe(hdr(3, at, []byte{0x0c}), OriginChain, false)
		rec, ok := s.StoredAt(3)
		require.True(t, ok)
		require.Equal(t, []byte{0x0c}, rec.Hash)
		require.Equal(t, OriginChain, rec.Origin)
		require.False(t, rec.Verified)
		require.Equal(t, uint64(1), s.Conflicts())
	})

	t.Run("dapi does not replace comet", func(t *testing.T) {
		logs.Reset()
		s := New()
		before := testutil.ToFloat64(hashConflicts.WithLabelValues("kept"))
		s.Observe(hdr(3, at, []byte{0x0a}), OriginComet, true)
		s.Observe(hdr(3, at, []byte{0x0b}), OriginDAPI, true)
		rec, ok := s.StoredAt(3)
		require.True(t, ok)
		require.Equal(t, []byte{0x0a}, rec.Hash)
		require.Equal(t, OriginComet, rec.Origin)
		require.Equal(t, uint64(1), s.Conflicts())
		require.Equal(t, before+1, testutil.ToFloat64(hashConflicts.WithLabelValues("kept")))
		require.Contains(t, logs.String(), `"level":"WARN"`)
	})

	t.Run("chain does not replace comet", func(t *testing.T) {
		s := New()
		s.Observe(hdr(3, at, []byte{0x0a}), OriginComet, true)
		s.Observe(hdr(3, at, []byte{0x0c}), OriginChain, true)
		rec, ok := s.StoredAt(3)
		require.True(t, ok)
		require.Equal(t, []byte{0x0a}, rec.Hash)
		require.Equal(t, OriginComet, rec.Origin)
		require.Equal(t, uint64(1), s.Conflicts())
	})
}

func TestSourceGatewayKeepsTheFirstVerifiedHash(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	logs := captureLogs(t)
	s := New(WithGateway())
	before := testutil.ToFloat64(hashConflicts.WithLabelValues("kept"))
	s.Observe(hdr(3, at, []byte{0x0a}), OriginComet, true)
	s.Observe(hdr(3, at.Add(time.Second), []byte{0x0b}), OriginChain, true)
	rec, ok := s.StoredAt(3)
	require.True(t, ok)
	require.Equal(t, []byte{0x0a}, rec.Hash)
	require.Equal(t, OriginComet, rec.Origin)
	require.Equal(t, at.UnixMilli(), rec.TimeMs)
	require.Equal(t, uint64(1), s.Conflicts())
	require.Equal(t, before+1, testutil.ToFloat64(hashConflicts.WithLabelValues("kept")))
	require.Contains(t, logs.String(), `"level":"ERROR"`)
	require.Contains(t, logs.String(), "header time hash conflict")
}

func TestSourceDropsEntriesBelowTheFloor(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	floor := uint64(5)
	s := New(WithFloor(func() uint64 { return floor }))
	for h := int64(4); h <= 6; h++ {
		s.Observe(hdr(h, at, []byte{byte(h)}), OriginComet, false)
	}
	_, ok := s.StoredAt(4)
	require.False(t, ok)
	_, ok = s.StoredAt(5)
	require.True(t, ok)
	_, ok = s.StoredAt(6)
	require.True(t, ok)

	floor = 6
	s.SetFloorHeight(6)
	_, ok = s.StoredAt(5)
	require.False(t, ok)
	_, err := s.HeaderTimeMs(5, []byte{5})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	_, ok = s.StoredAt(6)
	require.True(t, ok)

	capped := New(WithFloor(func() uint64 { return 0 }), WithCapacity(2))
	for h := int64(1); h <= 3; h++ {
		capped.Observe(hdr(h, at, []byte{byte(h)}), OriginChain, false)
	}
	_, ok = capped.StoredAt(1)
	require.False(t, ok)
	_, ok = capped.StoredAt(3)
	require.True(t, ok)
}

func TestSourceHighWaterStopsBelowAGap(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	s := New()
	require.Equal(t, uint64(0), s.HighWater())
	for _, h := range []int64{1, 2, 4} {
		s.Observe(hdr(h, at, []byte{byte(h)}), OriginDAPI, true)
	}
	require.Equal(t, uint64(2), s.HighWater())
	s.Observe(hdr(3, at, []byte{3}), OriginDAPI, true)
	require.Equal(t, uint64(4), s.HighWater())

	floor := uint64(10)
	window := New(WithFloor(func() uint64 { return floor }))
	for _, h := range []int64{10, 11, 13} {
		window.Observe(hdr(h, at, []byte{byte(h)}), OriginComet, false)
	}
	require.Equal(t, uint64(11), window.HighWater())
	window.Observe(hdr(12, at, []byte{12}), OriginComet, false)
	require.Equal(t, uint64(13), window.HighWater())

	floor = 14
	window.Observe(hdr(15, at, []byte{15}), OriginComet, false)
	require.Equal(t, uint64(15), window.HighWater(), "HighWater follows the stored run when the floor height is missing")
}

func TestSourceHighWaterFollowsTheRunWhenFloorHeightIsMissing(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	floor := uint64(9390)
	s := New(WithFloor(func() uint64 { return floor }), WithBlockInterval(time.Second))
	s.Observe(hdr(10000, at, []byte{10}), OriginComet, true)
	require.Equal(t, uint64(10000), s.HighWater())
	_, ok := s.StoredAt(9390)
	require.False(t, ok)
}

func TestSourceHeaderTimeBelowFloorIsPassThrough(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	hash := []byte{0x05, 0x00}
	release := make(chan struct{})
	var calls atomic.Int64
	s := New(
		WithFloor(func() uint64 { return 1000 }),
		WithMissWait(time.Second),
		WithFiller(fillerFunc(func(ctx context.Context, height int64) (Fill, error) {
			calls.Add(1)
			select {
			case <-release:
			case <-ctx.Done():
				return Fill{}, ctx.Err()
			}
			return Fill{Header: hdr(height, at, hash), Origin: OriginChain}, nil
		})),
	)

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	msCh := make(chan int64, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ms, err := s.HeaderTimeMs(500, hash)
			errCh <- err
			msCh <- ms
		}()
	}
	require.Eventually(t, func() bool { return calls.Load() == 1 }, time.Second, 5*time.Millisecond)
	close(release)
	wg.Wait()
	close(errCh)
	close(msCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	for ms := range msCh {
		require.Equal(t, at.UnixMilli(), ms)
	}
	require.Equal(t, int64(1), calls.Load())
	_, ok := s.StoredAt(500)
	require.False(t, ok)
}

func TestSourceCatchUpSkipsStoredHeights(t *testing.T) {
	var mu sync.Mutex
	var got []int64
	base := time.Unix(1_800_000_000, 0)
	s := New(WithFiller(fillerFunc(func(_ context.Context, height int64) (Fill, error) {
		mu.Lock()
		got = append(got, height)
		mu.Unlock()
		return Fill{Header: hdr(height, base.Add(time.Duration(height)*time.Millisecond), []byte{byte(height)}), Origin: OriginChain}, nil
	})))
	s.Observe(hdr(10, base.Add(10*time.Millisecond), []byte{10}), OriginComet, true)
	s.Observe(hdr(12, base.Add(12*time.Millisecond), []byte{12}), OriginComet, true)
	s.Observe(hdr(15, base.Add(15*time.Millisecond), []byte{15}), OriginComet, true)

	require.Eventually(t, func() bool {
		_, ok11 := s.StoredAt(11)
		_, ok13 := s.StoredAt(13)
		_, ok14 := s.StoredAt(14)
		return ok11 && ok13 && ok14 && s.HighWater() == 15
	}, 2*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []int64{11, 13, 14}, got)
}

func TestSourceBlockInterval(t *testing.T) {
	const def = 5 * time.Second
	s := New(WithBlockInterval(def))
	require.Equal(t, def, s.BlockInterval())

	base := time.Unix(1_800_000_000, 0)
	for h := int64(1); h <= 19; h++ {
		s.Observe(hdr(h, base.Add(time.Duration(h)*time.Millisecond), []byte{byte(h)}), OriginComet, false)
	}
	require.Equal(t, def, s.BlockInterval(), "fewer than 20 entries keep the default")

	s.Observe(hdr(20, base.Add(20*time.Millisecond), []byte{20}), OriginComet, false)
	require.Equal(t, time.Millisecond, s.BlockInterval(), "20 consecutive entries use their gaps")

	varied := New(WithBlockInterval(def))
	ms := int64(0)
	for h := int64(1); h <= 30; h++ {
		varied.Observe(hdr(h, time.UnixMilli(ms), []byte{byte(h)}), OriginChain, false)
		if h < 10 {
			ms += 1
		} else {
			ms += 7
		}
	}
	require.Equal(t, 7*time.Millisecond, varied.BlockInterval(), "only the last 20 gaps")

	same := New(WithBlockInterval(def))
	at := time.Unix(1_800_000_000, 0)
	for h := int64(1); h <= 19; h++ {
		same.Observe(hdr(h, at, []byte{byte(h)}), OriginComet, false)
	}
	require.Equal(t, def, same.BlockInterval())
	same.Observe(hdr(20, at, []byte{20}), OriginComet, false)
	require.Equal(t, def, same.BlockInterval(), "a zero median keeps the default")

	spaced := New(WithBlockInterval(def))
	for h := int64(1); h <= 20; h++ {
		spaced.Observe(hdr(h, at.Add(time.Duration(h)*time.Millisecond), []byte{byte(h)}), OriginComet, false)
	}
	require.Equal(t, time.Millisecond, spaced.BlockInterval())
}

type holdFiller struct {
	started chan int64
	mu      sync.Mutex
	hold    map[int64]chan struct{}
	headers map[int64]*blocks.Header
}

func newHoldFiller(headers map[int64]*blocks.Header) *holdFiller {
	f := &holdFiller{
		started: make(chan int64, 16),
		hold:    make(map[int64]chan struct{}, len(headers)),
		headers: headers,
	}
	for h := range headers {
		f.hold[h] = make(chan struct{})
	}
	return f
}

func (f *holdFiller) Fill(ctx context.Context, height int64) (Fill, error) {
	f.started <- height
	f.mu.Lock()
	ch := f.hold[height]
	h := f.headers[height]
	f.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
			return Fill{}, ctx.Err()
		}
	}
	if h == nil {
		return Fill{}, blocks.ErrHeaderNotFound
	}
	return Fill{Header: h, Origin: OriginChain}, nil
}

func (f *holdFiller) release(height int64) {
	f.mu.Lock()
	ch := f.hold[height]
	f.hold[height] = nil
	f.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

func (f *holdFiller) releaseAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for h, ch := range f.hold {
		if ch != nil {
			close(ch)
			f.hold[h] = nil
		}
	}
}

func waitFill(t *testing.T, started <-chan int64) int64 {
	t.Helper()
	select {
	case h := <-started:
		return h
	case <-time.After(2 * time.Second):
		t.Fatal("filler did not start")
		return 0
	}
}

func TestSourceCatchUpFillsTheGapBelowTheTip(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	headers := map[int64]*blocks.Header{}
	for h := int64(5); h <= 9; h++ {
		headers[h] = hdr(h, base.Add(time.Duration(h)*time.Millisecond), []byte{byte(h)})
	}
	gate := newHoldFiller(headers)
	t.Cleanup(gate.releaseAll)

	s := New(WithMissWait(5 * time.Second))
	for h := int64(10); h <= 12; h++ {
		s.Observe(hdr(h, base.Add(time.Duration(h)*time.Millisecond), []byte{byte(h)}), OriginComet, true)
	}
	require.Equal(t, uint64(12), s.HighWater())
	s.SetFiller(gate)

	errc := make(chan error, 1)
	go func() {
		_, err := s.HeaderTimeMs(8, []byte{8})
		errc <- err
	}()
	require.Equal(t, int64(8), waitFill(t, gate.started))
	gate.release(8)
	require.NoError(t, <-errc)
	require.Equal(t, int64(9), waitFill(t, gate.started), "the gap above 8 is the background job")

	go func() {
		_, err := s.HeaderTimeMs(5, []byte{5})
		errc <- err
	}()
	require.Equal(t, int64(5), waitFill(t, gate.started), "the exact height is fetched immediately")
	gate.release(5)
	require.NoError(t, <-errc)

	select {
	case h := <-gate.started:
		t.Fatalf("height %d started before the first catch-up job finished", h)
	default:
	}

	gate.release(9)
	require.Equal(t, int64(6), waitFill(t, gate.started))
	gate.release(6)
	require.Equal(t, int64(7), waitFill(t, gate.started))
	gate.release(7)

	require.Eventually(t, func() bool {
		if s.HighWater() != 12 {
			return false
		}
		for h := uint64(5); h <= 12; h++ {
			if _, ok := s.StoredAt(h); !ok {
				return false
			}
		}
		return true
	}, 2*time.Second, 10*time.Millisecond)
}

func TestSourceCatchUpFillsAForwardGap(t *testing.T) {
	var mu sync.Mutex
	var got []int64
	base := time.Unix(1_800_000_000, 0)
	s := New(WithFiller(fillerFunc(func(_ context.Context, height int64) (Fill, error) {
		mu.Lock()
		got = append(got, height)
		mu.Unlock()
		return Fill{Header: hdr(height, base.Add(time.Duration(height)*time.Millisecond), []byte{byte(height)}), Origin: OriginChain}, nil
	})))
	s.Observe(hdr(10, base.Add(10*time.Millisecond), []byte{10}), OriginComet, true)
	s.Observe(hdr(13, base.Add(13*time.Millisecond), []byte{13}), OriginComet, true)

	require.Eventually(t, func() bool {
		_, ok11 := s.StoredAt(11)
		_, ok12 := s.StoredAt(12)
		return ok11 && ok12 && s.HighWater() == 13
	}, 2*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []int64{11, 12}, got)
}

func TestSourceCatchUpAppliesABatch(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	headers := map[int64]*blocks.Header{}
	for _, h := range []int64{6, 7, 8, 9, 13, 14} {
		headers[h] = hdr(h, base.Add(time.Duration(h)*time.Millisecond), []byte{byte(h)})
	}
	gate := newHoldFiller(headers)
	t.Cleanup(gate.releaseAll)

	s := New(WithMissWait(5 * time.Second))
	for h := int64(10); h <= 12; h++ {
		s.Observe(hdr(h, base.Add(time.Duration(h)*time.Millisecond), []byte{byte(h)}), OriginComet, true)
	}
	s.SetFiller(gate)

	errc := make(chan error, 1)
	go func() {
		_, err := s.HeaderTimeMs(6, []byte{6})
		errc <- err
	}()
	require.Equal(t, int64(6), waitFill(t, gate.started))
	gate.release(6)
	require.NoError(t, <-errc)
	require.Equal(t, int64(7), waitFill(t, gate.started))

	s.Observe(hdr(15, base.Add(15*time.Millisecond), []byte{15}), OriginComet, true)
	select {
	case h := <-gate.started:
		t.Fatalf("height %d started while 7..9 were still in the cache", h)
	default:
	}
	_, ok := s.StoredAt(7)
	require.False(t, ok)

	gate.release(7)
	require.Equal(t, int64(8), waitFill(t, gate.started))
	_, ok = s.StoredAt(7)
	require.False(t, ok, "7 stays in the job cache until the batch is applied")

	gate.release(8)
	require.Equal(t, int64(9), waitFill(t, gate.started))
	_, ok = s.StoredAt(8)
	require.False(t, ok)

	gate.release(9)
	require.Equal(t, int64(13), waitFill(t, gate.started))
	for h := uint64(6); h <= 12; h++ {
		_, ok := s.StoredAt(h)
		require.Truef(t, ok, "height %d", h)
	}
	_, ok = s.StoredAt(13)
	require.False(t, ok)
	require.Equal(t, uint64(12), s.HighWater())
	s.mu.Lock()
	baseHeight := s.runBase
	s.mu.Unlock()
	require.Equal(t, uint64(6), baseHeight)

	gate.release(13)
	require.Equal(t, int64(14), waitFill(t, gate.started))
	_, ok = s.StoredAt(13)
	require.False(t, ok, "13 stays in the forward batch until 14 is fetched")
	gate.release(14)

	require.Eventually(t, func() bool {
		_, ok13 := s.StoredAt(13)
		_, ok14 := s.StoredAt(14)
		s.mu.Lock()
		base := s.runBase
		s.mu.Unlock()
		return ok13 && ok14 && s.HighWater() == 15 && base == 6
	}, 2*time.Second, 10*time.Millisecond)
}

// missGate holds each fill until release. fails is how many times that height
// returns not found before a later fill returns the header.
type missGate struct {
	started chan int64
	mu      sync.Mutex
	hold    map[int64]chan struct{}
	headers map[int64]*blocks.Header
	fails   map[int64]int
	calls   map[int64]int
}

func newMissGate(headers map[int64]*blocks.Header, fails map[int64]int) *missGate {
	g := &missGate{
		started: make(chan int64, 32),
		hold:    make(map[int64]chan struct{}, len(headers)+len(fails)),
		headers: headers,
		fails:   fails,
		calls:   map[int64]int{},
	}
	for h := range headers {
		g.hold[h] = make(chan struct{})
	}
	for h := range fails {
		if _, ok := g.hold[h]; !ok {
			g.hold[h] = make(chan struct{})
		}
	}
	return g
}

func (g *missGate) Fill(ctx context.Context, height int64) (Fill, error) {
	g.mu.Lock()
	g.calls[height]++
	ch := g.hold[height]
	h := g.headers[height]
	fail := g.fails[height] > 0
	if fail {
		g.fails[height]--
	}
	g.mu.Unlock()
	g.started <- height
	if ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
			return Fill{}, ctx.Err()
		}
	}
	if fail || h == nil {
		return Fill{}, blocks.ErrHeaderNotFound
	}
	return Fill{Header: h, Origin: OriginChain}, nil
}

func (g *missGate) release(height int64) {
	g.mu.Lock()
	ch := g.hold[height]
	g.hold[height] = nil
	g.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

func (g *missGate) releaseAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for h, ch := range g.hold {
		if ch != nil {
			close(ch)
			g.hold[h] = nil
		}
	}
}

func (g *missGate) callCount(height int64) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[height]
}

func TestSourceCatchUpRetriesAMiss(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	hdrs := func(heights ...int64) map[int64]*blocks.Header {
		out := make(map[int64]*blocks.Header, len(heights))
		for _, h := range heights {
			out[h] = hdr(h, base.Add(time.Duration(h)*time.Millisecond), []byte{byte(h)})
		}
		return out
	}
	observeRun := func(s *Source) {
		for h := int64(10); h <= 12; h++ {
			s.Observe(hdr(h, base.Add(time.Duration(h)*time.Millisecond), []byte{byte(h)}), OriginComet, true)
		}
	}
	fetch := func(s *Source, height int64) <-chan error {
		errc := make(chan error, 1)
		go func() {
			_, err := s.HeaderTimeMs(uint64(height), []byte{byte(height)})
			errc <- err
		}()
		return errc
	}

	t.Run("lower ranges cover the hole and the retry is dropped", func(t *testing.T) {
		gate := newMissGate(hdrs(3, 4, 5, 6, 7, 8, 9), map[int64]int{9: 1})
		t.Cleanup(gate.releaseAll)
		s := New(WithMissWait(5 * time.Second))
		observeRun(s)
		s.SetFiller(gate)

		errc := fetch(s, 8)
		require.Equal(t, int64(8), waitFill(t, gate.started))
		gate.release(8)
		require.NoError(t, <-errc)
		require.Equal(t, int64(9), waitFill(t, gate.started))

		errc = fetch(s, 5)
		require.Equal(t, int64(5), waitFill(t, gate.started))
		gate.release(5)
		require.NoError(t, <-errc)
		errc = fetch(s, 3)
		require.Equal(t, int64(3), waitFill(t, gate.started))
		gate.release(3)
		require.NoError(t, <-errc)

		s.mu.Lock()
		floor := s.catchFloor
		s.mu.Unlock()
		require.Equal(t, uint64(10), floor, "the catch floor stays put while 9 is in flight")

		gate.release(9)
		require.Equal(t, int64(9), waitFill(t, gate.started), "the next job fills the high missing height first")
		gate.release(9)
		require.Equal(t, int64(6), waitFill(t, gate.started))
		gate.release(6)
		require.Equal(t, int64(7), waitFill(t, gate.started))
		gate.release(7)
		require.Equal(t, int64(4), waitFill(t, gate.started))
		gate.release(4)

		require.Eventually(t, func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.catchFloor == 3 && s.runBase == 3 && !s.catchOn && len(s.catchQ) == 0
		}, 2*time.Second, 10*time.Millisecond)
		for h := uint64(3); h <= 12; h++ {
			_, ok := s.StoredAt(h)
			require.Truef(t, ok, "height %d", h)
		}
		require.Equal(t, uint64(12), s.HighWater())
		require.Equal(t, 2, gate.callCount(9))
		select {
		case h := <-gate.started:
			t.Fatalf("height %d started after the retry was dropped", h)
		default:
		}
	})

	t.Run("one miss then the header is stored", func(t *testing.T) {
		gate := newMissGate(hdrs(8, 9), map[int64]int{9: 1})
		t.Cleanup(gate.releaseAll)
		s := New(WithMissWait(5 * time.Second))
		observeRun(s)
		s.SetFiller(gate)

		errc := fetch(s, 8)
		require.Equal(t, int64(8), waitFill(t, gate.started))
		gate.release(8)
		require.NoError(t, <-errc)
		require.Equal(t, int64(9), waitFill(t, gate.started))
		gate.release(9)
		require.Equal(t, int64(9), waitFill(t, gate.started))
		gate.release(9)

		require.Eventually(t, func() bool {
			_, ok := s.StoredAt(9)
			s.mu.Lock()
			floor := s.catchFloor
			base := s.runBase
			s.mu.Unlock()
			return ok && floor == 8 && base == 8 && s.HighWater() == 12
		}, 2*time.Second, 10*time.Millisecond)
		require.Equal(t, 2, gate.callCount(9))
	})

	t.Run("give up after the retry limit", func(t *testing.T) {
		gate := newMissGate(hdrs(8), map[int64]int{9: catchMissLimit + 5})
		t.Cleanup(gate.releaseAll)
		s := New(WithMissWait(5 * time.Second))
		observeRun(s)
		s.SetFiller(gate)

		errc := fetch(s, 8)
		require.Equal(t, int64(8), waitFill(t, gate.started))
		gate.release(8)
		require.NoError(t, <-errc)
		for i := 0; i < catchMissLimit; i++ {
			require.Equal(t, int64(9), waitFill(t, gate.started))
			gate.release(9)
		}
		require.Eventually(t, func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			_, stored := s.entries[9]
			return !stored && s.catchFloor == 10 && s.runBase == 10 && !s.catchOn && len(s.catchQ) == 0
		}, 2*time.Second, 10*time.Millisecond)
		require.Equal(t, catchMissLimit, gate.callCount(9))
		_, ok := s.StoredAt(10)
		require.True(t, ok)
		_, ok = s.StoredAt(12)
		require.True(t, ok)
		require.Equal(t, uint64(12), s.HighWater())

		_, err := s.HeaderTimeMs(9, []byte{9})
		require.Error(t, err)
		require.Equal(t, catchMissLimit+1, gate.callCount(9))
		_, ok = s.StoredAt(9)
		require.False(t, ok)
	})
}

func TestSourceHighWaterStaysOnTheLiveRun(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	filler := fillerFunc(func(_ context.Context, height int64) (Fill, error) {
		if height != 2 {
			return Fill{}, blocks.ErrHeaderNotFound
		}
		return Fill{Header: hdr(2, at, []byte{2}), Origin: OriginChain}, nil
	})
	observeRun := func(s *Source) {
		for h := int64(10); h <= 12; h++ {
			s.Observe(hdr(h, at, []byte{byte(h)}), OriginComet, true)
		}
	}

	s := New(WithFiller(filler))
	observeRun(s)
	ms, err := s.HeaderTimeMs(2, []byte{2})
	require.NoError(t, err)
	require.Equal(t, at.UnixMilli(), ms)
	require.Equal(t, uint64(12), s.HighWater())
	_, ok := s.StoredAt(2)
	require.True(t, ok)

	capped := New(WithCapacity(3), WithFiller(filler))
	observeRun(capped)
	ms, err = capped.HeaderTimeMs(2, []byte{2})
	require.NoError(t, err)
	require.Equal(t, at.UnixMilli(), ms)
	require.Equal(t, uint64(12), capped.HighWater())
	_, ok = capped.StoredAt(10)
	require.True(t, ok)
	_, ok = capped.StoredAt(2)
	require.False(t, ok)
}

func TestSourceFloorCallbackDoesNotDeadlock(t *testing.T) {
	var s *Source
	var calls atomic.Int32
	s = New(WithFloor(func() uint64 {
		s.HighWater()
		s.BlockInterval()
		calls.Add(1)
		return 5
	}))
	at := time.Unix(1_800_000_000, 0)
	done := make(chan struct{}, 2)
	go func() {
		s.Observe(hdr(6, at, []byte{6}), OriginComet, false)
		done <- struct{}{}
	}()
	go func() {
		s.HighWater()
		done <- struct{}{}
	}()
	timeout := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-timeout:
			t.Fatal("floor callback deadlocked")
		}
	}
	require.Positive(t, calls.Load())

	type storedResult struct {
		height int64
		ok     bool
	}
	stored := make(chan storedResult, 2)
	go func() {
		_, ok := s.StoredAt(6)
		stored <- storedResult{6, ok}
	}()
	go func() {
		s.Observe(hdr(4, at, []byte{4}), OriginComet, false)
		_, ok := s.StoredAt(4)
		stored <- storedResult{4, ok}
	}()
	timeout = time.After(2 * time.Second)
	found := map[int64]bool{}
	for i := 0; i < 2; i++ {
		select {
		case r := <-stored:
			found[r.height] = r.ok
		case <-timeout:
			t.Fatal("floor callback deadlocked")
		}
	}
	require.True(t, found[6])
	require.False(t, found[4], "a header below the floor is dropped")
}

func fillRun(n int) *Source {
	at := time.Unix(1_800_000_000, 0)
	s := New()
	for h := 1; h <= n; h++ {
		s.Observe(hdr(int64(h), at.Add(time.Duration(h)*time.Millisecond), []byte{byte(h), byte(h >> 8)}), OriginComet, true)
	}
	return s
}

func TestSourceHeaderTimeMsAllocsDoNotGrowWithN(t *testing.T) {
	allocs := func(n int) float64 {
		s := fillRun(n)
		h := uint64(n / 2)
		hash := []byte{byte(h), byte(h >> 8)}
		return testing.AllocsPerRun(200, func() {
			_, _ = s.HeaderTimeMs(h, hash)
		})
	}
	small := allocs(1_000)
	large := allocs(20_000)
	require.InDelta(t, small, large, 0.5)
	require.Less(t, large, 1.0)
}

func TestSourceObserveAllocsDoNotGrowWithN(t *testing.T) {
	allocs := func(n int) float64 {
		s := fillRun(n)
		at := time.Unix(1_800_000_000, 0)
		next := int64(n + 1)
		return testing.AllocsPerRun(100, func() {
			s.Observe(hdr(next, at.Add(time.Duration(next)*time.Millisecond), []byte{byte(next), byte(next >> 8)}), OriginComet, true)
			next++
		})
	}
	small := allocs(1_000)
	large := allocs(20_000)
	require.InDelta(t, small, large, 2)
}

type blockingBatch struct {
	mu    sync.Mutex
	calls int
	hdrs  map[int64]*blocks.Header
}

func (b *blockingBatch) Fill(context.Context, int64) (Fill, error) {
	return Fill{}, blocks.ErrHeaderNotFound
}

func (b *blockingBatch) FillBatch(ctx context.Context, from, to int64) ([]Fill, error) {
	b.mu.Lock()
	b.calls++
	n := b.calls
	b.mu.Unlock()
	if n == 1 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var out []Fill
	for h := from; h <= to; h++ {
		if hdr, ok := b.hdrs[h]; ok {
			out = append(out, Fill{Header: hdr, Origin: OriginChain, Verified: true})
		}
	}
	return out, nil
}

func TestSourceCatchUpBatchTimeoutLetsTheNextJobRun(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	batch := &blockingBatch{hdrs: map[int64]*blocks.Header{
		11: hdr(11, base.Add(11*time.Millisecond), []byte{11}),
		12: hdr(12, base.Add(12*time.Millisecond), []byte{12}),
	}}
	s := New(WithFetchTimeout(20*time.Millisecond), WithFiller(batch))
	s.Observe(hdr(10, base.Add(10*time.Millisecond), []byte{10}), OriginComet, true)
	s.Observe(hdr(13, base.Add(13*time.Millisecond), []byte{13}), OriginComet, true)

	require.Eventually(t, func() bool {
		_, ok11 := s.StoredAt(11)
		_, ok12 := s.StoredAt(12)
		s.mu.Lock()
		idle := !s.catchOn && len(s.catchQ) == 0
		s.mu.Unlock()
		return ok11 && ok12 && idle && s.HighWater() == 13
	}, 2*time.Second, 10*time.Millisecond, "the hung batch ends at the deadline and the retry stores the hole")
	batch.mu.Lock()
	calls := batch.calls
	batch.mu.Unlock()
	require.GreaterOrEqual(t, calls, 2)
}
