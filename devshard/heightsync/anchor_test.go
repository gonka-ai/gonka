package heightsync_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"devshard/chainoracle/blocks"
	"devshard/heightsync"

	"github.com/stretchr/testify/require"
)

type fakeOracle struct {
	hdr *blocks.Header
	err error
}

func (f *fakeOracle) Latest(context.Context) (*blocks.Header, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.hdr == nil {
		return nil, nil
	}
	cpy := *f.hdr
	cpy.BlockHash = append([]byte(nil), f.hdr.BlockHash...)
	return &cpy, nil
}

func (f *fakeOracle) At(context.Context, int64) (*blocks.Header, error) { return nil, nil }
func (f *fakeOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, nil
}
func (f *fakeOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	return nil, nil
}

type fakeStaleOracle struct {
	fakeOracle
	stale bool
}

func (f *fakeStaleOracle) Stale() bool { return f.stale }

func (f *fakeStaleOracle) StaleDetails() (stale bool, lastRecvAgeMs int64, latestHeight int64, neverReceived bool) {
	if f.hdr != nil {
		return f.stale, 12_000, f.hdr.Height, false
	}
	return f.stale, 0, 0, true
}

func TestAnchorScheduler_StaleFeedEmitsDegradedAnchorInSyncTurn(t *testing.T) {
	or := &fakeStaleOracle{
		fakeOracle: fakeOracle{hdr: &blocks.Header{Height: 100, ChainID: "gonka", BlockHash: []byte{0xab}}},
		stale:      true,
	}
	s := heightsync.MustNewAnchorScheduler(8, 4, heightsync.NewLocalOracleSource(or))
	got, err, miss := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 2})
	require.NoError(t, err)
	require.False(t, miss)
	require.NotNil(t, got)
	require.Equal(t, int64(100), got.MainnetHeight)
	require.Equal(t, int64(12_000), got.TipStaleAfterMs)
}

func TestAnchorScheduler_SyncTurnSweepK10Slots4(t *testing.T) {
	or := &fakeOracle{hdr: &blocks.Header{Height: 100, ChainID: "gonka", BlockHash: []byte{0xcc}}}
	s := heightsync.MustNewAnchorScheduler(10, 4, heightsync.NewLocalOracleSource(or))

	expectedAnchor := map[uint64]bool{}
	for _, n := range []uint64{
		1, 2, 3, 4, // initial sync turn
		10, 11, 12, 13, // first periodic sync turn
		20, 21, 22, 23, // second periodic sync turn
		30, 31, 32, 33, // third periodic sync turn
	} {
		expectedAnchor[n] = true
	}

	for nonce := uint64(1); nonce <= 35; nonce++ {
		got, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: nonce})
		require.NoError(t, err, "nonce=%d", nonce)
		if expectedAnchor[nonce] {
			require.NotNilf(t, got, "expected Anchor at nonce=%d", nonce)
		} else {
			require.Nilf(t, got, "expected Omit at nonce=%d", nonce)
		}
	}
}

func TestAnchorScheduler_SlotsOneCollapsesToCadence(t *testing.T) {
	or := &fakeOracle{hdr: &blocks.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(10, 1, heightsync.NewLocalOracleSource(or))

	expectedAnchor := map[uint64]bool{1: true, 10: true, 20: true, 30: true}

	for nonce := uint64(1); nonce <= 35; nonce++ {
		got, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: nonce})
		require.NoError(t, err, "nonce=%d", nonce)
		if expectedAnchor[nonce] {
			require.NotNilf(t, got, "expected Anchor at nonce=%d", nonce)
		} else {
			require.Nilf(t, got, "expected Omit at nonce=%d", nonce)
		}
	}
}

func TestAnchorScheduler_KEqualsSlotsIsWallToWall(t *testing.T) {
	or := &fakeOracle{hdr: &blocks.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(4, 4, heightsync.NewLocalOracleSource(or))

	for nonce := uint64(1); nonce <= 12; nonce++ {
		got, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: nonce})
		require.NoError(t, err)
		require.NotNilf(t, got, "K=slots_num=4 must Anchor every nonce; Omit at nonce=%d", nonce)
	}
}

