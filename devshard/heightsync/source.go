package heightsync

import (
	"context"
	"encoding/hex"
	"time"

	"devshard/chainoracle/blocks"
)

// OracleSource supplies Anchor sections for AnchorScheduler.Decide.
type OracleSource interface {
	LatestSection(ctx context.Context) (*HeightSyncSection, error)
	Stale() bool
}

// PeerTipCache is the peer-tip store used by courier-mode users (devshardctl).
type PeerTipCache interface {
	MaxFresh(now time.Time, freshness time.Duration) *HeightSyncSection
}

// LocalOracleSource wraps a host-side block oracle (follower / height-sync SSE).
type LocalOracleSource struct {
	oracle blocks.BlockOracle
	now    func() time.Time
}

// NewLocalOracleSource adapts a BlockOracle for AnchorScheduler.
func NewLocalOracleSource(oracle blocks.BlockOracle) *LocalOracleSource {
	return &LocalOracleSource{oracle: oracle, now: time.Now}
}

func (s *LocalOracleSource) Stale() bool {
	if s == nil || s.oracle == nil {
		return true
	}
	if so, ok := s.oracle.(interface{ Stale() bool }); ok {
		return so.Stale()
	}
	return false
}

func (s *LocalOracleSource) LatestSection(ctx context.Context) (*HeightSyncSection, error) {
	if s == nil || s.oracle == nil {
		return nil, ErrNoOracle
	}
	hdr, err := s.oracle.Latest(ctx)
	if err != nil {
		return nil, err
	}
	if hdr == nil {
		return nil, ErrNilOracleHeader
	}
	clock := s.now
	if clock == nil {
		clock = time.Now
	}
	return &HeightSyncSection{
		ChainID:             hdr.ChainID,
		ProofType:           AnchorProofType,
		MainnetHeight:       hdr.Height,
		MainnetBlockHashHex: hex.EncodeToString(hdr.BlockHash),
		TimestampUnixMs:     clock().UnixMilli(),
	}, nil
}

// PeerTipOracleSource reads the highest fresh cached host Anchor (courier user).
type PeerTipOracleSource struct {
	cache     PeerTipCache
	freshness time.Duration
	now       func() time.Time
}

// NewPeerTipOracleSource constructs a courier-mode oracle from a peer-tip cache.
func NewPeerTipOracleSource(cache PeerTipCache, freshness time.Duration) *PeerTipOracleSource {
	if freshness <= 0 {
		freshness = 60 * time.Second
	}
	return &PeerTipOracleSource{cache: cache, freshness: freshness, now: time.Now}
}

func (s *PeerTipOracleSource) Stale() bool {
	if s == nil || s.cache == nil {
		return true
	}
	clock := s.now
	if clock == nil {
		clock = time.Now
	}
	return s.cache.MaxFresh(clock(), s.freshness) == nil
}

func (s *PeerTipOracleSource) LatestSection(context.Context) (*HeightSyncSection, error) {
	if s == nil || s.cache == nil {
		return nil, ErrNoOracle
	}
	clock := s.now
	if clock == nil {
		clock = time.Now
	}
	sec := s.cache.MaxFresh(clock(), s.freshness)
	if sec == nil {
		return nil, nil
	}
	cp := *sec
	return &cp, nil
}
