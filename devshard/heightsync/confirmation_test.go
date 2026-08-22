package heightsync_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
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

	or.hdr.Height = 14
	require.Equal(t, heightsync.ConfirmConfirmed, idx.IsStrictlyConfirmed(uint64(h)),
		"confirmed heights still inside W_conf stay confirmed after compact")
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

type blockingConfirmOracle struct {
	entered chan struct{}
	release chan struct{}
	hdr     *blocks.Header
}

func (o *blockingConfirmOracle) Latest(ctx context.Context) (*blocks.Header, error) {
	select {
	case <-o.entered:
	default:
		close(o.entered)
	}
	select {
	case <-o.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if o.hdr == nil {
		return nil, context.Canceled
	}
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}
func (o *blockingConfirmOracle) At(context.Context, int64) (*blocks.Header, error) {
	return nil, context.Canceled
}
func (o *blockingConfirmOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, context.Canceled
}
func (o *blockingConfirmOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func TestConfirm_BlockedOracleDoesNotHoldMutex(t *testing.T) {
	or := &blockingConfirmOracle{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		hdr:     &blocks.Header{Height: 10, BlockHash: []byte{1}},
	}
	idx := heightsync.NewConfirmationIndex(heightsync.ConfirmationConfig{
		Roster: []string{"h1"},
		Quorum: 1,
		Oracle: or,
		Now:    func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
	done := make(chan struct{})
	go func() {
		idx.RecordAttestation(heightsync.AnchorAttestation{
			OriginatorSenderID: "h1",
			MainnetHeight:      10,
			MainnetBlockHash:   []byte{1},
			ObservedAtUnixMs:   time.Unix(1_700_000_000, 0).UnixMilli(),
		})
		close(done)
	}()
	select {
	case <-or.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Latest was not entered")
	}
	unblocked := make(chan struct{})
	go func() {
		idx.SetQuorum(1)
		close(unblocked)
	}()
	select {
	case <-unblocked:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("confirmation mutex held during oracle I/O")
	}
	close(or.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RecordAttestation did not return")
	}
}

func TestConfirm_OriginatorTimestampDrivesFreshness(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	const f = time.Minute
	or := &stubConfirmOracle{hdr: &blocks.Header{Height: 11, BlockHash: []byte{1}}}
	idx := heightsync.NewConfirmationIndex(heightsync.ConfirmationConfig{
		Roster:    []string{"h1"},
		Quorum:    1,
		Freshness: f,
		Oracle:    or,
		Now:       func() time.Time { return now },
	})
	idx.RecordAttestation(heightsync.AnchorAttestation{
		PeerID:                "carrier",
		OriginatorSenderID:    "h1",
		MainnetHeight:         11,
		MainnetBlockHash:      []byte{1},
		ObservedAtUnixMs:      now.UnixMilli(),
		OriginatorTimestampMs: now.Add(-f + time.Second).UnixMilli(),
	})
	now = now.Add(f)
	require.Equal(t, heightsync.ConfirmPending, idx.IsStrictlyConfirmed(11),
		"F later the originator ts is stale; receipt time would still be eligible")
}

func TestConfirm_ConfirmedHeightsBounded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	const window int64 = 10
	or := &stubConfirmOracle{hdr: &blocks.Header{Height: 1, BlockHash: []byte{1}}}
	idx := heightsync.NewConfirmationIndex(heightsync.ConfirmationConfig{
		Roster:        []string{"h1"},
		Quorum:        1,
		Freshness:     time.Hour,
		WindowHeights: window,
		Oracle:        or,
		Now:           func() time.Time { return now },
	})
	for h := int64(1); h <= 200; h++ {
		or.hdr.Height = h
		idx.RecordAttestation(heightsync.AnchorAttestation{
			OriginatorSenderID: "h1",
			MainnetHeight:      h,
			MainnetBlockHash:   []byte{1},
			ObservedAtUnixMs:   now.UnixMilli(),
		})
		require.Equal(t, heightsync.ConfirmConfirmed, idx.IsStrictlyConfirmed(uint64(h)))
	}
	require.LessOrEqual(t, idx.ConfirmedCount(), int(window)+2,
		"confirmedHeights must prune below tip−W_conf")
	require.Equal(t, heightsync.ConfirmPending, idx.IsStrictlyConfirmed(1))
	require.Equal(t, heightsync.ConfirmConfirmed, idx.IsStrictlyConfirmed(200))
}
