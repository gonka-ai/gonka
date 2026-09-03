package direct_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"common/chainoracle/blocks"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"devshard/chainoracle/blocks/direct"
)

type stubGRPC struct {
	hdr *cmtservice.Header
	id  []byte
	err error
}

func (s *stubGRPC) GetLatestBlock(context.Context, *cmtservice.GetLatestBlockRequest, ...grpc.CallOption) (*cmtservice.GetLatestBlockResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &cmtservice.GetLatestBlockResponse{
		BlockId:  &cmtproto.BlockID{Hash: append([]byte(nil), s.id...)},
		SdkBlock: &cmtservice.Block{Header: *s.hdr},
	}, nil
}

func (s *stubGRPC) GetBlockByHeight(ctx context.Context, _ *cmtservice.GetBlockByHeightRequest, opts ...grpc.CallOption) (*cmtservice.GetBlockByHeightResponse, error) {
	resp, err := s.GetLatestBlock(ctx, &cmtservice.GetLatestBlockRequest{}, opts...)
	if err != nil {
		return nil, err
	}
	return &cmtservice.GetBlockByHeightResponse{BlockId: resp.BlockId, SdkBlock: resp.SdkBlock}, nil
}

type recRPC struct {
	hdr   *blocks.Header
	calls int
}

func (r *recRPC) Latest(context.Context) (*blocks.Header, error) {
	r.calls++
	if r.hdr == nil {
		return nil, errors.New("rpc empty")
	}
	cp := *r.hdr
	return &cp, nil
}
func (r *recRPC) At(ctx context.Context, _ int64) (*blocks.Header, error) { return r.Latest(ctx) }

func TestDirectChainOracle_PrefersGRPC_FallsBackToRPC(t *testing.T) {
	grpcHdr := &cmtservice.Header{
		ChainID: "gonka-test",
		Height:  11,
		Time:    time.Unix(1_700_000_000, 0).UTC(),
	}
	grpcHash := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	rpcHdr := blocks.HashOnlyHeader(22, time.Unix(1_700_000_001, 0).UTC(), "gonka-test", []byte{0x11, 0x22})

	primary := direct.NewGRPCFetcher(&stubGRPC{hdr: grpcHdr, id: grpcHash})
	secondary := &recRPC{hdr: rpcHdr}
	o := direct.New(primary, secondary)

	h, err := o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(11), h.Height)
	require.Equal(t, grpcHash, h.BlockHash)
	require.Empty(t, h.Commit.Signatures)
	require.Equal(t, 0, secondary.calls, "RPC must not run when gRPC succeeds")

	failing := direct.NewGRPCFetcher(&stubGRPC{err: errors.New("grpc down")})
	o = direct.New(failing, secondary)
	h, err = o.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(22), h.Height)
	require.Equal(t, 1, secondary.calls)
	require.Empty(t, h.Commit.Signatures)
}

func TestRPCFetcher_ParsesCometJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/block", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"block_id": map[string]any{"hash": "aabbccdd"},
				"block": map[string]any{
					"header": map[string]any{
						"chain_id": "gonka-test",
						"height":   "33",
						"time":     "2023-11-14T22:13:20Z",
					},
				},
			},
		})
	}))
	t.Cleanup(ts.Close)

	h, err := direct.NewRPCFetcher(ts.URL, nil).Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(33), h.Height)
	require.Equal(t, []byte{0xaa, 0xbb, 0xcc, 0xdd}, h.BlockHash)
	require.Empty(t, h.Commit.Signatures)
}
