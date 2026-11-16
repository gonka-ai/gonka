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

type batchRPC struct {
	tip        int64
	tamper     int64
	headerCall atomic.Int64
	batchCalls atomic.Int64
}

func batchHeader(height int64) cmttypes.Header {
	block := cmttypes.MakeBlock(height, nil, nil, nil)
	block.Header.ChainID = "gonka-test"
	block.Header.Time = time.Unix(1_700_000_000+height*5, 0).UTC()
	block.Header.ValidatorsHash = bytes.Repeat([]byte{0xab}, 32)
	return block.Header
}

func (r *batchRPC) Header(_ context.Context, height *int64) (*ctypes.ResultHeader, error) {
	r.headerCall.Add(1)
	if height == nil || *height > r.tip {
		return nil, errors.New("height 9999 must be less than or equal to the current blockchain height")
	}
	h := batchHeader(*height)
	return &ctypes.ResultHeader{Header: &h}, nil
}

func (r *batchRPC) BlockchainInfo(_ context.Context, minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error) {
	r.batchCalls.Add(1)
	if maxHeight > r.tip {
		maxHeight = r.tip
	}
	if minHeight > maxHeight {
		return nil, errors.New("min height can't be greater than max height")
	}
	res := &ctypes.ResultBlockchainInfo{LastHeight: r.tip}
	for h := maxHeight; h >= minHeight; h-- {
		hdr := batchHeader(h)
		id := cmttypes.BlockID{Hash: hdr.Hash()}
		if h == r.tamper {
			hdr.Time = hdr.Time.Add(time.Hour)
		}
		res.BlockMetas = append(res.BlockMetas, &cmttypes.BlockMeta{BlockID: id, Header: hdr})
	}
	return res, nil
}

func TestAtBackfillsOldHeightsInBatches(t *testing.T) {
	rpc := &batchRPC{tip: 1000}
	o := newOracle(rpc)

	h, err := o.At(context.Background(), 500)
	require.NoError(t, err)
	want := batchHeader(500)
	require.Equal(t, want.Hash().Bytes(), h.BlockHash)
	require.True(t, want.Time.Equal(h.Time))

	for height := int64(501); height < 500+BlockchainInfoBatch; height++ {
		_, err := o.At(context.Background(), height)
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), rpc.batchCalls.Load(), "one call covers the batch")
	require.Zero(t, rpc.headerCall.Load())
	require.Equal(t, BlockchainInfoBatch, o.history.Len())
}

func TestAtRefusesABatchWhoseHeaderDoesNotHashToItsBlockID(t *testing.T) {
	rpc := &batchRPC{tip: 1000, tamper: 505}
	o := newOracle(rpc)

	h, err := o.At(context.Background(), 500)
	require.NoError(t, err)
	require.Equal(t, int64(500), h.Height)
	_, ok := o.history.Get(505)
	require.False(t, ok, "a header whose time was changed is never stored")
	_, ok = o.history.Get(506)
	require.True(t, ok, "the rest of the batch is kept")
}

func TestAtAboveTheTipIsNotFound(t *testing.T) {
	o := newOracle(&batchRPC{tip: 1000})
	_, err := o.At(context.Background(), 1001)
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)
}

func TestHistoryKeepsTheWindowBelowTheTip(t *testing.T) {
	hist := NewHistory(10)
	at := time.Unix(1_700_000_000, 0)
	for height := int64(1); height <= 25; height++ {
		hist.Put(blocks.HashOnlyHeader(height, at, "c", []byte{byte(height)}))
	}
	_, ok := hist.Get(15)
	require.False(t, ok)
	h, ok := hist.Get(16)
	require.True(t, ok)
	require.Equal(t, "c", h.ChainID)
	require.LessOrEqual(t, hist.Len(), 11)
}
