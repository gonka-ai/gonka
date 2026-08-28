package heightsync

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestFloorIndexFromProtoNilIsMissingBlob(t *testing.T) {
	got, err := FloorIndexFromProto(FloorConfig{}, nil)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestFloorIndexFromProtoEmptyIsValidFold(t *testing.T) {
	got, err := FloorIndexFromProto(FloorConfig{}, &types.FloorIndexProto{})
	require.NoError(t, err)
	require.NotNil(t, got)
	h, hash, known := got.AsOf(1)
	require.True(t, known)
	require.Zero(t, h)
	require.Empty(t, hash)
	require.False(t, got.truncated)
}

func TestFloorIndexProtoRoundTrip(t *testing.T) {
	cfg := FloorConfigFor(3, DefaultHeartbeatConfig())
	f := NewFloorIndexWith(cfg)
	hash := []byte{0xaa, 0xbb}
	f.Observe(1, []FloorClaim{{Signer: 0, Height: 50, Hash: hash}})
	f.Observe(2, []FloorClaim{{Signer: 1, Height: 50, Hash: hash}})

	p := f.ToProto()
	require.NotNil(t, p)
	require.Len(t, p.Entries, 1)
	require.Len(t, p.Claims, 2)
	require.False(t, p.Truncated)

	got, err := FloorIndexFromProto(cfg, p)
	require.NoError(t, err)
	require.NotNil(t, got)

	wantH, wantHash, wantKnown := f.AsOf(3)
	gotH, gotHash, gotKnown := got.AsOf(3)
	require.Equal(t, wantKnown, gotKnown)
	require.Equal(t, wantH, gotH)
	require.Equal(t, wantHash, gotHash)
	require.Equal(t, f.truncated, got.truncated)
	require.Equal(t, f.Len(), got.Len())
	require.Equal(t, f.cfg, got.cfg)

	// Restore must not alias the live index's hash backing arrays.
	got.Observe(3, []FloorClaim{{Signer: 2, Height: 60, Hash: []byte{0xcc}}})
	liveH, _, _ := f.AsOf(4)
	require.Equal(t, uint64(50), liveH)
}

// TestFloorIndexProtoLongFoldRoundTrip exercises a fold long enough to overflow
// the retention window, so the validator has to accept a real truncated suffix
// rather than only the two-entry shapes the other tests build.
func TestFloorIndexProtoLongFoldRoundTrip(t *testing.T) {
	cfg := FloorConfigFor(3, DefaultHeartbeatConfig())
	cfg.Window = 8
	f := NewFloorIndexWith(cfg)
	for i := uint64(1); i <= 40; i++ {
		h := i * 2
		f.Observe(i, []FloorClaim{{Signer: 0, Height: h, Hash: []byte{byte(i)}}})
	}
	require.True(t, f.truncated)
	require.Equal(t, 8, f.Len())

	got, err := FloorIndexFromProto(cfg, f.ToProto())
	require.NoError(t, err)
	for _, m := range []uint64{1, 20, 39, 41} {
		wantH, wantHash, wantKnown := f.AsOf(m)
		gotH, gotHash, gotKnown := got.AsOf(m)
		require.Equal(t, wantKnown, gotKnown, "AsOf(%d)", m)
		require.Equal(t, wantH, gotH, "AsOf(%d)", m)
		require.Equal(t, wantHash, gotHash, "AsOf(%d)", m)
	}
}

func TestFloorIndexProtoTruncatedRoundTrip(t *testing.T) {
	p := &types.FloorIndexProto{
		Truncated: true,
		Entries: []*types.FloorIndexEntryProto{{
			Nonce:  100,
			Height: 50,
			Hash:   []byte{0x01},
			Author: 0,
		}},
	}
	got, err := FloorIndexFromProto(FloorConfig{}, p)
	require.NoError(t, err)
	require.True(t, got.truncated)

	_, _, known := got.AsOf(1)
	require.False(t, known, "nonce before the retained window must stay unknown")

	h, hash, known := got.AsOf(101)
	require.True(t, known)
	require.Equal(t, uint64(50), h)
	require.Equal(t, []byte{0x01}, hash)

	require.True(t, got.ToProto().Truncated)
}

// TestFloorIndexFromProtoRejectsUnfoldableBlobs covers the shapes that no diff
// sequence can produce. AsOf binary-searches entries and reads the last one as a
// running maximum, so accepting any of these would answer F(m) with a height the
// log never established — silently, and only on the replica that restored.
func TestFloorIndexFromProtoRejectsUnfoldableBlobs(t *testing.T) {
	entry := func(nonce, height uint64) *types.FloorIndexEntryProto {
		return &types.FloorIndexEntryProto{Nonce: nonce, Height: height, Hash: []byte{0x01}}
	}

	cases := map[string]*types.FloorIndexProto{
		"nonce out of order": {Entries: []*types.FloorIndexEntryProto{entry(5, 10), entry(3, 20)}},
		"repeated nonce":     {Entries: []*types.FloorIndexEntryProto{entry(5, 10), entry(5, 20)}},
		"height regresses":   {Entries: []*types.FloorIndexEntryProto{entry(5, 20), entry(6, 10)}},
		"height repeats":     {Entries: []*types.FloorIndexEntryProto{entry(5, 20), entry(6, 20)}},
		"zero height":        {Entries: []*types.FloorIndexEntryProto{entry(5, 0)}},
		"missing hash": {Entries: []*types.FloorIndexEntryProto{
			{Nonce: 5, Height: 10},
		}},
		"nil entry": {Entries: []*types.FloorIndexEntryProto{nil}},
		"nil claim": {Claims: []*types.FloorSignerClaimProto{nil}},
		"claim without stamp": {Claims: []*types.FloorSignerClaimProto{
			{Signer: 0, Height: 10},
		}},
		"repeated signer": {Claims: []*types.FloorSignerClaimProto{
			{Signer: 0, Height: 10, Hash: []byte{0x01}},
			{Signer: 0, Height: 20, Hash: []byte{0x02}},
		}},
	}

	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := FloorIndexFromProto(FloorConfig{}, p)
			require.ErrorIs(t, err, ErrFloorBlobInvalid)
			require.Nil(t, got)
		})
	}
}
