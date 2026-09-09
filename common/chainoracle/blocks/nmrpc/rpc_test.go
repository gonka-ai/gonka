package nmrpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/nodemanager/gen"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type stubOracle struct {
	latest *blocks.Header
	at     map[int64]*blocks.Header
	atErr  error
	proof  *blocks.Proof
	prove  error
}

func (s stubOracle) Latest(context.Context) (*blocks.Header, error) {
	if s.latest == nil {
		return nil, blocks.ErrHeaderNotFound
	}
	return s.latest, nil
}
func (s stubOracle) At(_ context.Context, height int64) (*blocks.Header, error) {
	if s.atErr != nil {
		return nil, s.atErr
	}
	h, ok := s.at[height]
	if !ok {
		return nil, blocks.ErrHeaderNotFound
	}
	return h, nil
}
func (s stubOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	if s.prove != nil {
		return nil, s.prove
	}
	return s.proof, nil
}
func (stubOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func TestGetBlockHeader_AtAndLatest(t *testing.T) {
	hdr := blocks.HashOnlyHeader(4, time.Unix(10, 0).UTC(), "gonka-test", []byte{4})
	o := stubOracle{latest: hdr, at: map[int64]*blocks.Header{4: hdr}}

	got, err := GetBlockHeader(context.Background(), o, &gen.GetBlockHeaderRequest{Height: 4})
	require.NoError(t, err)
	require.Equal(t, int64(4), got.Header.Height)
	require.Equal(t, []byte{4}, got.Header.BlockHash)
	require.Equal(t, "gonka-test", got.Header.ChainId)

	latest, err := GetBlockHeader(context.Background(), o, &gen.GetBlockHeaderRequest{Height: 0})
	require.NoError(t, err)
	require.Equal(t, int64(4), latest.Header.Height)
}

func TestGetBlockHeader_StatusCodes(t *testing.T) {
	_, err := GetBlockHeader(context.Background(), nil, &gen.GetBlockHeaderRequest{Height: 1})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	_, err = GetBlockHeader(context.Background(), stubOracle{}, &gen.GetBlockHeaderRequest{Height: -1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = GetBlockHeader(context.Background(), stubOracle{}, &gen.GetBlockHeaderRequest{Height: 9})
	require.Equal(t, codes.NotFound, status.Code(err))

	_, err = GetBlockHeader(context.Background(), stubOracle{atErr: errors.New("i/o timeout")}, &gen.GetBlockHeaderRequest{Height: 1})
	require.Equal(t, codes.Unavailable, status.Code(err))
}

func TestProveBlockPath_MockProofAndUnimplemented(t *testing.T) {
	want := &blocks.Proof{Path: "/escrow/1", Value: []byte{1}, Ops: [][]byte{{2}}}
	got, err := ProveBlockPath(context.Background(), stubOracle{proof: want}, &gen.ProveBlockPathRequest{Height: 4, Path: "/escrow/1"})
	require.NoError(t, err)
	require.Equal(t, "/escrow/1", got.Proof.Path)
	require.Equal(t, []byte{1}, got.Proof.Value)
	require.Equal(t, [][]byte{{2}}, got.Proof.Ops)

	_, err = ProveBlockPath(context.Background(), stubOracle{prove: blocks.ErrProveNotImplemented}, &gen.ProveBlockPathRequest{Height: 4, Path: "/escrow/1"})
	require.Equal(t, codes.Unimplemented, status.Code(err))

	_, err = ProveBlockPath(context.Background(), stubOracle{}, &gen.ProveBlockPathRequest{Height: 4})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestHeaderProofRoundTrip(t *testing.T) {
	h := &blocks.Header{
		Height:             8,
		Time:               time.Unix(1_700_000_000, 5).UTC(),
		ChainID:            "gonka-test",
		BlockHash:          []byte{1, 2, 3},
		AppHash:            []byte{4},
		ValidatorsHash:     []byte{5},
		NextValidatorsHash: []byte{6},
		Commit: blocks.Commit{
			Height:  8,
			Round:   1,
			BlockID: []byte{7},
			Signatures: []blocks.CommitSig{{
				ValidatorAddress: []byte{8},
				Timestamp:        time.Unix(1_700_000_001, 0).UTC(),
				Signature:        []byte{9},
			}},
		},
	}
	got := HeaderFromProto(HeaderToProto(h))
	require.Equal(t, h, got)

	p := &blocks.Proof{Path: "/k", Value: []byte{1}, Ops: [][]byte{{2, 3}}}
	require.Equal(t, p, ProofFromProto(ProofToProto(p)))
}
