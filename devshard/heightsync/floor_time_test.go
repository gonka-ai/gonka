package heightsync_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
)

func TestFloorIndex_RaiseStoresTimeMs(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xaa}
	const want int64 = 1_700_000_000_000
	f.Observe(1, []heightsync.FloorClaim{{Signer: 0, Height: 100, Hash: hash, TimeMs: want}})

	p, known := f.PointAsOf(2)
	require.True(t, known)
	require.Equal(t, uint64(100), p.Height)
	require.Equal(t, hash, p.Hash)
	require.Equal(t, want, p.TimeMs)
}

func TestFloorIndex_SameTimeIsStable(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xbb}
	const want int64 = 5_000
	f.Observe(1, []heightsync.FloorClaim{{Signer: 0, Height: 50, Hash: hash, TimeMs: want}})
	f.Observe(2, []heightsync.FloorClaim{{Signer: 1, Height: 50, Hash: hash, TimeMs: want}})

	require.Empty(t, f.DrainTimeMismatches())
	p, known := f.PointAsOf(3)
	require.True(t, known)
	require.Equal(t, want, p.TimeMs)
}

func TestFloorIndex_DisagreeKeepsFirstTime(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xcc}
	f.Observe(1, []heightsync.FloorClaim{{Signer: 0, Height: 50, Hash: hash, TimeMs: 7_000}})
	f.Observe(2, []heightsync.FloorClaim{{Signer: 1, Height: 50, Hash: hash, TimeMs: 9_000}})

	mm := f.DrainTimeMismatches()
	require.Len(t, mm, 1)
	require.Equal(t, uint64(50), mm[0].Height)
	require.Equal(t, int64(7_000), mm[0].Kept)
	require.Equal(t, int64(9_000), mm[0].Got)

	p, known := f.PointAsOf(3)
	require.True(t, known)
	require.Equal(t, int64(7_000), p.TimeMs, "first non-zero wins")
}

func TestFloorIndex_ZeroThenFill(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xdd}
	f.Observe(1, []heightsync.FloorClaim{{Signer: 0, Height: 50, Hash: hash, TimeMs: 0}})
	p, _ := f.PointAsOf(2)
	require.Zero(t, p.TimeMs)

	f.Observe(2, []heightsync.FloorClaim{{Signer: 1, Height: 50, Hash: hash, TimeMs: 4_000}})
	require.Empty(t, f.DrainTimeMismatches())
	p, known := f.PointAsOf(3)
	require.True(t, known)
	require.Equal(t, int64(4_000), p.TimeMs, "first non-zero fills a legacy zero")
}

func TestFloorIndex_LegacyStampsLeaveTimeZero(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xee}
	f.Observe(1, []heightsync.FloorClaim{claim(0, 10, hash)})
	p, known := f.PointAsOf(2)
	require.True(t, known)
	require.Zero(t, p.TimeMs)
}
