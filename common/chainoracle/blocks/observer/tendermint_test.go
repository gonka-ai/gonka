package observer

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

type stubHeaderRPC struct {
	res   *ctypes.ResultHeader
	err   error
	calls atomic.Int64
}

func (s *stubHeaderRPC) Header(context.Context, *int64) (*ctypes.ResultHeader, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return s.res, nil
}

func testResultHeader(t *testing.T, height int64) *ctypes.ResultHeader {
	t.Helper()
	block := cmttypes.MakeBlock(height, nil, nil, nil)
	block.Header.ChainID = "gonka-test"
	block.Header.Time = time.Unix(1_700_000_000, 0).UTC()
	block.Header.ValidatorsHash = bytes.Repeat([]byte{0xab}, 32)
	return &ctypes.ResultHeader{Header: &block.Header}
}

func TestOracle_AtUsesCacheThenHeaderRPC(t *testing.T) {
	res := testResultHeader(t, 7)
	stub := &stubHeaderRPC{res: res}
	o := newOracle(stub)

	h, err := o.At(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, int64(1), stub.calls.Load())

	h, err = o.At(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), h.Height)
	require.Equal(t, int64(1), stub.calls.Load(), "Remember must cache At() results")
}

func TestOracle_ObserveFeedsAtWithoutRPC(t *testing.T) {
	stub := &stubHeaderRPC{err: errors.New("rpc must not be called")}
	o := newOracle(stub)
	want := blocks.HashOnlyHeader(4, time.Unix(1, 0).UTC(), "gonka-test", []byte{4})
	o.Observe(want)

	h, err := o.At(context.Background(), 4)
	require.NoError(t, err)
	require.Equal(t, []byte{4}, h.BlockHash)
	require.Equal(t, int64(0), stub.calls.Load())
}

func TestOracle_AtTransportErrorNotNotFound(t *testing.T) {
	stub := &stubHeaderRPC{err: errors.New("dial tcp: i/o timeout")}
	o := newOracle(stub)
	_, err := o.At(context.Background(), 3)
	require.Error(t, err)
	require.False(t, errors.Is(err, blocks.ErrHeaderNotFound))
}

func TestOracle_AtCometHeightErrIsNotFound(t *testing.T) {
	stub := &stubHeaderRPC{err: errors.New("height 3 is not available, lowest height is 10")}
	o := newOracle(stub)
	_, err := o.At(context.Background(), 3)
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)
}

func TestOracle_ObserveHex(t *testing.T) {
	o := newOracle(&stubHeaderRPC{})
	require.NoError(t, o.ObserveHex(5, "0a0b", time.Unix(2, 0).UTC(), "gonka-test"))
	h, err := o.At(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, []byte{0x0a, 0x0b}, h.BlockHash)
}
