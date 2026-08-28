package heightsync

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestFloorIndexFromProtoNilIsMissingBlob(t *testing.T) {
	require.Nil(t, FloorIndexFromProto(FloorConfig{}, nil))
}

func TestFloorIndexFromProtoEmptyIsValidFold(t *testing.T) {
	got := FloorIndexFromProto(FloorConfig{}, &types.FloorIndexProto{})
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

	got := FloorIndexFromProto(cfg, p)
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
	got := FloorIndexFromProto(FloorConfig{}, p)
	require.True(t, got.truncated)

	_, _, known := got.AsOf(1)
	require.False(t, known, "nonce before the retained window must stay unknown")

	h, hash, known := got.AsOf(101)
	require.True(t, known)
	require.Equal(t, uint64(50), h)
	require.Equal(t, []byte{0x01}, hash)

	require.True(t, got.ToProto().Truncated)
}
