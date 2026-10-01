package rpcserver

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionStoreMemoryQueriesStayZero(t *testing.T) {
	h := newTestAuth(PeerAuthConfig{})
	require.True(t, h.SessionsReady())
	_, ok := h.LookupToken([]byte("0123456789abcdef"))
	require.False(t, ok)
	require.Equal(t, int64(0), h.sessionStoreQueries())
}

func TestSinceReadsAcrossSeqGaps(t *testing.T) {
	require.Contains(t, sinceSessionsSQL, "seq >")
	require.Contains(t, sinceSessionsSQL, "updated_at >")
	require.Contains(t, sinceSessionsSQL, "host_address = $1")
	require.Contains(t, sinceSessionsSQL, "version = $2")
}

func TestWatermarkHoldsAGapUntilTheMissingSeqArrives(t *testing.T) {
	s := &SharedSessions{}
	now := time.Now()
	mark, advanced := s.noteAppliedSeqs([]int64{10}, now)
	require.True(t, advanced)
	require.Equal(t, int64(10), mark)

	mark, advanced = s.noteAppliedSeqs([]int64{12}, now)
	require.False(t, advanced)
	require.Equal(t, int64(10), s.applied.Load())
	require.Equal(t, int64(10), mark)

	mark, advanced = s.noteAppliedSeqs([]int64{11}, now)
	require.True(t, advanced)
	require.Equal(t, int64(12), mark)
}

func TestWatermarkExpiresAnAbandonedHole(t *testing.T) {
	s := &SharedSessions{}
	now := time.Now()
	_, advanced := s.noteAppliedSeqs([]int64{10, 12}, now)
	require.True(t, advanced)
	require.Equal(t, int64(10), s.applied.Load())

	mark, advanced := s.noteAppliedSeqs(nil, now.Add(sharedSeqHoleGrace))
	require.True(t, advanced)
	require.Equal(t, int64(12), mark)
}

func TestApplySharedHigherSeqWinsAndStopsWatch(t *testing.T) {
	h := newTestAuth(PeerAuthConfig{})
	h.shared = &SharedSessions{host: testHostAddress, version: "v2"}
	h.byHash = map[string]*peerSession{}
	raw := []byte("0123456789abcdef")
	hash := tokenHashHex(raw)
	stop := make(chan struct{})
	sess := &peerSession{
		peer:        "peer-a",
		expires:     time.Now().Add(time.Minute),
		rawKey:      string(raw),
		tokenHash:   hash,
		current:     true,
		cancelWatch: stop,
		watching:    true,
	}
	h.sessions[string(raw)] = sess
	h.byPeer["peer-a"] = string(raw)
	h.byHash[hash] = sess

	later := time.Now().Add(5 * time.Second)
	h.applySharedBatch([]sessionRow{
		{
			HashHex: hash, Host: testHostAddress, Version: "v2", Peer: "peer-a",
			State: sessionStateReplaced, AdmitUntil: later, Seq: 5,
		},
		{
			HashHex: hash, Host: testHostAddress, Version: "v2", Peer: "peer-a",
			State: sessionStateLive, AdmitUntil: later, Seq: 4,
		},
	}, nil)

	select {
	case <-stop:
	default:
		t.Fatal("higher-seq replace must stop Watch")
	}
	require.False(t, h.byHash[hash].current)
	peer, ok := h.LookupToken(raw)
	require.True(t, ok, "grace token still admits")
	require.Equal(t, "peer-a", peer)
}

func TestApplySharedDuplicateAndVersionFilter(t *testing.T) {
	h := newTestAuth(PeerAuthConfig{})
	h.shared = &SharedSessions{host: testHostAddress, version: "v2"}
	h.byHash = map[string]*peerSession{}
	later := time.Now().Add(time.Minute)
	row := sessionRow{
		HashHex: "aa", Host: testHostAddress, Version: "v2", Peer: "peer-a",
		State: sessionStateLive, AdmitUntil: later, Seq: 1,
	}
	h.applySharedBatch([]sessionRow{
		row,
		row,
		{
			HashHex: "bb", Host: testHostAddress, Version: "v9", Peer: "other",
			State: sessionStateLive, AdmitUntil: later, Seq: 2,
		},
	}, nil)
	require.Len(t, h.byHash, 1)
	_, ok := h.byHash["aa"]
	require.True(t, ok)
}

func TestApplyAfterCloseDoesNotResurrect(t *testing.T) {
	h := newTestAuth(PeerAuthConfig{})
	h.shared = &SharedSessions{host: testHostAddress, version: "v2"}
	h.byHash = map[string]*peerSession{}
	h.closed.Store(true)
	h.applySharedBatch([]sessionRow{{
		HashHex: "aa", Host: testHostAddress, Version: "v2", Peer: "peer-a",
		State: sessionStateLive, AdmitUntil: time.Now().Add(time.Minute), Seq: 1,
	}}, nil)
	require.Empty(t, h.byHash)
}

func TestSharedCacheCloseDoesNotRaceTheSweep(t *testing.T) {
	h := newTestAuth(PeerAuthConfig{})
	h.byHash = map[string]*peerSession{
		"aa": {peer: "peer-a", current: true, expires: time.Now().Add(time.Hour)},
	}
	require.True(t, h.sharedPeerLive("peer-a"))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			h.sweepSharedCache(time.Now())
			h.sharedPeerLive("peer-a")
		}
	}()
	go func() {
		defer wg.Done()
		h.Close()
	}()
	wg.Wait()
	require.False(t, h.sharedPeerLive("peer-a"))
	h.sweepSharedCache(time.Now())
}

func TestListenOutageClearsReadinessPastTheLimit(t *testing.T) {
	s := OpenSharedSessions(nil, SharedConfig{HostAddress: "h", Version: "v"})
	s.ready.Store(true)
	now := time.Unix(1_700_000_000, 0)
	require.True(t, s.ListenHealthy())
	require.Zero(t, s.AppliedLag(now))

	s.noteListenDown(now)
	require.False(t, s.ListenHealthy())
	require.True(t, s.Ready(), "a LISTEN break inside the limit keeps the child in rotation")
	require.False(t, s.degradeIfListenDown(now.Add(sharedListenDownLimit-time.Millisecond)))
	require.True(t, s.Ready())
	require.Equal(t, sharedListenDownLimit-time.Millisecond, s.AppliedLag(now.Add(sharedListenDownLimit-time.Millisecond)))

	require.True(t, s.degradeIfListenDown(now.Add(sharedListenDownLimit)))
	require.False(t, s.Ready())
	require.GreaterOrEqual(t, s.AppliedLag(now.Add(sharedListenDownLimit)), sharedListenDownLimit)
	require.True(t, s.degradeIfListenDown(now.Add(sharedListenDownLimit+time.Second)))
	require.False(t, s.Ready())

	s.clearListenDown()
	s.ready.Store(true)
	require.True(t, s.ListenHealthy())
	require.Zero(t, s.AppliedLag(now.Add(time.Hour)))
	require.False(t, s.degradeIfListenDown(now.Add(time.Hour)))
}
