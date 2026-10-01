package rpcserver

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// sessionStore is the peer-session record. A nil shared store means the
// handler maps are the record: sessions, byPeer, prevByPeer, and retired.
// That memory implementation is what SQLite, a single host, and the unit
// tests use. SharedSessions is the Postgres record used when several
// children serve one host version. Lookup of an unknown token reads the
// in-memory maps only; it does not query Postgres.
type sessionStore interface {
	Ready() bool
	SQLQueries() int64
	Close()
}

// SharedSessions is the Postgres implementation. The memory implementation
// is the handler maps, used when shared is nil.
var _ sessionStore = (*SharedSessions)(nil)

const (
	sessionStateLive        = "live"
	sessionStateReplaced    = "replaced"
	sessionStateInvalidated = "invalidated"
	sessionStateEvicted     = "evicted"

	sessionNotifyChannel = "devshard_peer_rpc_sessions"

	sharedHeartbeat    = time.Second
	sharedHeartbeatTTL = 3 * time.Second
	sharedBarrierWait  = time.Second
	sharedBarrierPoll  = 20 * time.Millisecond
	sharedPoll         = 5 * time.Second
	// sharedApplyLookback re-reads a row that commits after a higher seq was
	// already applied. It has to outlast sharedPoll so a missed NOTIFY still
	// lands on the next catch-up.
	sharedApplyLookback = 15 * time.Second
	// sharedSeqHoleGrace is how long a missing seq holds the published
	// watermark. A transaction open longer than this is still applied when
	// it commits, but the barrier may already have moved on.
	sharedSeqHoleGrace = 2 * time.Second
	// lastSeenThrottle is the minimum gap between last_seen writes for one
	// token. Admit and Watch heartbeats both try; the row itself also refuses
	// a newer write inside this gap.
	lastSeenThrottle = 30 * time.Second
	// lastSeenKeep is how recently a last_seen must have moved for that live
	// row to be kept at the session cap. It outlasts one missed heartbeat
	// plus the throttle, so a Watch that is still up is not the victim.
	lastSeenKeep = 2 * time.Minute
)

// sinceSessionsSQL is the catch-up read. seq > $3 is the highest seq already
// seen. updated_at > $4 is the lookback: nextval runs before commit, so a
// lower seq can become visible after a higher one. Applying a row twice is
// safe; a lower seq than the one already stored for that token is ignored.
const sinceSessionsSQL = `
SELECT token_hash, peer, attached_unix, expires_at, grace_until, state, seq, updated_at
FROM devshard_peer_rpc_sessions
WHERE host_address = $1 AND version = $2
  AND (seq > $3 OR updated_at > $4)
ORDER BY seq`

// sessionRow is one replicated peer-session record. AdmitUntil is when this
// process stops accepting the token: expires_at while live, grace_until
// after replace or eviction.
type sessionRow struct {
	Hash         []byte
	HashHex      string
	Host         string
	Version      string
	Peer         string
	AttachedUnix int64
	AdmitUntil   time.Time
	State        string
	Seq          int64
	UpdatedAt    time.Time
}

type attachCommit struct {
	Peer         string
	Hash         []byte
	AttachedUnix int64
	TTL          time.Duration
	Grace        time.Duration
	MaxSessions  int
}

type attachOutcome struct {
	Rows    []sessionRow
	Seq     int64
	Expires time.Time
}

func newInstanceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}

func durationMicros(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return d.Microseconds()
}
