package rpcserver

import (
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
	require.Contains(t, sinceSessionsSQL, "host_address = $1")
	require.Contains(t, sinceSessionsSQL, "version = $2")
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
