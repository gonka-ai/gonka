package inference

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/chain"
	"common/httpguard"
	commonvalidation "common/validation"
	"devshard/internal/testutil"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcserver"
)

func TestClassifyPayloadFetchError_ClosedPeerIsNotExecutorFault(t *testing.T) {
	t.Cleanup(transport.ResetOutboundPeerReleaseForTest)

	require.NotErrorIs(t, classifyPayloadFetchError(context.Background(), transport.ErrPeerNotReady), errExecutorPayloadFault)
	require.ErrorIs(t, classifyPayloadFetchError(context.Background(), transport.ErrPeerNotReady), transport.ErrPeerNotReady)

	connReset := errors.New("connection reset by peer")
	require.ErrorIs(t, classifyPayloadFetchError(context.Background(), connReset), errExecutorPayloadFault)

	transport.ReleaseOutboundPeerConns()
	require.NotErrorIs(t, classifyPayloadFetchError(context.Background(), connReset), errExecutorPayloadFault)
	require.ErrorIs(t, classifyPayloadFetchError(context.Background(), connReset), connReset)

	gone := fmt.Errorf("payload not found: %w", commonvalidation.ErrPayloadGone)
	require.ErrorIs(t, classifyPayloadFetchError(context.Background(), gone), commonvalidation.ErrPayloadGone)
	require.NotErrorIs(t, classifyPayloadFetchError(context.Background(), gone), errExecutorPayloadFault)

	tooLarge := fmt.Errorf("read: %w", commonvalidation.ErrPayloadTooLarge)
	require.ErrorIs(t, classifyPayloadFetchError(context.Background(), tooLarge), errExecutorPayloadFault)
}

func TestFetchSignedPayloads_AttachTimeoutIsExecutorFault(t *testing.T) {
	t.Cleanup(transport.ResetOutboundPeerReleaseForTest)
	prev := payloadFetchHeaderTimeout
	payloadFetchHeaderTimeout = 20 * time.Millisecond
	t.Cleanup(func() { payloadFetchHeaderTimeout = prev })

	peer := testutil.MustGenerateKey(t)
	pc := transport.NewPeerConn(transport.PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "host",
		Signer:       peer,
		DirectMux:    true,
		DoorEscrowID: "escrow-1",
		WatchStale:   time.Minute,
	})
	t.Cleanup(pc.Close)
	rpc := transport.NewRPCClient(nil, pc, transport.ParseRPCEndpoints(transport.EndpointPayload))

	start := time.Now()
	_, err := fetchSignedPayloads(context.Background(), nil, rpc, "http://unused", "path",
		"42", "val", 1, 10, "sig", 0)
	require.Less(t, time.Since(start), time.Second)
	require.ErrorIs(t, err, transport.ErrPeerNotReady)
	require.ErrorIs(t, err, errExecutorPayloadFault)

	classified := classifyPayloadFetchError(context.Background(), err)
	require.ErrorIs(t, classified, errExecutorPayloadFault)
	phase := new(chain.Phase)
	phase.SetEpoch(10)
	verdict := executorFaultVerdict(context.Background(), phase, faultReq(10), 10, classified, true)
	require.NotNil(t, verdict)
	require.False(t, verdict.Valid)
	require.Equal(t, executorPayloadUnavailableReason, verdict.Reason)
}

func TestFetchSignedPayloads_CancelledAttachWaitIsNotExecutorFault(t *testing.T) {
	t.Cleanup(transport.ResetOutboundPeerReleaseForTest)
	peer := testutil.MustGenerateKey(t)
	pc := transport.NewPeerConn(transport.PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "host",
		Signer:       peer,
		DirectMux:    true,
		DoorEscrowID: "escrow-1",
		WatchStale:   time.Minute,
	})
	t.Cleanup(pc.Close)
	rpc := transport.NewRPCClient(nil, pc, transport.ParseRPCEndpoints(transport.EndpointPayload))

	_, err := fetchSignedPayloads(cancelledCtx(), nil, rpc, "http://unused", "path",
		"42", "val", 1, 10, "sig", 0)
	require.ErrorIs(t, err, transport.ErrPeerNotReady)
	require.NotErrorIs(t, err, errExecutorPayloadFault)
	require.NotErrorIs(t, classifyPayloadFetchError(cancelledCtx(), err), errExecutorPayloadFault)
}

func TestFetchSignedPayloads_ReleasedPeerAttachWaitIsNotExecutorFault(t *testing.T) {
	t.Cleanup(transport.ResetOutboundPeerReleaseForTest)
	transport.ReleaseOutboundPeerConns()
	prev := payloadFetchHeaderTimeout
	payloadFetchHeaderTimeout = 20 * time.Millisecond
	t.Cleanup(func() { payloadFetchHeaderTimeout = prev })

	peer := testutil.MustGenerateKey(t)
	pc := transport.NewPeerConn(transport.PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "host",
		Signer:       peer,
		DirectMux:    true,
		DoorEscrowID: "escrow-1",
		WatchStale:   time.Minute,
	})
	t.Cleanup(pc.Close)
	rpc := transport.NewRPCClient(nil, pc, transport.ParseRPCEndpoints(transport.EndpointPayload))

	_, err := fetchSignedPayloads(context.Background(), nil, rpc, "http://unused", "path",
		"42", "val", 1, 10, "sig", 0)
	require.Error(t, err)
	require.NotErrorIs(t, err, errExecutorPayloadFault)
	require.NotErrorIs(t, classifyPayloadFetchError(context.Background(), err), errExecutorPayloadFault)
}

func TestFetchSignedPayloads_ClosedConnIsNotExecutorFault(t *testing.T) {
	t.Cleanup(transport.ResetOutboundPeerReleaseForTest)
	peer := testutil.MustGenerateKey(t)
	pc := transport.NewPeerConn(transport.PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "host",
		Signer:       peer,
		DirectMux:    true,
		DoorEscrowID: "escrow-1",
		WatchStale:   time.Minute,
	})
	pc.Close()
	rpc := transport.NewRPCClient(nil, pc, transport.ParseRPCEndpoints(transport.EndpointPayload))

	_, err := fetchSignedPayloads(context.Background(), nil, rpc, "http://unused", "path",
		"42", "val", 1, 10, "sig", 0)
	require.ErrorIs(t, err, transport.ErrPeerNotReady)
	require.NotErrorIs(t, classifyPayloadFetchError(context.Background(), err), errExecutorPayloadFault)
}

func TestFetchSignedPayloads_ReleaseUnblocksGetPayload(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	t.Cleanup(transport.ResetOutboundPeerReleaseForTest)
	prev := payloadFetchRetryBackoff
	payloadFetchRetryBackoff = 0
	t.Cleanup(func() { payloadFetchRetryBackoff = prev })

	entered := make(chan struct{})
	rpc := newPayloadRPCClient(t, func(ctx context.Context, _ rpcserver.SessionCore, _ string, _ *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})

	done := make(chan error, 1)
	go func() {
		_, err := fetchSignedPayloads(context.Background(), nil, rpc, "http://unused", "path",
			"42", "val", 1, 10, "sig", 0)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("GetPayload did not start")
	}

	start := time.Now()
	transport.ReleaseOutboundPeerConns()
	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GetPayload still blocked after release")
	}
	require.Less(t, time.Since(start), 2*time.Second)
	require.Error(t, err)
	require.NotErrorIs(t, classifyPayloadFetchError(context.Background(), err), errExecutorPayloadFault)
}
