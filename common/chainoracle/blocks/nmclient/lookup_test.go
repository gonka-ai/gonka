package nmclient_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/nmclient"
	"common/chainoracle/blocks/nmrpc"
	"common/nodemanager/gen"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type stubOracle struct {
	hdr   *blocks.Header
	proof *blocks.Proof
	prove error
	atErr error
}

func (s stubOracle) Latest(context.Context) (*blocks.Header, error) {
	if s.atErr != nil {
		return nil, s.atErr
	}
	return s.hdr, nil
}
func (s stubOracle) At(context.Context, int64) (*blocks.Header, error) {
	if s.atErr != nil {
		return nil, s.atErr
	}
	if s.hdr == nil {
		return nil, blocks.ErrHeaderNotFound
	}
	return s.hdr, nil
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

type nmServer struct {
	gen.UnimplementedNodeManagerServer
	oracle blocks.BlockOracle
}

func (s *nmServer) GetBlockHeader(ctx context.Context, req *gen.GetBlockHeaderRequest) (*gen.GetBlockHeaderResponse, error) {
	return nmrpc.GetBlockHeader(ctx, s.oracle, req)
}
func (s *nmServer) ProveBlockPath(ctx context.Context, req *gen.ProveBlockPathRequest) (*gen.ProveBlockPathResponse, error) {
	return nmrpc.ProveBlockPath(ctx, s.oracle, req)
}

func dialNM(t *testing.T, srv gen.NodeManagerServer) gen.NodeManagerClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	gs := grpc.NewServer()
	gen.RegisterNodeManagerServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return gen.NewNodeManagerClient(conn)
}

func TestLookup_AtProveAndUnimplemented(t *testing.T) {
	hdr := blocks.HashOnlyHeader(3, time.Unix(9, 0).UTC(), "gonka-test", []byte{3})
	proof := &blocks.Proof{Path: "/escrow/1", Value: []byte{9}}
	client := dialNM(t, &nmServer{oracle: stubOracle{hdr: hdr, proof: proof}})
	lookup, err := nmclient.New(client)
	require.NoError(t, err)

	got, err := lookup.At(context.Background(), 3)
	require.NoError(t, err)
	require.Equal(t, int64(3), got.Height)
	require.Equal(t, []byte{3}, got.BlockHash)

	latest, err := lookup.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(3), latest.Height)

	p, err := lookup.Prove(context.Background(), "/escrow/1", 3)
	require.NoError(t, err)
	require.Equal(t, proof.Path, p.Path)
	require.Equal(t, proof.Value, p.Value)

	empty := dialNM(t, &nmServer{oracle: stubOracle{prove: blocks.ErrProveNotImplemented, hdr: hdr}})
	elookup, err := nmclient.New(empty)
	require.NoError(t, err)
	_, err = elookup.Prove(context.Background(), "/escrow/1", 3)
	require.ErrorIs(t, err, blocks.ErrProveNotImplemented)
}

// An old dapi has neither RPC. A header caller must see the same quiet miss as
// HTTP 404; a Prove caller must see "no proof", not "no header".
func TestLookup_OldServerUnimplemented(t *testing.T) {
	client := dialNM(t, &gen.UnimplementedNodeManagerServer{})
	lookup, err := nmclient.New(client)
	require.NoError(t, err)

	_, err = lookup.At(context.Background(), 1)
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)

	_, err = lookup.Latest(context.Background())
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)

	_, err = lookup.Prove(context.Background(), "/escrow/1", 1)
	require.ErrorIs(t, err, blocks.ErrProveNotImplemented)
}

// A dapi that is up but has no oracle configured is a miss, not a transport error.
func TestLookup_NoOracleConfiguredIsQuietMiss(t *testing.T) {
	client := dialNM(t, &nmServer{oracle: nil})
	lookup, err := nmclient.New(client)
	require.NoError(t, err)

	_, err = lookup.At(context.Background(), 1)
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)

	_, err = lookup.Prove(context.Background(), "/escrow/1", 1)
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)
}

// A transport / upstream failure must not be laundered into a quiet miss:
// failover has to tell "no route" from "this dapi is up, its RPC failed".
func TestLookup_TransportErrorIsNotAMiss(t *testing.T) {
	hdr := blocks.HashOnlyHeader(1, time.Unix(1, 0).UTC(), "gonka-test", []byte{1})
	client := dialNM(t, &nmServer{oracle: stubOracle{hdr: hdr, atErr: errors.New("i/o timeout")}})
	lookup, err := nmclient.New(client)
	require.NoError(t, err)

	_, err = lookup.At(context.Background(), 1)
	require.Error(t, err)
	require.NotErrorIs(t, err, blocks.ErrHeaderNotFound)
	require.Equal(t, codes.Unavailable, status.Code(err))
}
