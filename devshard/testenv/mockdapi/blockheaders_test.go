package mockdapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/nodemanager/gen"
	"devshard/chainoracle/blocks/observer"
	"devshard/testenv/mockdapi"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestMockDAPI_LPA4a_CatchUpAndClamp(t *testing.T) {
	bed := startBedFrozen(t)
	t.Cleanup(bed.cleanup)

	h := int64(200_000)
	seedMockWindow(t, bed.svc.BlockOracle(), h)

	conn, err := grpc.NewClient(bed.grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := gen.NewNodeManagerClient(conn)

	batch, err := client.GetBlockHeaders(context.Background(), &gen.GetBlockHeadersRequest{
		FromHeight: h - 2000,
	})
	require.NoError(t, err)
	require.Len(t, batch.Headers, blocks.MaxHeadersPerPoll)
	require.Equal(t, h-1999, batch.Headers[0].Height)
	require.Equal(t, h-1000, batch.Headers[len(batch.Headers)-1].Height)
	require.Equal(t, h-1000, batch.NextFromHeight)

	oldest := blocks.OldestHeight(h)
	clamped, err := client.GetBlockHeaders(context.Background(), &gen.GetBlockHeadersRequest{
		FromHeight: h - blocks.HistoryWindow - 1,
	})
	require.NoError(t, err)
	require.Equal(t, oldest, clamped.Headers[0].Height)
}

func TestMockDAPI_LPA4b_HTTPMatchesUnaryAndLongPoll(t *testing.T) {
	bed := startBedFrozen(t)
	t.Cleanup(bed.cleanup)

	h := int64(200_000)
	mock := bed.svc.BlockOracle()
	seedMockWindow(t, mock, h)
	want, err := mock.At(context.Background(), h-1000)
	require.NoError(t, err)

	httpResp, err := http.Get(bed.httpURL + "/block/199000")
	require.NoError(t, err)
	defer httpResp.Body.Close()
	require.Equal(t, http.StatusOK, httpResp.StatusCode)
	var httpHdr blocks.Header
	require.NoError(t, json.NewDecoder(httpResp.Body).Decode(&httpHdr))
	require.Equal(t, want.Height, httpHdr.Height)
	require.Equal(t, want.BlockHash, httpHdr.BlockHash)

	conn, err := grpc.NewClient(bed.grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := gen.NewNodeManagerClient(conn)

	unary, err := client.GetBlockHeader(context.Background(), &gen.GetBlockHeaderRequest{Height: h - 1000})
	require.NoError(t, err)
	require.Equal(t, want.BlockHash, unary.Header.BlockHash)

	poll, err := client.GetBlockHeaders(context.Background(), &gen.GetBlockHeadersRequest{
		FromHeight: h - 2000,
	})
	require.NoError(t, err)
	found := false
	for _, hdr := range poll.Headers {
		if hdr.Height == h-1000 {
			require.Equal(t, want.BlockHash, hdr.BlockHash)
			found = true
			break
		}
	}
	require.True(t, found, "long-poll batch should include height %d", h-1000)
}

func TestMockDAPI_LPA4c_AdvanceOneWakesLongPoll(t *testing.T) {
	bed := startBedFrozen(t)
	t.Cleanup(bed.cleanup)

	mock := bed.svc.BlockOracle()
	tip, err := mock.Latest(context.Background())
	require.NoError(t, err)

	conn, err := grpc.NewClient(bed.grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := gen.NewNodeManagerClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan *gen.GetBlockHeadersResponse, 1)
	go func() {
		resp, err := client.GetBlockHeaders(ctx, &gen.GetBlockHeadersRequest{
			FromHeight:     tip.Height,
			MaxWaitSeconds: 5,
		})
		require.NoError(t, err)
		done <- resp
	}()

	time.Sleep(80 * time.Millisecond)
	live, err := mock.AdvanceOne()
	require.NoError(t, err)

	select {
	case resp := <-done:
		require.False(t, resp.Unchanged)
		require.Equal(t, live.Height, resp.Headers[0].Height)
		require.Equal(t, live.BlockHash, resp.Headers[0].BlockHash)
	case <-ctx.Done():
		t.Fatal("long-poll did not wake on AdvanceOne")
	}
}

func TestMockDAPI_LPA4d_OmitBlockRoutesGetBlockHeadersUnimplemented(t *testing.T) {
	bed := startBedOmitBlocks(t)
	t.Cleanup(bed.cleanup)

	conn, err := grpc.NewClient(bed.grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = gen.NewNodeManagerClient(conn).GetBlockHeaders(context.Background(), &gen.GetBlockHeadersRequest{})
	require.Equal(t, codes.Unimplemented, status.Code(err), "old-dapi stand-in must not return an empty success")
}

func startBedFrozen(t *testing.T) testBed {
	t.Helper()
	return startBedWith(t, func(cfg *mockdapi.Config) {
		cfg.BlockInterval = time.Hour
	})
}

func seedMockWindow(t *testing.T, mock *observer.Mock, h int64) {
	t.Helper()
	_, err := mock.AdvanceTo(blocks.OldestHeight(h))
	require.NoError(t, err)
	_, err = mock.AdvanceTo(h - 2000)
	require.NoError(t, err)
	_, err = mock.AdvanceN(2000)
	require.NoError(t, err)
	got, err := mock.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, h, got.Height)
}
