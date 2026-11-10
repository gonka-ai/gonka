package nodemanager

import (
	"context"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/nodemanager/gen"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type blockStub struct {
	hdr   *blocks.Header
	proof *blocks.Proof
	prove error
}

func (s blockStub) Latest(context.Context) (*blocks.Header, error) { return s.hdr, nil }
func (s blockStub) At(context.Context, int64) (*blocks.Header, error) {
	if s.hdr == nil {
		return nil, blocks.ErrHeaderNotFound
	}
	return s.hdr, nil
}
func (s blockStub) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	if s.prove != nil {
		return nil, s.prove
	}
	return s.proof, nil
}
func (blockStub) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func TestGetBlockHeader_UsesSharedOracle(t *testing.T) {
	hdr := blocks.HashOnlyHeader(12, time.Unix(50, 0).UTC(), "gonka-test", []byte{0xab})
	srv := NewServer(nil, nil, nil, WithBlockOracle(blockStub{hdr: hdr}))

	resp, err := srv.GetBlockHeader(context.Background(), &gen.GetBlockHeaderRequest{Height: 12})
	require.NoError(t, err)
	require.Equal(t, int64(12), resp.Header.Height)
	require.Equal(t, []byte{0xab}, resp.Header.BlockHash)
	require.Equal(t, "gonka-test", resp.Header.ChainId)
}

func TestGetBlockHeader_NilOracleFailedPrecondition(t *testing.T) {
	srv := NewServer(nil, nil, nil)
	_, err := srv.GetBlockHeader(context.Background(), &gen.GetBlockHeaderRequest{Height: 1})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestProveBlockPath_UnimplementedOnHashOnly(t *testing.T) {
	hdr := blocks.HashOnlyHeader(1, time.Unix(1, 0).UTC(), "gonka-test", []byte{1})
	srv := NewServer(nil, nil, nil, WithBlockOracle(blockStub{hdr: hdr, prove: blocks.ErrProveNotImplemented}))
	_, err := srv.ProveBlockPath(context.Background(), &gen.ProveBlockPathRequest{Height: 1, Path: "/escrow/1"})
	require.Equal(t, codes.Unimplemented, status.Code(err))
}

func TestProveBlockPath_ReturnsOracleProof(t *testing.T) {
	proof := &blocks.Proof{Path: "/escrow/1", Value: []byte{1, 2}, Ops: [][]byte{{3}}}
	srv := NewServer(nil, nil, nil, WithBlockOracle(blockStub{
		hdr:   blocks.HashOnlyHeader(1, time.Unix(1, 0).UTC(), "gonka-test", []byte{1}),
		proof: proof,
	}))
	resp, err := srv.ProveBlockPath(context.Background(), &gen.ProveBlockPathRequest{Height: 1, Path: "/escrow/1"})
	require.NoError(t, err)
	require.Equal(t, proof.Path, resp.Proof.Path)
	require.Equal(t, proof.Value, resp.Proof.Value)
	require.Equal(t, proof.Ops, resp.Proof.Ops)
}