func TestAnchorScheduler_SessionStartOverridesOmitWindow(t *testing.T) {
	or := &fakeOracle{hdr: &blocks.Header{Height: 5, ChainID: "gonka", BlockHash: []byte{0x05}}}
	s := heightsync.MustNewAnchorScheduler(10, 4, heightsync.NewLocalOracleSource(or))

	got, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 7, SessionStart: true})
	require.NoError(t, err)
	require.NotNil(t, got, "SessionStart must force Anchor at nonce=7 (Omit window)")
}

func TestAnchorScheduler_ForceAnchorOverridesOmitWindow(t *testing.T) {
	or := &fakeOracle{hdr: &blocks.Header{Height: 5, ChainID: "gonka", BlockHash: []byte{0x05}}}
	s := heightsync.MustNewAnchorScheduler(10, 4, heightsync.NewLocalOracleSource(or))

	got, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 7, ForceAnchor: true})
	require.NoError(t, err)
	require.NotNil(t, got, "ForceAnchor must force Anchor at nonce=7 (Omit window)")
}

func TestAnchorScheduler_NonceZeroEmitsOmit(t *testing.T) {
	or := &fakeOracle{hdr: &blocks.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(10, 4, heightsync.NewLocalOracleSource(or))

	got, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 0})
	require.NoError(t, err)
	require.Nil(t, got, "nonce=0 with no hints must be Omit")
}

func TestAnchorScheduler_KZeroDefaultsToTen(t *testing.T) {
	or := &fakeOracle{hdr: &blocks.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(0, 1, heightsync.NewLocalOracleSource(or))

	at9, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 9})
	require.NoError(t, err)
	require.Nil(t, at9)

	at10, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 10})
	require.NoError(t, err)
	require.NotNil(t, at10)
}

func TestAnchorScheduler_SlotsZeroDefaultsToOne(t *testing.T) {
	or := &fakeOracle{hdr: &blocks.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(10, 0, heightsync.NewLocalOracleSource(or))

	at1, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.NotNil(t, at1)

	at2, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 2})
	require.NoError(t, err)
	require.Nil(t, at2)
}

func TestAnchorScheduler_KLessThanSlotsIsRejected(t *testing.T) {
	or := &fakeOracle{}
	_, err := heightsync.NewAnchorScheduler(2, 4, heightsync.NewLocalOracleSource(or))
	require.ErrorIs(t, err, heightsync.ErrInvalidConfig)
}

func TestAnchorScheduler_OracleErrorOmitUnlessForced(t *testing.T) {
	or := &fakeOracle{
		hdr: &blocks.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x1}},
		err: errors.New("rpc down"),
	}
	s := heightsync.MustNewAnchorScheduler(10, 4, heightsync.NewLocalOracleSource(or))

	cadence, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.Nil(t, cadence)

	session, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 100, SessionStart: true})
	require.NoError(t, err)
	require.Nil(t, session)

	forced, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 100, ForceAnchor: true})
	require.Error(t, err)
	require.Nil(t, forced)
}

func TestAnchorScheduler_NilOracleHeaderHandling(t *testing.T) {
	or := &fakeOracle{}
	s := heightsync.MustNewAnchorScheduler(10, 4, heightsync.NewLocalOracleSource(or))

	cadence, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.Nil(t, cadence)

	forced, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 100, ForceAnchor: true})
	require.ErrorIs(t, err, heightsync.ErrNilOracleHeader)
	require.Nil(t, forced)
}

func TestAnchorScheduler_NoOracleHandling(t *testing.T) {
	s := heightsync.MustNewAnchorScheduler(10, 4, nil)

	cadence, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.Nil(t, cadence)

	forced, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 100, ForceAnchor: true})
	require.ErrorIs(t, err, heightsync.ErrNoOracle)
	require.Nil(t, forced)
}

