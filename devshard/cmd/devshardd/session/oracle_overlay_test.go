package session

import (
	"context"
	"errors"
	"testing"

	"common/chainoracle/blocks"

	"github.com/stretchr/testify/require"
)

type stubHeaderOracle struct {
	hdr *blocks.Header
}

func (o *stubHeaderOracle) Latest(context.Context) (*blocks.Header, error) {
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}

func (o *stubHeaderOracle) At(_ context.Context, height int64) (*blocks.Header, error) {
	if o.hdr != nil && height == o.hdr.Height {
		return o.Latest(context.Background())
	}
	return nil, errors.New("no header at height")
}

func (o *stubHeaderOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *stubHeaderOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func TestParseOracleOverlay(t *testing.T) {
	d, fab := parseOracleOverlay("-20", "true")
	require.Equal(t, int64(-20), d)
	require.True(t, fab)
	d, fab = parseOracleOverlay("10", "on")
	require.Equal(t, int64(10), d)
	require.True(t, fab)
	d, fab = parseOracleOverlay("", "")
	require.Zero(t, d)
	require.False(t, fab)
	d, fab = parseOracleOverlay("nope", "maybe")
	require.Zero(t, d)
	require.False(t, fab)
}

func TestWrapTestenvOracleOverlayShiftsLatestLeavesAtCanonical(t *testing.T) {
	canon := []byte{0xaa, 0xbb, 0xcc}
	inner := &stubHeaderOracle{hdr: &blocks.Header{Height: 50, BlockHash: append([]byte(nil), canon...)}}
	wrapped := wrapTestenvOracleOverlay(inner, 10, true)
	require.NotSame(t, inner, wrapped)

	got, err := wrapped.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(60), got.Height)
	require.Equal(t, byte(0xaa^0xff), got.BlockHash[0])
	require.Equal(t, canon[1], got.BlockHash[1], "only the first hash byte is flipped")

	at, err := wrapped.At(context.Background(), 50)
	require.NoError(t, err)
	require.Equal(t, int64(50), at.Height)
	require.Equal(t, canon, at.BlockHash, "At stays canonical so L6 can reconcile")
}

func TestWrapTestenvOracleOverlayNoop(t *testing.T) {
	inner := &stubHeaderOracle{hdr: &blocks.Header{Height: 3}}
	require.Same(t, inner, wrapTestenvOracleOverlay(inner, 0, false))
}
