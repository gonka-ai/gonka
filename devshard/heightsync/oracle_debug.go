package heightsync

import (
	"context"
	"time"
)

// blockOracleStaleDiagnostics is implemented by blockoracle/client.HTTP (mockdapi / height-sync SSE).
type blockOracleStaleDiagnostics interface {
	StaleDetails() (stale bool, lastRecvAgeMs int64, latestHeight int64, neverReceived bool)
}

// PeerTipCacheDebug is optional debug on courier peer-tip caches (transport.HeightSyncPeerTips).
type PeerTipCacheDebug interface {
	DecidePeerTipSnapshot(now time.Time) (cacheReady bool, verifiedOrigins int, maxFreshHeight int64)
}

// OracleDecideSnapshot is attached to heightsync: decide debug lines.
type OracleDecideSnapshot struct {
	SourceKind        string
	Stale             bool
	LastRecvAgeMs     int64
	LatestHeight      int64
	NeverReceived     bool
	PeerTipCacheReady bool
	VerifiedOrigins   int
}

func snapshotOracleForDecide(src OracleSource, now time.Time) OracleDecideSnapshot {
	if now.IsZero() {
		now = time.Now()
	}
	switch s := src.(type) {
	case *LocalOracleSource:
		return snapshotLocalOracle(s)
	case *PeerTipOracleSource:
		return snapshotPeerTipOracle(s, now)
	default:
		stale := false
		if src != nil {
			stale = src.Stale()
		}
		return OracleDecideSnapshot{SourceKind: "unknown", Stale: stale}
	}
}

func snapshotLocalOracle(s *LocalOracleSource) OracleDecideSnapshot {
	out := OracleDecideSnapshot{SourceKind: "local_block_oracle"}
	if s == nil || s.oracle == nil {
		out.Stale = true
		out.NeverReceived = true
		return out
	}
	if diag, ok := s.oracle.(blockOracleStaleDiagnostics); ok {
		out.Stale, out.LastRecvAgeMs, out.LatestHeight, out.NeverReceived = diag.StaleDetails()
		return out
	}
	out.Stale = s.Stale()
	if hdr, err := s.oracle.Latest(context.Background()); err == nil && hdr != nil {
		out.LatestHeight = hdr.Height
	}
	return out
}

func snapshotPeerTipOracle(s *PeerTipOracleSource, now time.Time) OracleDecideSnapshot {
	out := OracleDecideSnapshot{SourceKind: "peer_tip_cache"}
	if s == nil {
		out.Stale = true
		return out
	}
	out.Stale = s.Stale()
	if s.cache == nil {
		return out
	}
	if dbg, ok := s.cache.(PeerTipCacheDebug); ok {
		out.PeerTipCacheReady, out.VerifiedOrigins, out.LatestHeight = dbg.DecidePeerTipSnapshot(now)
	} else if sec := s.cache.MaxFresh(now, s.freshness); sec != nil {
		out.PeerTipCacheReady = true
		out.LatestHeight = sec.MainnetHeight
	}
	return out
}