func TestAnchorScheduler_TimestampSet(t *testing.T) {
	or := &fakeOracle{hdr: &blocks.Header{Height: 7, ChainID: "gonka", BlockHash: []byte{0x07}}}
	s := heightsync.MustNewAnchorScheduler(10, 4, heightsync.NewLocalOracleSource(or))

	before := time.Now().Add(-time.Second).UnixMilli()
	out, err, _ := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.GreaterOrEqual(t, out.TimestampUnixMs, before)
}

func TestDecide_OriginatorPopulatedFromSigner(t *testing.T) {
	const originator = "gonka1hostoriginator"
	or := &fakeOracle{hdr: &blocks.Header{Height: 11, ChainID: "gonka", BlockHash: []byte{0x0b}}}
	s := heightsync.MustNewAnchorScheduler(8, 4, heightsync.NewLocalOracleSource(or))

	before := time.Now().Add(-time.Second).UnixMilli()
	got, err, miss := s.Decide(context.Background(), heightsync.DecideHints{
		Nonce:              1,
		OriginatorSenderID: originator,
	})
	require.NoError(t, err)
	require.False(t, miss)
	require.NotNil(t, got)
	require.Equal(t, originator, got.OriginatorSenderID)
	require.Equal(t, got.TimestampUnixMs, got.OriginatorTimestampMs)
	require.GreaterOrEqual(t, got.OriginatorTimestampMs, before)
}

func TestDecide_OriginatorOmittedInCourierMode(t *testing.T) {
	or := &fakeOracle{hdr: &blocks.Header{Height: 11, ChainID: "gonka", BlockHash: []byte{0x0b}}}
	s := heightsync.MustNewAnchorScheduler(8, 4, heightsync.NewLocalOracleSource(or))

	got, err, miss := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.False(t, miss)
	require.NotNil(t, got)
	require.Empty(t, got.OriginatorSenderID)
	require.Zero(t, got.OriginatorTimestampMs)
}

func TestAnchorScheduler_EscrowForcedWindow(t *testing.T) {
	or := &fakeOracle{hdr: &blocks.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(8, 4, heightsync.NewLocalOracleSource(or))
	got, err, _ := s.Decide(context.Background(), heightsync.DecideHints{
		Nonce: 7,
		Escrow: &heightsync.EscrowHeightSyncHints{
			ForcedStart: 7,
			ForcedEnd:   10,
			TurnK:       8,
			TurnSlots:   4,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, got)
}

func TestDecide_LazyEmissionOutsideSyncTurn(t *testing.T) {
	cached := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         51,
		MainnetBlockHashHex:   "bb",
		OriginatorSenderID:    "gonka1hostA",
		OriginatorTimestampMs: time.Now().UnixMilli(),
	}
	src := heightsync.NewPeerTipOracleSource(&fakePeerTipCache{sec: cached}, time.Minute)
	s := heightsync.MustNewAnchorScheduler(8, 4, src)
	tips := newMapPropagator()

	const hostB = "http://host-b"
	const hostC = "http://host-c"

	got5, err, miss := s.Decide(context.Background(), heightsync.DecideHints{
		Nonce:      5,
		Recipient:  hostB,
		Propagator: tips,
	})
	require.NoError(t, err)
	require.False(t, miss)
	require.NotNil(t, got5, "omit window nonce 5 must lazy-Anchor to unseen host")
	require.Equal(t, int64(51), got5.MainnetHeight)
	tips.mark(hostB, 51)

	got6same, err, miss := s.Decide(context.Background(), heightsync.DecideHints{
		Nonce:      6,
		Recipient:  hostB,
		Propagator: tips,
	})
	require.NoError(t, err)
	require.False(t, miss)
	require.Nil(t, got6same, "same recipient must Omit until cache advances")

	got6other, err, miss := s.Decide(context.Background(), heightsync.DecideHints{
		Nonce:      6,
		Recipient:  hostC,
		Propagator: tips,
	})
	require.NoError(t, err)
	require.False(t, miss)
	require.NotNil(t, got6other, "different recipient must still lazy-Anchor")
}

func TestDecide_SyncTurnOverridesLastPropagated(t *testing.T) {
	cached := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         50,
		MainnetBlockHashHex:   "aa",
		OriginatorSenderID:    "gonka1hostA",
		OriginatorTimestampMs: time.Now().UnixMilli(),
	}
	src := heightsync.NewPeerTipOracleSource(&fakePeerTipCache{sec: cached}, time.Minute)
	s := heightsync.MustNewAnchorScheduler(8, 4, src)
	tips := newMapPropagator()

	const hostA = "http://host-a"
	tips.mark(hostA, 50)

	gotSync, err, miss := s.Decide(context.Background(), heightsync.DecideHints{
		Nonce:      2,
		Recipient:  hostA,
		Propagator: tips,
	})
	require.NoError(t, err)
	require.False(t, miss)
	require.NotNil(t, gotSync, "sync-turn must Anchor even when last_propagated already at tip height")

	gotLazy, err, miss := s.Decide(context.Background(), heightsync.DecideHints{
		Nonce:      5,
		Recipient:  hostA,
		Propagator: tips,
	})
	require.NoError(t, err)
	require.False(t, miss)
	require.Nil(t, gotLazy, "omit window must skip when height already propagated")
}

func TestDecide_LazyEmitDisabledWithoutPropagator(t *testing.T) {
	cached := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       11,
		MainnetBlockHashHex: "aa",
	}
	src := heightsync.NewPeerTipOracleSource(&fakePeerTipCache{sec: cached}, time.Minute)
	s := heightsync.MustNewAnchorScheduler(8, 4, src)

	got, err, miss := s.Decide(context.Background(), heightsync.DecideHints{
		Nonce:     5,
		Recipient: "http://host-a",
	})
	require.NoError(t, err)
	require.False(t, miss)
	require.Nil(t, got, "omit window without Propagator must stay Omit")
}

