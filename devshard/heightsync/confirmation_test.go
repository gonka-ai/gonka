package heightsync_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks"
	"devshard/heightsync"
)

type stubConfirmOracle struct {
	hdr   *blocks.Header
	stale bool
	err   error
}

func (o *stubConfirmOracle) Latest(context.Context) (*blocks.Header, error) {
	if o.err != nil {
		return nil, o.err
	}
	if o.stale {
		return nil, context.Canceled
	}
	if o.hdr == nil {
		return nil, context.Canceled
	}
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}

func (o *stubConfirmOracle) At(context.Context, int64) (*blocks.Header, error) {
	return nil, context.Canceled
}

func (o *stubConfirmOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, context.Canceled
}

func (o *stubConfirmOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func (o *stubConfirmOracle) Stale() bool { return o.stale }

func TestConfirm_QuorumThreshold(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	or := &stubConfirmOracle{hdr: &blocks.Header{Height: 100, BlockHash: []byte{1}}}
	idx := heightsync.NewConfirmationIndex(heightsync.ConfirmationConfig{
		Roster: []string{"h1", "h2", "h3", "h4"},
		Quorum: 3,
		Oracle: or,
		Now:    func() time.Time { return now },
	})

	for _, origin := range []string{"h1", "h2"} {
		idx.RecordAttestation(heightsync.AnchorAttestation{
			PeerID:             "carrier",
			OriginatorSenderID: origin,
			MainnetHeight:      11,
			MainnetBlockHash:   []byte{0xaa},
			ObservedAtUnixMs:   now.UnixMilli(),
		})
	}
	require.Equal(t, heightsync.ConfirmPending, idx.IsStrictlyConfirmed(11))

	idx.RecordAttestation(heightsync.AnchorAttestation{
		PeerID:             "carrier",
		OriginatorSenderID: "h3",
		MainnetHeight:      11,
		MainnetBlockHash:   []byte{0xaa},
		ObservedAtUnixMs:   now.UnixMilli(),
	})
	require.Equal(t, heightsync.ConfirmConfirmed, idx.IsStrictlyConfirmed(11))

	idx.SetQuorum(5)
	require.Equal(t, heightsync.ConfirmConfirmed, idx.IsStrictlyConfirmed(11),
		"monotonic: stays confirmed after impossible quota")
}

func TestConfirm_StaleWhenOracleStale(t *testing.T) {
	or := &stubConfirmOracle{
		hdr:   &blocks.Header{Height: 10, BlockHash: []byte{1}},
		stale: true,
	}
	idx := heightsync.NewConfirmationIndex(heightsync.ConfirmationConfig{
		Roster: []string{"h1"},
		Quorum: 1,
		Oracle: or,
	})
	require.Equal(t, heightsync.ConfirmStale, idx.IsStrictlyConfirmed(10))
}

func TestConfirm_FreshnessAndWindowEligibility(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	const tip int64 = 11
	or := &stubConfirmOracle{hdr: &blocks.Header{Height: tip, BlockHash: []byte{1}}}
	idx := heightsync.NewConfirmationIndex(heightsync.ConfirmationConfig{
		Roster:        []string{"h1", "h2"},
		Quorum:        2,
		Freshness:     time.Minute,
		WindowHeights: 10,
		Oracle:        or,
		Now:           func() time.Time { return now },
	})

	idx.RecordAttestation(heightsync.AnchorAttestation{
		OriginatorSenderID: "h1",
		MainnetHeight:      tip,
		MainnetBlockHash:   []byte{1},
		ObservedAtUnixMs:   now.Add(-2 * time.Minute).UnixMilli(),
	})
	idx.RecordAttestation(heightsync.AnchorAttestation{
		OriginatorSenderID: "h2",
		MainnetHeight:      tip,
		MainnetBlockHash:   []byte{1},
		ObservedAtUnixMs:   now.UnixMilli(),
	})
	require.Equal(t, heightsync.ConfirmPending, idx.IsStrictlyConfirmed(uint64(tip)))

	idx.RecordAttestation(heightsync.AnchorAttestation{
		OriginatorSenderID: "h1",
		MainnetHeight:      tip,
		MainnetBlockHash:   []byte{1},
		ObservedAtUnixMs:   now.UnixMilli(),
	})
	require.Equal(t, heightsync.ConfirmConfirmed, idx.IsStrictlyConfirmed(uint64(tip)))
}

func TestConfirm_CompactOnTipAdvance(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	or := &stubConfirmOracle{hdr: &blocks.Header{Height: 50, BlockHash: []byte{1}}}
	idx := heightsync.NewConfirmationIndex(heightsync.ConfirmationConfig{
		Roster:        []string{"h1"},
		Quorum:        1,
		Freshness:     time.Hour,
		WindowHeights: 10,
		Oracle:        or,
		Now:           func() time.Time { return now },
	})
	idx.RecordAttestation(heightsync.AnchorAttestation{
		OriginatorSenderID: "h1",
		MainnetHeight:      20,
		MainnetBlockHash:   []byte{1},
		ObservedAtUnixMs:   now.UnixMilli(),
	})
	require.Equal(t, heightsync.ConfirmPending, idx.IsStrictlyConfirmed(25))

	or.hdr.Height = 200
	require.Equal(t, heightsync.ConfirmPending, idx.IsStrictlyConfirmed(25),
		"compact after tip advance drops out-of-window originator rows")
}

func TestConfirm_MonotonicityAfterPrune(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	const h int64 = 11
	or := &stubConfirmOracle{hdr: &blocks.Header{Height: h, BlockHash: []byte{1}}}
	idx := heightsync.NewConfirmationIndex(heightsync.ConfirmationConfig{
		Roster:        []string{"h1", "h2", "h3"},
		Quorum:        3,
		Freshness:     time.Hour,
		WindowHeights: 5,
		Oracle:        or,
		Now:           func() time.Time { return now },
	})
	for _, o := range []string{"h1", "h2", "h3"} {
		idx.RecordAttestation(heightsync.AnchorAttestation{
			OriginatorSenderID: o,
			MainnetHeight:      h,
			MainnetBlockHash:   []byte{1},
			ObservedAtUnixMs:   now.UnixMilli(),
		})
	}
	require.Equal(t, heightsync.ConfirmConfirmed, idx.IsStrictlyConfirmed(uint64(h)))

	or.hdr.Height = 200
	require.Equal(t, heightsync.ConfirmConfirmed, idx.IsStrictlyConfirmed(uint64(h)),
		"confirmed_heights must stay confirmed after index compact on tip advance")
}

func TestConfirm_IndexUsesOriginatorNotCarrier(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	carrier := "gonka1user"
	idx := heightsync.NewConfirmationIndex(heightsync.ConfirmationConfig{
		Roster: []string{"h1", "h2", "h3"},
		Quorum: 3,
		Oracle: &stubConfirmOracle{hdr: &blocks.Header{Height: 10, BlockHash: []byte{1}}},
		Now:    func() time.Time { return now },
	})
	for _, origin := range []string{"h1", "h2", "h3"} {
		idx.RecordAttestation(heightsync.AnchorAttestation{
			PeerID:             carrier,
			OriginatorSenderID: origin,
			MainnetHeight:      11,
			MainnetBlockHash:   []byte{1},
			ObservedAtUnixMs:   now.UnixMilli(),
		})
	}
	require.Equal(t, heightsync.ConfirmConfirmed, idx.IsStrictlyConfirmed(11))
}

func TestConfirm_LateOracleTipBelowH(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	const hOld, hNew int64 = 10, 11
	or := &stubConfirmOracle{hdr: &blocks.Header{Height: hOld, BlockHash: []byte{1}}}
	idx := heightsync.NewConfirmationIndex(heightsync.ConfirmationConfig{
		Roster: []string{"h1", "h2", "h3"},
		Quorum: 3,
		Oracle: or,
		Now:    func() time.Time { return now },
	})
	for _, origin := range []string{"h1", "h2", "h3"} {
		idx.RecordAttestation(heightsync.AnchorAttestation{
			OriginatorSenderID: origin,
			MainnetHeight:      hNew,
			MainnetBlockHash:   []byte{2},
			ObservedAtUnixMs:   now.UnixMilli(),
		})
	}
	require.Equal(t, heightsync.ConfirmConfirmed, idx.IsStrictlyConfirmed(uint64(hNew)))
}

func TestQuorumForRoster(t *testing.T) {
	require.Equal(t, 3, heightsync.QuorumForRoster(4))
	require.Equal(t, 7, heightsync.QuorumForRoster(10))
}
