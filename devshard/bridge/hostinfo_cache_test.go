package bridge

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type countingHostInfoBridge struct {
	MainnetBridge
	calls atomic.Int32
	info  *HostInfo
	err   error
	gate  chan struct{} // when non-nil, each call blocks until it is closed
}

func (b *countingHostInfoBridge) GetHostInfo(string) (*HostInfo, error) {
	b.calls.Add(1)
	if b.gate != nil {
		<-b.gate
	}
	if b.err != nil {
		return nil, b.err
	}
	if b.info == nil {
		return &HostInfo{Address: "gonka1host", URL: "http://peer:8080"}, nil
	}
	cp := *b.info
	return &cp, nil
}

func TestCachingHostInfo_HitWithinTTL(t *testing.T) {
	inner := &countingHostInfoBridge{info: &HostInfo{Address: "gonka1a", URL: "http://a"}}
	c := NewCachingHostInfo(inner)

	first, err := c.GetHostInfo("gonka1a")
	require.NoError(t, err)
	require.Equal(t, "http://a", first.URL)

	second, err := c.GetHostInfo("gonka1a")
	require.NoError(t, err)
	require.Equal(t, "http://a", second.URL)
	require.Equal(t, int32(1), inner.calls.Load())

	first.URL = "mutated"
	third, err := c.GetHostInfo("gonka1a")
	require.NoError(t, err)
	require.Equal(t, "http://a", third.URL)
}

func TestCachingHostInfo_Expires(t *testing.T) {
	inner := &countingHostInfoBridge{info: &HostInfo{Address: "gonka1a", URL: "http://a"}}
	c := NewCachingHostInfo(inner)
	now := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return now }

	_, err := c.GetHostInfo("gonka1a")
	require.NoError(t, err)
	now = now.Add(DefaultHostInfoCacheTTL + time.Second)
	_, err = c.GetHostInfo("gonka1a")
	require.NoError(t, err)
	require.Equal(t, int32(2), inner.calls.Load())
}

func TestCachingHostInfo_CachesParticipantNotFound(t *testing.T) {
	inner := &countingHostInfoBridge{err: ErrParticipantNotFound}
	c := NewCachingHostInfo(inner)

	_, err := c.GetHostInfo("gonka1missing")
	require.ErrorIs(t, err, ErrParticipantNotFound)
	_, err = c.GetHostInfo("gonka1missing")
	require.ErrorIs(t, err, ErrParticipantNotFound)
	require.Equal(t, int32(1), inner.calls.Load())
}

func TestCachingHostInfo_DoesNotCacheTransient(t *testing.T) {
	inner := &countingHostInfoBridge{err: ErrChainUnavailable}
	c := NewCachingHostInfo(inner)

	_, err := c.GetHostInfo("gonka1a")
	require.ErrorIs(t, err, ErrChainUnavailable)
	_, err = c.GetHostInfo("gonka1a")
	require.ErrorIs(t, err, ErrChainUnavailable)
	require.Equal(t, int32(2), inner.calls.Load())
}

// Tests that concurrent misses for one address share a single chain query.
//  1. Block the chain query and look the address up from eight goroutines.
//  2. Release the query.
//  3. Expect one query and every caller to get its own copy of the URL.
func TestCachingHostInfo_ConcurrentMissesShareOneQuery(t *testing.T) {
	inner := &countingHostInfoBridge{info: &HostInfo{Address: "gonka1a", URL: "http://a"}, gate: make(chan struct{})}
	c := NewCachingHostInfo(inner)

	const callers = 8
	got := make(chan *HostInfo, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := c.GetHostInfo("gonka1a")
			got <- info
			errs <- err
		}()
	}
	require.Eventually(t, func() bool { return inner.calls.Load() == 1 }, time.Second, time.Millisecond)
	close(inner.gate)
	wg.Wait()
	close(got)
	close(errs)

	require.Equal(t, int32(1), inner.calls.Load())
	for err := range errs {
		require.NoError(t, err)
	}
	seen := make(map[*HostInfo]bool, callers)
	for info := range got {
		require.Equal(t, "http://a", info.URL)
		require.False(t, seen[info], "callers must not share one HostInfo")
		seen[info] = true
	}
}

func TestCachingHostInfo_DoesNotCacheOtherErrors(t *testing.T) {
	live := errors.New("dial timeout")
	inner := &countingHostInfoBridge{err: live}
	c := NewCachingHostInfo(inner)

	_, err := c.GetHostInfo("gonka1a")
	require.ErrorIs(t, err, live)
	_, err = c.GetHostInfo("gonka1a")
	require.Equal(t, int32(2), inner.calls.Load())
}
