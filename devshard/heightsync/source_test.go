package heightsync_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"devshard/heightsync"

	"github.com/stretchr/testify/require"
)

type fakePeerTipCache struct {
	sec *heightsync.HeightSyncSection
}

func (f *fakePeerTipCache) MaxFresh(time.Time, time.Duration) *heightsync.HeightSyncSection {
	if f.sec == nil {
		return nil
	}
	cp := *f.sec
	return &cp
}

func TestLocalOracleSource_ParityWithChainOracle(t *testing.T) {
	or := &fakeOracle{hdr: &blocks.Header{Height: 42, ChainID: "gonka", BlockHash: []byte{0x01}}}
	src := heightsync.NewLocalOracleSource(or)

	sec, err := src.LatestSection(context.Background())
	require.NoError(t, err)
	require.NotNil(t, sec)
	require.Equal(t, int64(42), sec.MainnetHeight)
	require.False(t, src.Stale())

	stale := &fakeStaleOracle{
		fakeOracle: fakeOracle{hdr: &blocks.Header{Height: 42, ChainID: "gonka", BlockHash: []byte{0x01}}},
		stale:      true,
	}
	require.True(t, heightsync.NewLocalOracleSource(stale).Stale())
}

func TestPeerTipOracleSource_StaleWhenCacheEmpty(t *testing.T) {
	src := heightsync.NewPeerTipOracleSource(&fakePeerTipCache{}, time.Minute)
	require.True(t, src.Stale())
	sec, err := src.LatestSection(context.Background())
	require.NoError(t, err)
	require.Nil(t, sec)
}

func TestPeerTipOracleSource_ReturnsFreshTip(t *testing.T) {
	cached := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         11,
		MainnetBlockHashHex:   "aa",
		OriginatorSenderID:    "gonka1host",
		OriginatorTimestampMs: time.Now().UnixMilli(),
	}
	src := heightsync.NewPeerTipOracleSource(&fakePeerTipCache{sec: cached}, time.Minute)

	require.False(t, src.Stale())
	sec, err := src.LatestSection(context.Background())
	require.NoError(t, err)
	require.Equal(t, "gonka1host", sec.OriginatorSenderID)
	require.Equal(t, int64(11), sec.MainnetHeight)
}

func TestDecide_PeerTipOracleSource_ColdStart(t *testing.T) {
	src := heightsync.NewPeerTipOracleSource(&fakePeerTipCache{}, time.Minute)
	s := heightsync.MustNewAnchorScheduler(8, 4, src)

	got, err, miss := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.Nil(t, got)
	require.True(t, miss)
}

func TestDecide_PeerTipOracleSource_WarmCachePreservesOriginator(t *testing.T) {
	cached := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         11,
		MainnetBlockHashHex:   "aa",
		OriginatorSenderID:    "gonka1hostA",
		OriginatorTimestampMs: time.Now().UnixMilli(),
	}
	src := heightsync.NewPeerTipOracleSource(&fakePeerTipCache{sec: cached}, time.Minute)
	s := heightsync.MustNewAnchorScheduler(8, 4, src)

	got, err, miss := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 2})
	require.NoError(t, err)
	require.False(t, miss)
	require.NotNil(t, got)
	require.Equal(t, "gonka1hostA", got.OriginatorSenderID)
	require.Equal(t, cached.OriginatorTimestampMs, got.OriginatorTimestampMs)
	require.NotEmpty(t, got.OriginatorSenderID)
}

func TestDecide_LocalOracleSource_OracleError(t *testing.T) {
	or := &fakeOracle{err: errors.New("down")}
	s := heightsync.MustNewAnchorScheduler(8, 4, heightsync.NewLocalOracleSource(or))

	got, err, miss := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.Nil(t, got)
	require.True(t, miss)
}
