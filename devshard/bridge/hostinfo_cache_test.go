package bridge

import (
	"errors"
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
}

func (b *countingHostInfoBridge) GetHostInfo(string) (*HostInfo, error) {
	b.calls.Add(1)
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

func TestCachingHostInfo_DoesNotCacheOtherErrors(t *testing.T) {
	live := errors.New("dial timeout")
	inner := &countingHostInfoBridge{err: live}
	c := NewCachingHostInfo(inner)

	_, err := c.GetHostInfo("gonka1a")
	require.ErrorIs(t, err, live)
	_, err = c.GetHostInfo("gonka1a")
	require.Equal(t, int32(2), inner.calls.Load())
}
