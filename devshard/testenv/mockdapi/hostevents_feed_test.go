package mockdapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"common/nodemanager/gen"
	"devshard/testenv/mockchain/adminface"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockdapi"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	gogoproto "github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestHostEvents_BlockMatchesSignedChain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st := seed.Defaults()

	_, rpcURL, rpcCleanup, err := rpcface.NewInProcessServer(st, rpcface.Config{BlockInterval: 50 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(rpcCleanup)

	grpcSrv, grpcLis, err := grpcface.NewInProcessServer(grpcface.Deps{Store: st})
	require.NoError(t, err)
	t.Cleanup(func() {
		grpcSrv.Stop()
		_ = grpcLis.Close()
	})

	admin := adminface.NewServer(st, nil, nil)
	adminHTTP := httptest.NewServer(admin.Handler())
	t.Cleanup(adminHTTP.Close)

	cfg := mockdapi.DefaultConfig()
	cfg.ChainGRPCAddr = grpcLis.Addr().String()
	cfg.ChainTestenvURL = adminHTTP.URL
	cfg.ChainRPCAddr = rpcURL
	cfg.ChainPollInterval = time.Hour
	cfg.BlockInterval = 50 * time.Millisecond

	svc, err := mockdapi.New(ctx, cfg)
	require.NoError(t, err)

	grpcL, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	httpL, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = svc.RunOn(ctx, grpcL, httpL) }()

	conn, err := grpc.NewClient(grpcL.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := gen.NewNodeManagerClient(conn)

	var payload *gen.BlockPayload
	require.Eventually(t, func() bool {
		resp, err := client.GetHostEvents(ctx, &gen.GetHostEventsRequest{
			Cursor:         0,
			MaxWaitSeconds: 1,
			Subscribe:      []gen.HostEventKind{gen.HostEventKind_HOST_EVENT_KIND_BLOCK},
		})
		if err != nil || len(resp.Events) == 0 {
			return false
		}
		payload = resp.Events[0].GetBlock()
		return payload != nil && payload.GetHeight() > 150 && len(payload.GetHeader()) > 0
	}, 5*time.Second, 50*time.Millisecond)
	require.NotNil(t, payload)

	cmt, err := rpchttp.New(rpcURL, "/websocket")
	require.NoError(t, err)
	h := payload.GetHeight()
	hdr, err := cmt.Header(ctx, &h)
	require.NoError(t, err)
	raw, err := gogoproto.Marshal(hdr.Header.ToProto())
	require.NoError(t, err)
	require.Equal(t, raw, payload.GetHeader())
	require.True(t, bytes.Equal(hdr.Header.Hash(), payload.GetHash()))
}

func TestHostEvents_AdminRoutes(t *testing.T) {
	bed := startBed(t)
	t.Cleanup(bed.cleanup)

	conn, err := grpc.NewClient(bed.grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := gen.NewNodeManagerClient(conn)
	ctx := context.Background()
	sub := []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
	}

	postJSON := func(path string, body any) {
		t.Helper()
		b, err := json.Marshal(body)
		require.NoError(t, err)
		resp, err := http.Post(bed.httpURL+path, "application/json", bytes.NewReader(b))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		_ = resp.Body.Close()
	}

	postJSON("/testenv/host-events/escrow-created", mockdapi.EscrowCreatedRequest{EscrowID: 44})
	got, err := client.GetHostEvents(ctx, &gen.GetHostEventsRequest{Cursor: 0, Subscribe: sub})
	require.NoError(t, err)
	require.Len(t, got.Events, 1)
	require.Equal(t, gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED, got.Events[0].GetKind())
	require.Equal(t, uint64(44), got.Events[0].GetEscrow().GetEscrowId())
	cursor := got.NextCursor
	genID := got.Generation

	postJSON("/testenv/host-events/warm-key", mockdapi.WarmKeyRequest{Granter: "g", Grantee: "w", GrantID: 1, Height: 8})
	postJSON("/testenv/host-events/block", mockdapi.BlockAppendRequest{
		Height: 99, Hash: []byte{9, 9}, HeaderTimeUnixMs: 99_000, Header: []byte{1, 2, 3},
	})
	got, err = client.GetHostEvents(ctx, &gen.GetHostEventsRequest{Cursor: cursor, Subscribe: sub})
	require.NoError(t, err)
	require.Len(t, got.Events, 2)
	require.Equal(t, gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED, got.Events[0].GetKind())
	require.Equal(t, gen.HostEventKind_HOST_EVENT_KIND_BLOCK, got.Events[1].GetKind())
	require.Equal(t, []byte{9, 9}, got.Events[1].GetBlock().GetHash())
	require.Equal(t, []byte{1, 2, 3}, got.Events[1].GetBlock().GetHeader())
	cursor = got.NextCursor

	postJSON("/testenv/host-events/stall", mockdapi.StallRequest{Stalled: true})
	_, err = client.GetHostEvents(ctx, &gen.GetHostEventsRequest{Cursor: cursor, Subscribe: sub})
	require.Equal(t, codes.Unavailable, status.Code(err))
	postJSON("/testenv/host-events/stall", mockdapi.StallRequest{Stalled: false})
	cleared, err := client.GetHostEvents(ctx, &gen.GetHostEventsRequest{Cursor: cursor, Subscribe: sub})
	require.NoError(t, err)
	require.Equal(t, genID, cleared.Generation)

	postJSON("/testenv/host-events/generation", map[string]any{})
	reset, err := client.GetHostEvents(ctx, &gen.GetHostEventsRequest{
		Cursor: 0, AfterHeight: 98, Subscribe: sub,
	})
	require.NoError(t, err)
	require.True(t, reset.NeedsReset)
	follow, err := client.GetHostEvents(ctx, &gen.GetHostEventsRequest{
		Cursor: reset.NextCursor, AfterHeight: 0, Generation: reset.Generation, Subscribe: sub,
	})
	require.NoError(t, err)
	require.False(t, follow.NeedsReset)
}