type mapPropagator struct {
	last map[string]uint64
}

func newMapPropagator() *mapPropagator {
	return &mapPropagator{last: make(map[string]uint64)}
}

func (m *mapPropagator) ShouldPropagateTo(recipient string, h uint64) bool {
	if recipient == "" || h == 0 {
		return false
	}
	return h > m.last[recipient]
}

func (m *mapPropagator) mark(recipient string, h uint64) {
	if h > m.last[recipient] {
		m.last[recipient] = h
	}
}

func TestAnchorScheduler_CadenceSwallowTail(t *testing.T) {
	or := &fakeOracle{hdr: &blocks.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(8, 4, heightsync.NewLocalOracleSource(or))
	got, err, _ := s.Decide(context.Background(), heightsync.DecideHints{
		Nonce: 11,
		Escrow: &heightsync.EscrowHeightSyncHints{
			CadenceSwallowUntil: 11,
			SwallowFe:           10,
			TurnK:               8,
			TurnSlots:           4,
		},
	})
	require.NoError(t, err)
	require.Nil(t, got, "periodic Anchor at nonce 11 swallowed")
}

type blockingAnchorOracle struct {
	entered chan struct{}
	release chan struct{}
	hdr     *blocks.Header
}

func (o *blockingAnchorOracle) Latest(ctx context.Context) (*blocks.Header, error) {
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
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}
func (o *blockingAnchorOracle) At(context.Context, int64) (*blocks.Header, error) { return nil, nil }
func (o *blockingAnchorOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, nil
}
func (o *blockingAnchorOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	return nil, nil
}

func TestAnchorScheduler_BlockedOracleDoesNotHoldMutex(t *testing.T) {
	or := &blockingAnchorOracle{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		hdr:     &blocks.Header{Height: 100, ChainID: "gonka", BlockHash: []byte{0xab}},
	}
	s := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, or)
	done := make(chan struct{})
	go func() {
		_, _, _ = s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
		close(done)
	}()
	select {
	case <-or.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Latest was not entered")
	}
	unblocked := make(chan struct{})
	go func() {
		_ = s.K()
		close(unblocked)
	}()
	select {
	case <-unblocked:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("scheduler mutex held during oracle I/O")
	}
	close(or.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Decide did not return")
	}
}
