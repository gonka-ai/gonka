package session

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"common/httpguard"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
	"devshard/types"
)

// An execution vote challenges the executor once and sends no diffs.
// A host that has never bound the escrow stays unbound, and the timeout
// still stands when that host has no finish to return.
func TestExecutionTimeout_ColdExecutorStaysUnbound(t *testing.T) {
	const escrowID = "9730"
	const inferenceID uint64 = 1

	httpguard.SetAllowPrivate(true)
	t.Cleanup(func() { httpguard.SetAllowPrivate(false) })

	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, h := range hosts {
		addresses[i] = h.Address()
	}
	escrow := &bridge.EscrowInfo{
		EscrowID:         escrowID,
		EpochID:          7,
		Amount:           1_000_000,
		CreatorAddress:   user.Address(),
		Slots:            addresses,
		TokenPrice:       1,
		ExecutionTimeout: 1,
	}

	execStore := newManagerTestStore(t)
	execMgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(
		execStore, hosts[1], stub.NewInferenceEngine(), stub.NewValidationEngine(),
		nil, testutil.RuntimeTestVersion, &mockBridge{escrow: escrow}, nil, nil,
	))
	e := echo.New()
	execMgr.Register(e.Group(devshardpkg.DefaultRoutePrefix()))
	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	_, err := execStore.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	verStore := newManagerTestStore(t)
	verMgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(
		verStore, hosts[2], stub.NewInferenceEngine(), stub.NewValidationEngine(),
		nil, testutil.RuntimeTestVersion, &mockBridge{
			escrow: escrow,
			// The first SelectTransport for this host wins the shared PeerConn.
			// wireHostToHost runs inside sessionForOwner, so the executor URL
			// has to be the httptest server before that call.
			hostURLs: map[string]string{hosts[1].Address(): ts.URL},
		}, nil, nil,
	))
	srv, err := verMgr.sessionForOwner(escrowID, user.Address())
	require.NoError(t, err)
	require.NotNil(t, srv)

	start := testutil.SignDiff(t, user, escrowID, 1, []*types.DevshardTx{
		testutil.StartTxVersioned(inferenceID, testutil.RuntimeTestVersion),
	})
	confirmedAt := time.Now().Unix() - 30
	receipt := testutil.SignExecutorReceipt(
		t, hosts[1], escrowID, inferenceID, testutil.TestPromptHash[:],
		"llama", 100, testutil.TestMaxTokens, 1000, confirmedAt,
	)
	confirm := testutil.SignDiff(t, user, escrowID, 2, []*types.DevshardTx{
		{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId: inferenceID,
			ExecutorSig: receipt,
			ConfirmedAt: confirmedAt,
		}}},
	})
	_, err = srv.Host().HandleRequest(context.Background(), host.HostRequest{Diffs: []types.Diff{start, confirm}})
	require.NoError(t, err)
	require.Equal(t, types.StatusStarted, srv.Host().SnapshotState().Inferences[inferenceID].Status)

	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = devshardpkg.DefaultRoutePrefix()
	httpClient := transport.NewHTTPClient(ts.URL, escrowID, hosts[2], cfg)
	selected := transport.SelectTransport(httpClient, hosts[1].Address(), transport.AllRPCEndpoints(), nil)
	rpc, ok := selected.(*transport.RPCClient)
	require.True(t, ok, "SelectTransport returned %T", selected)
	t.Cleanup(rpc.Close)
	for _, old := range srv.PeerClients() {
		old.Close()
	}
	srv.SetPeerClients(map[int]transport.HostPeerClient{1: rpc})

	readyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, rpc.WaitReady(readyCtx))

	resp, err := srv.ServeVerifyTimeout(context.Background(), transport.VerifyTimeoutRequest{
		InferenceID: inferenceID,
		Reason:      transport.TimeoutReasonToString(types.TimeoutReason_TIMEOUT_REASON_EXECUTION),
	})
	require.NoError(t, err)
	require.True(t, resp.Accept, "no finish on the cold executor, so the timeout stands")

	_, err = execStore.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound, "execution timeout must not bind a cold executor")
}
