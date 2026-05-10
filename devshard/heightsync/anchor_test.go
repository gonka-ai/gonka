package heightsync_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"devshard/blockoracle"
	"devshard/heightsync"

	"github.com/stretchr/testify/require"
)

type fakeOracle struct {
	hdr *blockoracle.Header
	err error
}

func (f *fakeOracle) Latest(context.Context) (*blockoracle.Header, error) {
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

func (f *fakeOracle) At(context.Context, int64) (*blockoracle.Header, error) { return nil, nil }
func (f *fakeOracle) Prove(context.Context, string, int64) (*blockoracle.Proof, error) {
	return nil, nil
}
func (f *fakeOracle) Subscribe(context.Context, int64) (<-chan *blockoracle.Header, error) {
	return nil, nil
}

func TestAnchorScheduler_SyncTurnSweepK10Slots4(t *testing.T) {
	or := &fakeOracle{hdr: &blockoracle.Header{Height: 100, ChainID: "gonka", BlockHash: []byte{0xcc}}}
	s := heightsync.MustNewAnchorScheduler(10, 4, or)

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
		got, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: nonce})
		require.NoError(t, err, "nonce=%d", nonce)
		if expectedAnchor[nonce] {
			require.NotNilf(t, got, "expected Anchor at nonce=%d", nonce)
		} else {
			require.Nilf(t, got, "expected Omit at nonce=%d", nonce)
		}
	}
}

func TestAnchorScheduler_SlotsOneCollapsesToCadence(t *testing.T) {
	or := &fakeOracle{hdr: &blockoracle.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(10, 1, or)

	expectedAnchor := map[uint64]bool{1: true, 10: true, 20: true, 30: true}

	for nonce := uint64(1); nonce <= 35; nonce++ {
		got, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: nonce})
		require.NoError(t, err, "nonce=%d", nonce)
		if expectedAnchor[nonce] {
			require.NotNilf(t, got, "expected Anchor at nonce=%d", nonce)
		} else {
			require.Nilf(t, got, "expected Omit at nonce=%d", nonce)
		}
	}
}

func TestAnchorScheduler_KEqualsSlotsIsWallToWall(t *testing.T) {
	or := &fakeOracle{hdr: &blockoracle.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(4, 4, or)

	for nonce := uint64(1); nonce <= 12; nonce++ {
		got, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: nonce})
		require.NoError(t, err)
		require.NotNilf(t, got, "K=slots_num=4 must Anchor every nonce; Omit at nonce=%d", nonce)
	}
}

func TestAnchorScheduler_SessionStartOverridesOmitWindow(t *testing.T) {
	or := &fakeOracle{hdr: &blockoracle.Header{Height: 5, ChainID: "gonka", BlockHash: []byte{0x05}}}
	s := heightsync.MustNewAnchorScheduler(10, 4, or)

	got, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 7, SessionStart: true})
	require.NoError(t, err)
	require.NotNil(t, got, "SessionStart must force Anchor at nonce=7 (Omit window)")
}

func TestAnchorScheduler_ForceAnchorOverridesOmitWindow(t *testing.T) {
	or := &fakeOracle{hdr: &blockoracle.Header{Height: 5, ChainID: "gonka", BlockHash: []byte{0x05}}}
	s := heightsync.MustNewAnchorScheduler(10, 4, or)

	got, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 7, ForceAnchor: true})
	require.NoError(t, err)
	require.NotNil(t, got, "ForceAnchor must force Anchor at nonce=7 (Omit window)")
}

func TestAnchorScheduler_NonceZeroEmitsOmit(t *testing.T) {
	or := &fakeOracle{hdr: &blockoracle.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(10, 4, or)

	got, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 0})
	require.NoError(t, err)
	require.Nil(t, got, "nonce=0 with no hints must be Omit")
}

func TestAnchorScheduler_KZeroDefaultsToTen(t *testing.T) {
	or := &fakeOracle{hdr: &blockoracle.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(0, 1, or)

	at9, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 9})
	require.NoError(t, err)
	require.Nil(t, at9)

	at10, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 10})
	require.NoError(t, err)
	require.NotNil(t, at10)
}

func TestAnchorScheduler_SlotsZeroDefaultsToOne(t *testing.T) {
	or := &fakeOracle{hdr: &blockoracle.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(10, 0, or)

	at1, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.NotNil(t, at1)

	at2, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 2})
	require.NoError(t, err)
	require.Nil(t, at2)
}

func TestAnchorScheduler_KLessThanSlotsIsRejected(t *testing.T) {
	or := &fakeOracle{}
	_, err := heightsync.NewAnchorScheduler(2, 4, or)
	require.ErrorIs(t, err, heightsync.ErrInvalidConfig)
}

func TestAnchorScheduler_OracleErrorOmitUnlessForced(t *testing.T) {
	or := &fakeOracle{
		hdr: &blockoracle.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x1}},
		err: errors.New("rpc down"),
	}
	s := heightsync.MustNewAnchorScheduler(10, 4, or)

	cadence, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.Nil(t, cadence)

	session, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 100, SessionStart: true})
	require.NoError(t, err)
	require.Nil(t, session)

	forced, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 100, ForceAnchor: true})
	require.Error(t, err)
	require.Nil(t, forced)
}

func TestAnchorScheduler_NilOracleHeaderHandling(t *testing.T) {
	or := &fakeOracle{}
	s := heightsync.MustNewAnchorScheduler(10, 4, or)

	cadence, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.Nil(t, cadence)

	forced, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 100, ForceAnchor: true})
	require.ErrorIs(t, err, heightsync.ErrNilOracleHeader)
	require.Nil(t, forced)
}

func TestAnchorScheduler_NoOracleHandling(t *testing.T) {
	s := heightsync.MustNewAnchorScheduler(10, 4, nil)

	cadence, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.Nil(t, cadence)

	forced, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 100, ForceAnchor: true})
	require.ErrorIs(t, err, heightsync.ErrNoOracle)
	require.Nil(t, forced)
}

func TestAnchorScheduler_TimestampSet(t *testing.T) {
	or := &fakeOracle{hdr: &blockoracle.Header{Height: 7, ChainID: "gonka", BlockHash: []byte{0x07}}}
	s := heightsync.MustNewAnchorScheduler(10, 4, or)

	before := time.Now().Add(-time.Second).UnixMilli()
	out, err := s.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.GreaterOrEqual(t, out.TimestampUnixMs, before)
}

func TestAnchorScheduler_EscrowForcedWindow(t *testing.T) {
	or := &fakeOracle{hdr: &blockoracle.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(8, 4, or)
	got, err := s.Decide(context.Background(), heightsync.DecideHints{
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

func TestAnchorScheduler_CadenceSwallowTail(t *testing.T) {
	or := &fakeOracle{hdr: &blockoracle.Header{Height: 1, ChainID: "gonka", BlockHash: []byte{0x01}}}
	s := heightsync.MustNewAnchorScheduler(8, 4, or)
	got, err := s.Decide(context.Background(), heightsync.DecideHints{
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
